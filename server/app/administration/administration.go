// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package administration owns the institution administrator's access and
// retention controls: Roles and Role Bindings, the Access Policy and public
// access discovery, the Retention Policy, retention control, previews,
// records and candidate notices, and the audit listings. Service holds every
// one of these use cases, one file per concern.
//
// The retention Jobs' system audit and notice dispatcher live here too;
// composition builds them with NewRetentionJobAudit and
// NewRetentionNoticeDispatcher for the Job catalog.
//
// Start with administration.go for the Service and its collaborators, then
// open the file named for the concern you are changing.
package administration

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// resourceAuthorizer is the one authorization port. A use case authorizes an
// Action on a concrete Resource immediately before the work it guards.
type resourceAuthorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// institutionSingleton reads the installation's singleton Institution.
type institutionSingleton interface {
	GetSingleton(context.Context) (*model.Institution, error)
}

// Effects publishes the realtime effects of an administration change after it
// commits. Report records a publication that failed; the use case still
// succeeds.
type Effects interface {
	// InvalidateAuthorization drops one User's cached authorization, or
	// every User's when the identifier is empty.
	identity.AuthorizationInvalidationEffects
	// AccessPolicyChanged propagates a committed Access Policy replacement.
	accessPolicyEffects
	accessPolicyEffectFailures
}

// Dependencies are the collaborators New distributes to the Service.
type Dependencies struct {
	Store        store.Catalog
	Authorizer   *access.Authorizer
	Auditor      access.MutationAuditor
	Capabilities identity.AccessPolicyCapabilitySource
	Mail         Mail
	Effects      Effects
	// CanonicalOrigin, SupportAddress and PasswordMinimumLength are the
	// operator-owned facts access discovery publishes.
	CanonicalOrigin         string
	SupportAddress          string
	PasswordMinimumLength   int
	RecentAuthenticationTTL time.Duration
	// Sessions are the deployment Session lifetimes that bound the Access
	// Policy's Session lengths.
	Sessions identity.SessionPolicy
	Now      func() time.Time
}

// Service holds every administration use case. Each concern keeps its own
// focused implementation, embedded here so the Service's method set is their
// exported methods.
type Service struct {
	*roleService
	*roleBindingService
	*accessPolicyService
	*retentionPolicyService
	*auditListingService
}

// New builds the Service and every focused implementation it embeds.
func New(deps Dependencies) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("administration store catalog is required")
	}
	catalog := deps.Store
	accessPolicies, err := newAccessPolicyService(accessPolicyServiceDependencies{Policies: catalog.AccessPolicy(), Institutions: catalog.Institution(), Authorization: deps.Authorizer, Capabilities: deps.Capabilities, Audit: deps.Auditor, Effects: deps.Effects, EffectFailures: deps.Effects, CanonicalOrigin: deps.CanonicalOrigin, SupportAddress: deps.SupportAddress, PasswordMinimumLength: deps.PasswordMinimumLength, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Sessions: deps.Sessions, Now: deps.Now})
	if err != nil {
		return nil, err
	}
	retentionPolicy, err := newRetentionPolicyService(retentionPolicyServiceDependencies{Policies: catalog.RetentionPolicy(), Lifecycle: catalog.Retention(), Institutions: catalog.Institution(), Authorization: deps.Authorizer, Audit: deps.Auditor, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Now: deps.Now})
	if err != nil {
		return nil, err
	}
	roleAuthorization := roleAuthorization{authorization: deps.Authorizer, institutions: catalog.Institution()}
	return &Service{
		roleService: newRoleService(roleServiceDependencies{Roles: catalog.Role(), Authorization: roleAuthorization, Audit: deps.Auditor, Effects: roleRealtimeEffects{effects: deps.Effects}, Now: deps.Now}),
		roleBindingService: newRoleBindingService(roleBindingServiceDependencies{Bindings: catalog.RoleBinding(), Roles: catalog.Role(), Users: catalog.User(), Authorization: roleAuthorization,
			Capabilities: deps.Capabilities, Audit: deps.Auditor, Mail: deps.Mail, Effects: roleBindingRealtimeEffects{effects: deps.Effects}, Now: deps.Now}),
		accessPolicyService:    accessPolicies,
		retentionPolicyService: retentionPolicy,
		auditListingService: newAuditListingService(auditListingServiceDependencies{Audits: catalog.Audit(),
			Authorization: auditListingAuthorization{authorization: deps.Authorizer, institutions: catalog.Institution()}}),
	}, nil
}
