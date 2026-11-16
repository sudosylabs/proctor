// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SelfAuditActorKind says who performed an event in a user's own history
// without naming anyone else: the user, another person with authority over
// the account, or work with no signed-in actor (jobs, offline recovery, token
// links such as password reset).
type SelfAuditActorKind string

// The actor kinds of self history.
const (
	SelfAuditActorSelf          SelfAuditActorKind = "self"
	SelfAuditActorAdministrator SelfAuditActorKind = "administrator"
	SelfAuditActorSystem        SelfAuditActorKind = "system"
)

const maximumSelfAuditEventFilters = 64

// ListSelfAuditEventsQuery filters the caller's own history. Since is
// inclusive and Until exclusive; Relation selects subject or actor events, or
// both when empty.
type ListSelfAuditEventsQuery struct {
	Since    time.Time
	Until    time.Time
	Events   []model.AuditSelfEvent
	Status   model.AuditStatus
	Relation store.AuditSelfRelation
	// Ascending reads oldest first; the default is newest first.
	Ascending bool
	// BeforeCreatedAt and BeforeID continue past the previous page's last
	// event at microsecond precision.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
}

// SelfAuditEvent is one event as its subject may see it. The resource ID is
// present only when the resource is the caller, the institution, or an
// academic unit or class the caller belongs or belonged to; the network
// identity only for the caller's own actions. SubjectName names a user the
// caller acted on only while the directory still lets the caller see them.
// The actor of an event about the caller is never named.
type SelfAuditEvent struct {
	ID         model.AuditEventID
	CreatedAt  time.Time
	Event      model.AuditSelfEvent
	Status     model.AuditStatus
	Relation   store.AuditSelfRelation
	ActorKind  SelfAuditActorKind
	Resource   model.Resource
	ErrorCode  string
	ClientType string
	IPAddress  string
	UserAgent  string

	ResourceName string
	Role         store.AuditRoleName
	SubjectName  string
}

// SelfAuditRun is two or more consecutive items on one page that a client can
// show as one line. Start indexes the page's items. A run never reaches past
// the page, so the next page may continue it.
type SelfAuditRun struct {
	Start  int
	Length int
}

// SelfAuditPage is one page of the caller's history. Only the first page,
// read without a keyset, is counted, up to store.MaximumAuditSelfCount.
type SelfAuditPage struct {
	paging.Page[SelfAuditEvent]
	Runs              []SelfAuditRun
	Counted           bool
	Total             int
	TotalIsLowerBound bool
}

// ListSelfAuditEvents returns the session user's own account history. Like
// other /users/me reads it records no authorization decision. Personal
// access tokens are refused because the history includes credential and
// session changes.
func (s *Service) ListSelfAuditEvents(
	ctx context.Context,
	inv invocation.Invocation,
	query ListSelfAuditEventsQuery,
) (SelfAuditPage, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return SelfAuditPage{}, apperror.New("authentication.required")
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validSelfAuditQuery(query) {
		return SelfAuditPage{}, apperror.New("audit.query.invalid")
	}

	counted := query.BeforeID.IsZero()
	keys := model.AuditSelfEventKeys(query.Events, query.Status)
	if len(keys) == 0 {
		return SelfAuditPage{Page: paging.New[SelfAuditEvent](nil, query.Limit), Counted: counted}, nil
	}

	options := store.AuditSelfListOptions{
		UserID:          principal.UserID,
		Relation:        query.Relation,
		Events:          keys,
		Since:           query.Since,
		Until:           query.Until,
		Ascending:       query.Ascending,
		BeforeCreatedAt: query.BeforeCreatedAt,
		BeforeID:        query.BeforeID,
		Limit:           query.Limit + 1,
	}
	entries, err := s.audits.ListSelf(ctx, options)
	if err != nil {
		return SelfAuditPage{}, auditListingError(err)
	}
	if len(entries) > query.Limit+1 {
		return SelfAuditPage{}, apperror.New("audit.unavailable").WithField("resource", "audit")
	}

	items := make([]SelfAuditEvent, 0, len(entries))
	roles := make([]model.RoleID, 0, len(entries))
	for _, entry := range entries {
		item, ok := selfAuditProjection(principal.UserID, entry)
		if !ok {
			return SelfAuditPage{}, apperror.New("audit.unavailable").WithField("resource", "audit")
		}

		items = append(items, item)
		roles = append(roles, selfAuditRole(principal.UserID, entry))
	}

	page := SelfAuditPage{Page: paging.New(items, query.Limit), Counted: counted}
	page.Runs = selfAuditRuns(page.Items, entries)
	if err = s.nameSelfAuditEvents(ctx, principal, page.Items, entries, roles); err != nil {
		return SelfAuditPage{}, err
	}
	if !counted {
		return page, nil
	}
	if page.Total, err = s.audits.CountSelf(ctx, options); err != nil {
		return SelfAuditPage{}, auditListingError(err)
	}
	if page.Total < len(page.Items) || page.Total > store.MaximumAuditSelfCount {
		return SelfAuditPage{}, apperror.New("audit.unavailable").WithField("resource", "audit")
	}

	page.TotalIsLowerBound = page.Total == store.MaximumAuditSelfCount
	return page, nil
}

