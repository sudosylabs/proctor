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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// catalogFake hands New the Store contracts a test supplies; a contract left
// nil reaches New as a missing Store.
type catalogFake struct {
	store.Catalog
	roles             store.RoleStore
	roleBindings      store.RoleBindingStore
	users             store.UserStore
	institutions      store.InstitutionStore
	audits            store.AuditStore
	accessPolicies    store.AccessPolicyStore
	retentionPolicies store.RetentionPolicyStore
	retention         store.RetentionStore
}

func (c *catalogFake) Role() store.RoleStore                       { return c.roles }
func (c *catalogFake) RoleBinding() store.RoleBindingStore         { return c.roleBindings }
func (c *catalogFake) User() store.UserStore                       { return c.users }
func (c *catalogFake) Institution() store.InstitutionStore         { return c.institutions }
func (c *catalogFake) Audit() store.AuditStore                     { return c.audits }
func (c *catalogFake) AccessPolicy() store.AccessPolicyStore       { return c.accessPolicies }
func (c *catalogFake) RetentionPolicy() store.RetentionPolicyStore { return c.retentionPolicies }
func (c *catalogFake) Retention() store.RetentionStore             { return c.retention }

// dependenciesOf rebuilds the Dependencies a Service was built from, so a
// construction test can omit one collaborator at a time.
func dependenciesOf(s *Service) Dependencies {
	return Dependencies{
		Store: &catalogFake{
			roles: s.roles, roleBindings: s.roleBindings, users: s.users, institutions: s.institutions, audits: s.audits,
			accessPolicies: s.accessPolicies, retentionPolicies: s.retentionPolicies, retention: s.retention,
		},
		Authorizer: s.authorizer, Auditor: s.auditor, Capabilities: s.capabilities, Mail: s.mail, Publisher: s.publisher,
		CanonicalOrigin: s.canonicalOrigin, SupportAddress: s.supportAddress, PasswordMinimumLength: s.passwordMinimumLength,
		RecentAuthenticationTTL: s.recentAuthenticationTTL, Now: s.now,
	}
}

// institutionStoreFake answers the singleton Institution read every
// Institution-scoped authorization starts with.
type institutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
	err         error
}

func (s *institutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, s.err
}

func anyInstitution() *institutionStoreFake {
	return &institutionStoreFake{institution: &model.Institution{ID: model.NewInstitutionID()}}
}

// publisherIn returns a recording Publisher that also places each
// authorization invalidation in the log a test shares with its other fakes.
func publisherIn(events *[]string) *realtimetest.Publisher {
	return &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		if call.Method == realtimetest.InvalidateAuthorization {
			*events = append(*events, "invalidate-authorization")
		}
	}}
}

func selfAuditSessionInvocation() invocation.Invocation {
	at := time.Now().UTC()
	return invocation.New(model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: at, ClientType: model.SessionClientWeb,
	}, model.RequestMetadata{RequestID: "self-audit"})
}

type institutionAuditorFake struct {
	events    *[]string
	beginID   string
	beginIDs  []string
	failCode  string
	action    model.Action
	resource  model.Resource
	resources []model.Resource
	scopeType model.RoleScopeType
	scopeID   string
	subjects  []model.UserID
	values    []map[string]any
}

func (a *institutionAuditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	if !attempt.Subject.IsZero() {
		a.subjects = append(a.subjects, attempt.Subject)
	}
	if attempt.ScopeType != "" || attempt.ScopeID != "" {
		a.scopeType, a.scopeID = attempt.ScopeType, attempt.ScopeID
	}

	*a.events = append(*a.events, "audit-begin")
	a.values = append(a.values, attempt.Value)
	a.action, a.resource = attempt.Action, attempt.Resource
	a.resources = append(a.resources, attempt.Resource)
	if len(a.beginIDs) > 0 {
		id := a.beginIDs[0]
		a.beginIDs = a.beginIDs[1:]
		return model.AuditEventID(id), nil
	}
	return model.AuditEventID(a.beginID), nil
}

func (a *institutionAuditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	*a.events = append(*a.events, "audit-fail")
	a.failCode = code
	return nil
}
