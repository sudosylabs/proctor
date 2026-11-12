// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListAuditEventsQuery narrows the listing: ActorID, SubjectUserID (the User
// the Event is about), Status and Resource match exactly, and Actions, each a
// known audit action, are alternatives.
type ListAuditEventsQuery struct {
	ActorID       model.UserID
	SubjectUserID model.UserID
	Actions       []string
	Status        model.AuditStatus
	Resource      *model.Resource
	// Since is inclusive and Until exclusive; when both are set Since must
	// precede Until.
	Since time.Time
	Until time.Time
	// Ascending reads oldest first; the default is newest first.
	Ascending bool
	// BeforeCreatedAt and BeforeID continue a page at microsecond precision,
	// past the previous page's last event in the read order.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
}

// AuditEventPage is one page of the audit listing with the current names of
// the records its events refer to, each present only when the caller could
// see that record now. Redacted reports an academic-only reader, whose events
// carry no parameters, so they name neither an operation nor a subject.
type AuditEventPage struct {
	paging.Counted[*model.AuditEvent]
	Names    store.AuditNames
	Redacted bool
}

// ListAuditEvents returns a page of audit events within the caller's audit
// visibility; an academic-only reader receives redacted events.
func (s *Service) ListAuditEvents(
	ctx context.Context,
	inv invocation.Invocation,
	query ListAuditEventsQuery,
) (AuditEventPage, error) {
	visibility, err := s.authorizeAuditList(ctx, inv)
	if err != nil {
		return AuditEventPage{}, err
	}

	validAcademicVisibility := len(visibility.AllowedActions) > 0 &&
		((visibility.AcademicInstitutionWide && len(visibility.AcademicUnitRootIDs) == 0) ||
			(!visibility.AcademicInstitutionWide && len(visibility.AcademicUnitRootIDs) > 0))
	if !visibility.InstitutionWide && !validAcademicVisibility {
		return AuditEventPage{}, access.DeniedError()
	}
	if query.Limit == 0 {
		query.Limit = 50
	}

	if query.Limit < 1 ||
		query.Limit > 200 ||
		(query.ActorID != "" && !query.ActorID.IsValid()) ||
		(query.SubjectUserID != "" && !query.SubjectUserID.IsValid()) ||
		(query.BeforeID != "" && !query.BeforeID.IsValid()) ||
		(query.Resource != nil && query.Resource.Validate() != nil) ||
		(!query.Since.IsZero() && !query.Until.IsZero() && !query.Since.Before(query.Until)) ||
		!validAuditListStatus(query.Status) {
		return AuditEventPage{}, apperror.New("audit.query.invalid")
	}
	actions, err := auditListActions(query.Actions)
	if err != nil {
		return AuditEventPage{}, err
	}

	options := store.AuditListOptions{
		ActorID:         model.UserID(strings.TrimSpace(query.ActorID.String())),
		SubjectUserID:   query.SubjectUserID,
		Actions:         actions,
		Status:          query.Status,
		Resource:        query.Resource,
		Since:           query.Since,
		Until:           query.Until,
		Ascending:       query.Ascending,
		BeforeCreatedAt: query.BeforeCreatedAt,
		BeforeID:        query.BeforeID,
		Limit:           query.Limit + 1,
		Visibility:      visibility,
	}
	events, err := s.audits.List(ctx, options)
	if err != nil {
		return AuditEventPage{}, auditListingError(err)
	}

	page := AuditEventPage{Counted: paging.Counted[*model.AuditEvent]{Page: paging.New(events, query.Limit)}}
	if !visibility.InstitutionWide {
		page.Items = academicAuditProjection(page.Items)
		page.Redacted = true
	}

	total, consistent, err := paging.CountFirst(query.BeforeID.IsZero() &&
		query.BeforeCreatedAt.IsZero(), len(page.Items), func(maximum int) (int, error) {
		return s.audits.Count(ctx, options, maximum)
	})
	if err != nil || !consistent {
		return AuditEventPage{}, auditListingError(err)
	}

	page.Total = total
	if page.Names, err = s.auditNames(ctx, inv.Principal(), page.Items, page.Redacted); err != nil {
		return AuditEventPage{}, err
	}
	return page, nil
}

// auditNames reads, in one call, the names of the actors, subjects and
// resources a page refers to, each only inside the caller's current authority
// to see it: the User directory's visibility for Users, academic_unit.view
// for Academic Units and class.view for Classes. Seeing the audit history does
// not by itself reveal a name. A redacted page names no subject, because its
// events carry no parameters to say what happened to one.
func (s *Service) auditNames(
	ctx context.Context,
	principal model.Principal,
	events []*model.AuditEvent,
	redacted bool,
) (store.AuditNames, error) {
	var options store.AuditNameOptions
	for _, event := range events {
		if !event.ActorID.IsZero() {
			options.UserIDs = appendAuditName(options.UserIDs, event.ActorID)
		}
		if !redacted && !event.SubjectUserID.IsZero() {
			options.UserIDs = appendAuditName(options.UserIDs, event.SubjectUserID)
		}
		switch event.Resource.Type {
		case model.ResourceUser:
			options.UserIDs = appendAuditName(options.UserIDs, model.UserID(event.Resource.ID))
		case model.ResourceAcademicUnit:
			options.AcademicUnitIDs = appendAuditName(options.AcademicUnitIDs, model.AcademicUnitID(event.Resource.ID))
		case model.ResourceClass:
			options.ClassIDs = appendAuditName(options.ClassIDs, model.ClassID(event.Resource.ID))
		}
	}
	if len(options.UserIDs)+len(options.AcademicUnitIDs)+len(options.ClassIDs) == 0 {
		return store.AuditNames{}, nil
	}

	var err error
	if len(options.UserIDs) > 0 {
		if options.UserVisibility, err = s.authorizer.GrantedUserVisibility(ctx, principal); err != nil {
			return store.AuditNames{}, err
		}
	}
	if len(options.AcademicUnitIDs) > 0 {
		units, scopeErr := s.authorizer.GrantedScopes(ctx, principal, model.ActionAcademicUnitView, model.ResourceAcademicUnit)
		if scopeErr != nil {
			return store.AuditNames{}, scopeErr
		}
		options.AcademicUnitInstitutionWide = units.InstitutionWide
		options.AcademicUnitRootIDs = units.AcademicUnitRootIDs
	}
	if len(options.ClassIDs) > 0 {
		classes, scopeErr := s.authorizer.GrantedScopes(ctx, principal, model.ActionClassView, model.ResourceClass)
		if scopeErr != nil {
			return store.AuditNames{}, scopeErr
		}
		options.ClassVisibility = store.ClassVisibilityScope{
			InstitutionWide:     classes.InstitutionWide,
			AcademicUnitRootIDs: classes.AcademicUnitRootIDs,
			ClassIDs:            classes.ClassIDs,
		}
	}

	names, err := s.audits.ListNames(ctx, options)
	if err != nil {
		return store.AuditNames{}, auditListingError(err)
	}
	return names, nil
}

