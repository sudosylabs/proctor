// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package administration owns the institution administrator's access and
// retention controls: Roles and Role Bindings, the Access Policy and public
// access discovery, the Retention Policy, retention control, previews,
// records and candidate notices, and the audit listings. One flat Service
// holds every one of these use cases, one file per concern; its fields are
// the Store contracts it calls and one of each collaborator: the Authorizer,
// the mutation auditor, Mail and the realtime Publisher. effects.go builds the
// realtime events a committed change publishes.
//
// The retention Jobs' system audit and notice dispatcher live here too,
// outside Service; composition builds them with NewRetentionJobs for the Job
// catalog.
//
// Counts, in counts.go, is a second, small service: the bounded counts of
// work waiting on an administrator (pending Invitations, failed Jobs and mail
// deliveries, open onboarding imports) behind the administration navigation.
// It holds one counting function per resource, supplied by the service that
// owns the resource, so it imports none of them.
//
// Start with administration.go for the Service and its collaborators, then
// open the file named for the concern you are changing.
package administration

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Authorizer is the part of *access.Authorizer the administration use cases
// call. A use case authorizes an Action on a concrete Resource immediately
// before the work it guards.
type Authorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
	// GrantedScopes reports where the caller holds an Action; a scoped list
	// records its one decision with RecordAuthorizationDecision.
	GrantedScopes(context.Context, model.Principal, model.Action, model.ResourceType) (access.AuthorizedScopeConstraint, error)
	// GrantedUserVisibility reports the Users the caller can see, without a
	// decision; an audit page names only those.
	GrantedUserVisibility(context.Context, model.Principal) (store.UserVisibilityScope, error)
	RecordAuthorizationDecision(
		context.Context,
		model.Principal,
		model.Action,
		model.Resource,
		model.RoleScopeType,
		string,
		model.RequestMetadata,
		bool,
	) error
	// CanDelegateActionsAtScope reports whether the caller may grant every
	// Action at the scope.
	CanDelegateActionsAtScope(context.Context, model.Principal, []string, model.RoleScopeType, string) (bool, error)
	AuthorizeRoleBindingPreflight(context.Context, invocation.Invocation, model.Action) error
	AuthorizeRoleBindingScope(context.Context, invocation.Invocation, model.Action, model.RoleScopeType, string) (model.Resource, error)
}

var _ Authorizer = (*access.Authorizer)(nil)

// Dependencies are the collaborators New distributes to the Service.
// Publisher delivers the realtime effects of a committed change.
type Dependencies struct {
	Store        store.Catalog
	Authorizer   Authorizer
	Auditor      access.MutationAuditor
	Capabilities identity.AccessPolicyCapabilitySource
	Mail         Mail
	Publisher    realtime.Publisher
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

// Service holds every administration use case. Its fields are the Store
// contracts it calls and one of each collaborator.
type Service struct {
	roles             store.RoleStore
	roleBindings      store.RoleBindingStore
	users             store.UserStore
	institutions      store.InstitutionStore
	audits            store.AuditStore
	accessPolicies    store.AccessPolicyStore
	retentionPolicies store.RetentionPolicyStore
	retention         store.RetentionStore

	authorizer   Authorizer
	auditor      access.MutationAuditor
	publisher    realtime.Publisher
	mail         Mail
	capabilities identity.AccessPolicyCapabilitySource

	// canonicalOrigin, supportAddress and passwordMinimumLength are the
	// operator-owned facts access discovery publishes unchanged.
	canonicalOrigin         string
	supportAddress          string
	passwordMinimumLength   int
	recentAuthenticationTTL time.Duration
	// sessionLimits are the Access Policy's Session length bounds, derived
	// in New from the deployment Session lifetimes.
	sessionLimits model.SessionLengthLimits
	now           func() time.Time
}

// New builds the Service, keeping the Store contracts it calls. It refuses
// an invalid access discovery presentation and missing collaborators.
func New(deps Dependencies) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("administration store catalog is required")
	}

	catalog := deps.Store
	parsed, err := url.Parse(deps.CanonicalOrigin)
	supportAddress := strings.ToLower(strings.TrimSpace(deps.SupportAddress))
	if deps.PasswordMinimumLength < 1 || (supportAddress != "" && !model.IsValidEmail(supportAddress)) {
		return nil, errors.New("access policy public presentation is invalid")
	}

	if catalog.AccessPolicy() == nil ||
		catalog.Institution() == nil ||
		deps.Authorizer == nil ||
		deps.Capabilities == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Now == nil ||
		deps.RecentAuthenticationTTL <= 0 ||
		err != nil ||
		parsed.Scheme == "" ||
		parsed.Host == "" ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, errors.New("access policy service dependencies are invalid")
	}

	if catalog.RetentionPolicy() == nil || catalog.Retention() == nil {
		return nil, errors.New("retention policy service dependencies are invalid")
	}

	return &Service{
		roles:                   catalog.Role(),
		roleBindings:            catalog.RoleBinding(),
		users:                   catalog.User(),
		institutions:            catalog.Institution(),
		audits:                  catalog.Audit(),
		accessPolicies:          catalog.AccessPolicy(),
		retentionPolicies:       catalog.RetentionPolicy(),
		retention:               catalog.Retention(),
		authorizer:              deps.Authorizer,
		auditor:                 deps.Auditor,
		publisher:               deps.Publisher,
		mail:                    deps.Mail,
		capabilities:            deps.Capabilities,
		canonicalOrigin:         deps.CanonicalOrigin,
		supportAddress:          supportAddress,
		passwordMinimumLength:   deps.PasswordMinimumLength,
		recentAuthenticationTTL: deps.RecentAuthenticationTTL,
		sessionLimits:           sessionLengthLimits(deps.Sessions, deps.RecentAuthenticationTTL),
		now:                     deps.Now,
	}, nil
}