func validSelfAuditQuery(query ListSelfAuditEventsQuery) bool {
	if query.Limit < 1 || query.Limit > 200 {
		return false
	}
	if !query.Since.IsZero() && !query.Until.IsZero() && !query.Since.Before(query.Until) {
		return false
	}
	switch query.Status {
	case "", model.AuditStatusSuccess, model.AuditStatusFail:
	default:
		return false
	}
	switch query.Relation {
	case "", store.AuditSelfRelationSubject, store.AuditSelfRelationActor:
	default:
		return false
	}
	if len(query.Events) > maximumSelfAuditEventFilters {
		return false
	}
	for _, event := range query.Events {
		if !event.IsValid() {
			return false
		}
	}
	return query.BeforeCreatedAt.IsZero() == query.BeforeID.IsZero() &&
		(query.BeforeID.IsZero() || query.BeforeID.IsValid())
}

// selfAuditProjection reports false for an entry the store should never have
// returned.
func selfAuditProjection(caller model.UserID, entry *store.AuditSelfEntry) (SelfAuditEvent, bool) {
	if entry == nil {
		return SelfAuditEvent{}, false
	}

	event, known := model.AuditSelfEventFor(entry.Action, entry.Operation, entry.Status)
	if !known {
		return SelfAuditEvent{}, false
	}

	item := SelfAuditEvent{
		ID:         entry.ID,
		CreatedAt:  entry.CreatedAt,
		Event:      event,
		Status:     entry.Status,
		Resource:   model.Resource{Type: entry.Resource.Type},
		ErrorCode:  entry.ErrorCode,
		ClientType: entry.ClientType,
	}
	switch {
	case entry.SubjectUserID == caller:
		item.Relation = store.AuditSelfRelationSubject
	case entry.ActorID == caller:
		item.Relation = store.AuditSelfRelationActor
	default:
		return SelfAuditEvent{}, false
	}

	switch {
	case entry.ActorID == caller:
		item.ActorKind = SelfAuditActorSelf
		item.IPAddress, item.UserAgent = entry.IPAddress, entry.UserAgent
	case entry.ActorID.IsZero():
		item.ActorKind = SelfAuditActorSystem
	default:
		item.ActorKind = SelfAuditActorAdministrator
	}

	if entry.ResourceRelated {
		item.Resource.ID = entry.Resource.ID
	}

	return item, true
}

// selfAuditRole is the role a binding change bound to the caller, or zero for
// any other event, including a change the caller made for someone else.
func selfAuditRole(caller model.UserID, entry *store.AuditSelfEntry) model.RoleID {
	if entry.SubjectUserID != caller {
		return ""
	}
	return entry.RoleID
}