// validAuditListStatus reports whether status is empty or an audit status.
func validAuditListStatus(status model.AuditStatus) bool {
	switch status {
	case "", model.AuditStatusAttempt, model.AuditStatusSuccess, model.AuditStatusFail:
		return true
	}
	return false
}

// auditListActions trims the requested actions, drops repeats and refuses
// more than store.MaximumAuditListActions or one outside the audit action
// catalog. An academic visibility still intersects them with its own catalog,
// so naming an action never widens what the caller reads.
func auditListActions(requested []string) ([]string, error) {
	if len(requested) > store.MaximumAuditListActions {
		return nil, apperror.New("audit.query.invalid")
	}
	known := model.AuditActions()
	var actions []string
	for _, raw := range requested {
		action := strings.TrimSpace(raw)
		if !slices.Contains(known, action) {
			return nil, apperror.New("audit.query.invalid")
		}
		if !slices.Contains(actions, action) {
			actions = append(actions, action)
		}
	}
	return actions, nil
}

func academicAuditProjection(events []*model.AuditEvent) []*model.AuditEvent {
	result := make([]*model.AuditEvent, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		projected := event.Clone()
		projected.SessionID = ""
		projected.RequestID = ""
		projected.NodeID = ""
		projected.ClientType = ""
		projected.AuthMethod = ""
		projected.IPAddress = ""
		projected.UserAgent = ""
		projected.Parameters = nil
		projected.PriorState = nil
		projected.Result = nil
		result = append(result, projected)
	}
	return result
}

// authorizeAuditList authorizes the audit listing and returns the caller's
// audit visibility: every event with Institution-wide audit.view, otherwise
// the academic events beneath the Academic Units where the caller holds
// academic_audit.view. It records exactly one authorization decision.
func (s *Service) authorizeAuditList(ctx context.Context, inv invocation.Invocation) (store.AuditVisibilityScope, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return store.AuditVisibilityScope{}, auditListingError(err)
	}

	principal := inv.Principal()
	global, err := s.authorizer.GrantedScopes(ctx, principal, model.ActionAuditView, model.ResourceInstitution)
	if err != nil {
		return store.AuditVisibilityScope{}, err
	}

	if global.InstitutionWide {
		resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
		if err = s.authorizer.RecordAuthorizationDecision(
			ctx,
			principal,
			model.ActionAuditView,
			resource,
			model.RoleScopeInstitution,
			institution.ID.String(),
			inv.RequestMetadata(),
			true,
		); err != nil {
			return store.AuditVisibilityScope{}, err
		}

		return store.AuditVisibilityScope{InstitutionWide: true}, nil
	}

	academic, err := s.authorizer.GrantedScopes(ctx, principal, model.ActionAcademicAuditView, model.ResourceAcademicUnit)
	if err != nil {
		return store.AuditVisibilityScope{}, err
	}

	allowed := academic.InstitutionWide || len(academic.AcademicUnitRootIDs) > 0
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	scopeType, scopeID := model.RoleScopeInstitution, institution.ID.String()
	if !academic.InstitutionWide && len(academic.AcademicUnitRootIDs) > 0 {
		resource = model.Resource{Type: model.ResourceAcademicUnit, ID: academic.AcademicUnitRootIDs[0].String()}
		scopeType, scopeID = model.RoleScopeAcademicUnit, resource.ID
	}

	if err := s.authorizer.RecordAuthorizationDecision(
		ctx,
		principal,
		model.ActionAcademicAuditView,
		resource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		allowed,
	); err != nil {
		return store.AuditVisibilityScope{}, err
	}

	if !allowed {
		return store.AuditVisibilityScope{}, access.DeniedError()
	}

	return store.AuditVisibilityScope{
		AcademicInstitutionWide: academic.InstitutionWide,
		AcademicUnitRootIDs:     append([]model.AcademicUnitID(nil), academic.AcademicUnitRootIDs...),
		AllowedActions:          model.AcademicAuditActions(),
	}, nil
}

func auditListingError(err error) error {

	return apperror.New("audit.unavailable").WithField("resource", "audit").Wrap(err)
}

// appendAuditName adds id once, so a page that refers to one record many
// times asks for its name once.
func appendAuditName[T comparable](ids []T, id T) []T {
	if slices.Contains(ids, id) {
		return ids
	}
	return append(ids, id)
}
