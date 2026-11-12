// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ListAuditEventsQuery struct {
	ActorID  model.UserID
	Action   string
	Resource *model.Resource
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

// ListAuditEvents returns a page of audit events within the caller's audit
// visibility; an academic-only reader receives redacted events.
func (s *Service) ListAuditEvents(
	ctx context.Context,
	inv invocation.Invocation,
	query ListAuditEventsQuery,
) (paging.Counted[*model.AuditEvent], error) {
	visibility, err := s.authorizeAuditList(ctx, inv)
	if err != nil {
		return paging.Counted[*model.AuditEvent]{}, err
	}

	validAcademicVisibility := len(visibility.AllowedActions) > 0 &&
		((visibility.AcademicInstitutionWide && len(visibility.AcademicUnitRootIDs) == 0) ||
			(!visibility.AcademicInstitutionWide && len(visibility.AcademicUnitRootIDs) > 0))
	if !visibility.InstitutionWide && !validAcademicVisibility {
		return paging.Counted[*model.AuditEvent]{}, access.DeniedError()
	}
	if query.Limit == 0 {
		query.Limit = 50
	}

	if query.Limit < 1 ||
		query.Limit > 200 ||
		(query.ActorID != "" && !query.ActorID.IsValid()) ||
		(query.BeforeID != "" && !query.BeforeID.IsValid()) ||
		(query.Resource != nil && query.Resource.Validate() != nil) ||
		(!query.Since.IsZero() && !query.Until.IsZero() && !query.Since.Before(query.Until)) {
		return paging.Counted[*model.AuditEvent]{}, apperror.New("audit.query.invalid")
	}

	options := store.AuditListOptions{
		ActorID:         model.UserID(strings.TrimSpace(query.ActorID.String())),
		Action:          strings.TrimSpace(query.Action),
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
		return paging.Counted[*model.AuditEvent]{}, auditListingError(err)
	}

	page := paging.Counted[*model.AuditEvent]{Page: paging.New(events, query.Limit)}
	if !visibility.InstitutionWide {
		page.Items = academicAuditProjection(page.Items)
	}

	total, consistent, err := paging.CountFirst(query.BeforeID.IsZero() &&
		query.BeforeCreatedAt.IsZero(), len(page.Items), func(maximum int) (int, error) {
		return s.audits.Count(ctx, options, maximum)
	})
	if err != nil || !consistent {
		return paging.Counted[*model.AuditEvent]{}, auditListingError(err)
	}

	page.Total = total
	return page, nil
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
