// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SelfAuditActorKind says who performed an event in a User's own history
// without naming anyone but the User: the User themself, another person with
// authority over the account, or work with no signed-in actor (Jobs,
// offline recovery, and token links such as password reset).
type SelfAuditActorKind string

// The actor kinds of self history.
const (
	SelfAuditActorSelf          SelfAuditActorKind = "self"
	SelfAuditActorAdministrator SelfAuditActorKind = "administrator"
	SelfAuditActorSystem        SelfAuditActorKind = "system"
)

// maximumSelfAuditEventFilters bounds the repeated event filter.
const maximumSelfAuditEventFilters = 64

// ListSelfAuditEventsQuery filters the caller's own history. Since is
// inclusive and Until exclusive. Events and Status narrow the closed
// catalogue; Relation selects subject or actor events, or both when empty.
type ListSelfAuditEventsQuery struct {
	Since    time.Time
	Until    time.Time
	Events   []model.AuditSelfEvent
	Status   model.AuditStatus
	Relation store.AuditSelfRelation
	// BeforeCreatedAt and BeforeID continue a page at microsecond precision.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
}

// SelfAuditEvent is the self audit projection of one event: a catalogue
// name, the outcome, and only the facts the caller may see about it. The
// resource ID is present only when the resource is the caller, the
// Institution, or an Academic Unit or Class the caller belongs or belonged
// to. The network identity is present only for the caller's own actions.
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
}

// ListSelfAuditEvents returns the current Session User's own account
// history. It is the caller's own record, like other /users/me reads, so it
// records no authorization decision; Personal Access Tokens are refused
// because the history includes credential and Session changes.
func (s *auditListingService) ListSelfAuditEvents(ctx context.Context, invocation invocation.Invocation, query ListSelfAuditEventsQuery) (Page[SelfAuditEvent], error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return Page[SelfAuditEvent]{}, apperror.New("authentication.required")
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validSelfAuditQuery(query) {
		return Page[SelfAuditEvent]{}, apperror.New("audit.query.invalid")
	}
	keys := model.AuditSelfEventKeys(query.Events, query.Status)
	if len(keys) == 0 {
		return newPage[SelfAuditEvent](nil, query.Limit), nil
	}
	entries, err := s.deps.Audits.ListSelf(ctx, store.AuditSelfListOptions{
		UserID: principal.UserID, Relation: query.Relation, Events: keys,
		Since: query.Since, Until: query.Until,
		BeforeCreatedAt: query.BeforeCreatedAt, BeforeID: query.BeforeID,
		Limit: query.Limit + 1,
	})
	if err != nil {
		return Page[SelfAuditEvent]{}, auditListingError(err)
	}
	if len(entries) > query.Limit+1 {
		return Page[SelfAuditEvent]{}, apperror.New("audit.unavailable").WithField("resource", "audit")
	}
	items := make([]SelfAuditEvent, 0, len(entries))
	for _, entry := range entries {
		item, ok := selfAuditProjection(principal.UserID, entry)
		if !ok {
			return Page[SelfAuditEvent]{}, apperror.New("audit.unavailable").WithField("resource", "audit")
		}
		items = append(items, item)
	}
	return newPage(items, query.Limit), nil
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

// selfAuditProjection applies the self audit projection rule to one entry.
// It reports false for an entry the store should never have returned.
func selfAuditProjection(caller model.UserID, entry *store.AuditSelfEntry) (SelfAuditEvent, bool) {
	if entry == nil {
		return SelfAuditEvent{}, false
	}
	event, known := model.AuditSelfEventFor(entry.Action, entry.Operation, entry.Status)
	if !known {
		return SelfAuditEvent{}, false
	}
	item := SelfAuditEvent{
		ID: entry.ID, CreatedAt: entry.CreatedAt, Event: event, Status: entry.Status,
		Resource: model.Resource{Type: entry.Resource.Type}, ErrorCode: entry.ErrorCode,
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