// selfAuditRuns takes the entries the items were projected from, in the same
// order; the probe row past the page is excluded by the items' length.
func selfAuditRuns(items []SelfAuditEvent, entries []*store.AuditSelfEntry) []SelfAuditRun {
	var runs []SelfAuditRun
	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && sameSelfAuditRun(items[start], entries[start], items[end], entries[end]) {
			end++
		}
		if end-start >= 2 && items[start].Event.Repeats() {
			runs = append(runs, SelfAuditRun{Start: start, Length: end - start})
		}
		start = end
	}
	return runs
}

// sameSelfAuditRun compares relation and operation too: the relation decides
// whether the line is about the caller or by them, and the operation
// separates facts one event name covers, such as revoking one's own session
// or another user's.
func sameSelfAuditRun(left SelfAuditEvent, leftEntry *store.AuditSelfEntry, right SelfAuditEvent, rightEntry *store.AuditSelfEntry) bool {
	return left.ActorKind == right.ActorKind &&
		left.Event == right.Event &&
		left.Status == right.Status &&
		left.Relation == right.Relation &&
		leftEntry.Operation == rightEntry.Operation
}

// nameSelfAuditEvents needs no further visibility check for academic units,
// classes and roles, because the projection already applied the
// related-resource rule. A user is named only inside the caller's current
// user visibility, and the person who acted on the caller is never named.
func (s *Service) nameSelfAuditEvents(
	ctx context.Context,
	principal model.Principal,
	items []SelfAuditEvent,
	entries []*store.AuditSelfEntry,
	roles []model.RoleID,
) error {
	options := store.AuditNameOptions{
		AcademicUnitInstitutionWide: true,
		ClassVisibility:             store.ClassVisibilityScope{InstitutionWide: true},
	}
	for index, item := range items {
		switch {
		case item.Resource.ID == "":
		case item.Resource.Type == model.ResourceAcademicUnit:
			options.AcademicUnitIDs = append(options.AcademicUnitIDs, model.AcademicUnitID(item.Resource.ID))
		case item.Resource.Type == model.ResourceClass:
			options.ClassIDs = append(options.ClassIDs, model.ClassID(item.Resource.ID))
		}
		if !roles[index].IsZero() {
			options.RoleIDs = append(options.RoleIDs, roles[index])
		}
		if subject := selfAuditSubject(item, entries[index]); !subject.IsZero() {
			options.UserIDs = appendAuditName(options.UserIDs, subject)
		}
	}
	if len(options.AcademicUnitIDs)+len(options.ClassIDs)+len(options.RoleIDs)+len(options.UserIDs) == 0 {
		return nil
	}
	if len(options.UserIDs) > 0 {
		visibility, err := s.authorizer.GrantedUserVisibility(ctx, principal)
		if err != nil {
			return err
		}
		options.UserVisibility = visibility
	}

	names, err := s.audits.ListNames(ctx, options)
	if err != nil {
		return auditListingError(err)
	}
	for index := range items {
		item := &items[index]
		switch item.Resource.Type {
		case model.ResourceAcademicUnit:
			item.ResourceName = names.AcademicUnits[model.AcademicUnitID(item.Resource.ID)]
		case model.ResourceClass:
			item.ResourceName = names.Classes[model.ClassID(item.Resource.ID)]
		}
		item.Role = names.Roles[roles[index]]
		if subject := selfAuditSubject(*item, entries[index]); !subject.IsZero() {
			item.SubjectName = names.Users[subject]
		}
	}
	return nil
}

// selfAuditSubject is the other user an event the caller performed was
// about, or zero for an event about the caller or about no user.
func selfAuditSubject(item SelfAuditEvent, entry *store.AuditSelfEntry) model.UserID {
	if item.Relation != store.AuditSelfRelationActor {
		return ""
	}
	return entry.SubjectUserID
}
