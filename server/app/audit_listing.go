// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
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
	// BeforeCreatedAt and BeforeID continue a page at microsecond precision.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
}

type auditListingStore interface {
	List(context.Context, store.AuditListOptions) ([]*model.AuditEvent, error)
	Count(context.Context, store.AuditListOptions, int) (int, error)
	ListSelf(context.Context, store.AuditSelfListOptions) ([]*store.AuditSelfEntry, error)
	CountSelf(context.Context, store.AuditSelfListOptions) (int, error)
}

type auditListingAuthorizer interface {
	AuthorizeList(context.Context, invocation.Invocation) (store.AuditVisibilityScope, error)
}

// auditListingServiceDependencies are the collaborators of auditListingService;
// newAuditListingService validates them once and auditListingService keeps them in deps.
type auditListingServiceDependencies struct {
	Audits        auditListingStore
	Authorization auditListingAuthorizer
}

type auditListingService struct {
	deps auditListingServiceDependencies
}

func newAuditListingService(deps auditListingServiceDependencies) *auditListingService {
	return &auditListingService{deps: deps}
}

func (s *auditListingService) ListAuditEvents(ctx context.Context, invocation invocation.Invocation, query ListAuditEventsQuery) (CountedPage[*model.AuditEvent], error) {
	visibility, err := s.deps.Authorization.AuthorizeList(ctx, invocation)
	if err != nil {
		return CountedPage[*model.AuditEvent]{}, err
	}
	validAcademicVisibility := len(visibility.AllowedActions) > 0 &&
		((visibility.AcademicInstitutionWide && len(visibility.AcademicUnitRootIDs) == 0) ||
			(!visibility.AcademicInstitutionWide && len(visibility.AcademicUnitRootIDs) > 0))
	if !visibility.InstitutionWide && !validAcademicVisibility {
		return CountedPage[*model.AuditEvent]{}, authorizationDeniedError()
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 200 ||
		(query.ActorID != "" && !model.UserID(query.ActorID).IsValid()) ||
		(query.BeforeID != "" && !model.AuditEventID(query.BeforeID).IsValid()) ||
		(query.Resource != nil && query.Resource.Validate() != nil) ||
		(!query.Since.IsZero() && !query.Until.IsZero() && !query.Since.Before(query.Until)) {
		return CountedPage[*model.AuditEvent]{}, apperror.New("audit.query.invalid")
	}
	options := store.AuditListOptions{
		ActorID: model.UserID(strings.TrimSpace(query.ActorID.String())), Action: strings.TrimSpace(query.Action),
		Resource: query.Resource, Since: query.Since, Until: query.Until, BeforeCreatedAt: query.BeforeCreatedAt, BeforeID: query.BeforeID, Limit: query.Limit + 1,
		Visibility: visibility,
	}
	events, err := s.deps.Audits.List(ctx, options)
	if err != nil {
		return CountedPage[*model.AuditEvent]{}, auditListingError(err)
	}
	page := CountedPage[*model.AuditEvent]{Page: newPage(events, query.Limit)}
	if !visibility.InstitutionWide {
		page.Items = academicAuditProjection(page.Items)
	}
	total, consistent, err := countFirstPage(query.BeforeID.IsZero() && query.BeforeCreatedAt.IsZero(), len(page.Items), func(maximum int) (int, error) {
		return s.deps.Audits.Count(ctx, options, maximum)
	})
	if err != nil || !consistent {
		return CountedPage[*model.AuditEvent]{}, auditListingError(err)
	}
	page.PageTotal = total
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

type auditListingAuthorization struct {
	authorization *accessControlService
	institutions  store.InstitutionStore
}

func (a auditListingAuthorization) AuthorizeList(ctx context.Context, invocation invocation.Invocation) (store.AuditVisibilityScope, error) {
	institution, err := a.institutions.GetSingleton(ctx)
	if err != nil {
		return store.AuditVisibilityScope{}, auditListingError(err)
	}
	principal := invocation.Principal()
	global, err := a.authorization.authorizedScopes(ctx, principal, model.ActionAuditView, model.ResourceInstitution)
	if err != nil {
		return store.AuditVisibilityScope{}, err
	}
	if global.InstitutionWide {
		resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
		if err = a.authorization.deps.Audit.RecordAuthorizationDecision(ctx, principal, model.ActionAuditView, resource, model.RoleScopeInstitution, institution.ID.String(), invocation.RequestMetadata(), true); err != nil {
			return store.AuditVisibilityScope{}, err
		}
		return store.AuditVisibilityScope{InstitutionWide: true}, nil
	}
	academic, err := a.authorization.authorizedScopes(ctx, principal, model.ActionAcademicAuditView, model.ResourceAcademicUnit)
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
	if err := a.authorization.deps.Audit.RecordAuthorizationDecision(ctx, principal, model.ActionAcademicAuditView, resource, scopeType, scopeID, invocation.RequestMetadata(), allowed); err != nil {
		return store.AuditVisibilityScope{}, err
	}
	if !allowed {
		return store.AuditVisibilityScope{}, authorizationDeniedError()
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
