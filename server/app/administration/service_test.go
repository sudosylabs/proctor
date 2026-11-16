// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
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
	pluginSettings    store.PluginSettingsStore
}

func (c *catalogFake) Role() store.RoleStore                               { return c.roles }
func (c *catalogFake) RoleBinding() store.RoleBindingStore                 { return c.roleBindings }
func (c *catalogFake) User() store.UserStore                               { return c.users }
func (c *catalogFake) Institution() store.InstitutionStore                 { return c.institutions }
func (c *catalogFake) Audit() store.AuditStore                             { return c.audits }
func (c *catalogFake) AccessPolicy() store.AccessPolicyStore               { return c.accessPolicies }
func (c *catalogFake) RetentionPolicy() store.RetentionPolicyStore         { return c.retentionPolicies }
func (c *catalogFake) Retention() store.RetentionStore                     { return c.retention }
func (c *catalogFake) PluginSettings() store.PluginSettingsStore           { return c.pluginSettings }
func (c *catalogFake) EnvironmentProfile() store.EnvironmentProfileStore   { return nil }
func (c *catalogFake) AttemptEnvironment() store.AttemptEnvironmentStore   { return nil }
func (c *catalogFake) AttemptRuntimeLease() store.AttemptRuntimeLeaseStore { return nil }

// dependenciesOf rebuilds the Dependencies a Service was built from, with
// the auditor it was given, so a construction test can omit one collaborator
// at a time.
func dependenciesOf(s *Service, auditor *access.Auditor) Dependencies {
	return Dependencies{
		Store: &catalogFake{
			roles: s.roles, roleBindings: s.roleBindings, users: s.users, institutions: s.institutions, audits: s.audits,
			accessPolicies: s.accessPolicies, retentionPolicies: s.retentionPolicies, retention: s.retention,
		},
		Authorizer: s.authorizer, Auditor: auditor, Capabilities: s.capabilities, Mail: s.mail, Publisher: s.publisher,
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

// newCaller returns a signed-in caller and the invocation it makes. The real
// authorizer refuses a zero Principal.
func newCaller() (model.Principal, invocation.Invocation) {
	inv := selfAuditSessionInvocation()
	return inv.Principal(), inv
}

// accessWith builds the real authorizer and auditor the options describe and
// places what they do in events: "grants" for each read of the caller's Role
// Bindings and "decision:<action>:<status>" for a recorded decision. A nil
// events drops them.
func accessWith(t testing.TB, events *[]string, options accesstest.Options) *accesstest.Fixture {
	t.Helper()
	options.Observe = func(event string) {
		if events != nil {
			*events = append(*events, event)
		}
	}
	return accesstest.New(t, options)
}

// grantedAccess builds the real authorizer over the grants alone.
func grantedAccess(t testing.TB, events *[]string, grants ...accesstest.Grant) *accesstest.Fixture {
	t.Helper()
	return accessWith(t, events, accesstest.Options{Grants: grants})
}

// institutionGrant grants the actions Institution-wide.
func institutionGrant(userID model.UserID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions}
}

// unitGrant grants the actions in one Academic Unit and its descendants.
func unitGrant(userID model.UserID, unitID model.AcademicUnitID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}
}

// classGrant grants the actions in one Class.
func classGrant(userID model.UserID, classID model.ClassID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions, ScopeType: model.RoleScopeClass, ScopeID: classID.String()}
}

// strangerTo is the invocation of another User with the same credential
// and assurance as inv, who holds no grant.
func strangerTo(inv invocation.Invocation) invocation.Invocation {
	principal := inv.Principal()
	principal.UserID = model.NewUserID()
	return invocation.New(principal, inv.RequestMetadata())
}

// recordedDecision is one authorization decision the real auditor recorded.
type recordedDecision struct {
	action    model.Action
	resource  model.Resource
	scopeType model.RoleScopeType
	scopeID   string
	allowed   bool
}

// recordedDecisions returns the decisions the fixture recorded, in order.
func recordedDecisions(fixture *accesstest.Fixture) []recordedDecision {
	var decisions []recordedDecision
	for _, event := range fixture.Audits.Decisions() {
		decisions = append(decisions, recordedDecision{
			action: model.Action(event.Action), resource: event.Resource,
			scopeType: event.ScopeType, scopeID: event.ScopeID, allowed: event.Status == model.AuditStatusSuccess,
		})
	}
	return decisions
}

// decisions describes each recorded authorization decision as
// "<action> <resource type>:<resource id> <status>".
func decisions(fixture *accesstest.Fixture) []string {
	var described []string
	for _, event := range fixture.Audits.Decisions() {
		described = append(described, event.Action+" "+string(event.Resource.Type)+":"+event.Resource.ID+" "+string(event.Status))
	}
	return described
}

// classWorld is one live Class the real authorizer resolves: it sits in an
// Academic Unit and an Institution-owned Academic Period.
type classWorld struct {
	unitID model.AcademicUnitID
	class  *model.Class
	period *model.AcademicPeriod
}

func newClassWorld() classWorld {
	period := &model.AcademicPeriod{
		ID: model.NewAcademicPeriodID(), Owner: model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
		StartsAt: model.TimeFromMillis(0), EndsAt: model.TimeFromMillis(1_000_000),
	}
	return classWorld{
		unitID: model.NewAcademicUnitID(),
		class:  &model.Class{ID: model.NewClassID(), AcademicPeriodID: period.ID, DisplayName: "Class"},
		period: period,
	}
}

// options describe the world to accesstest with the grants.
func (w classWorld) options(grants ...accesstest.Grant) accesstest.Options {
	return accesstest.Options{
		Grants:          grants,
		Units:           []*model.AcademicUnit{{ID: w.unitID, Name: "unit"}},
		Classes:         worldClasses{world: w},
		AcademicPeriods: worldPeriods{world: w},
	}
}

type worldClasses struct {
	store.ClassStore
	world classWorld
}

func (c worldClasses) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	if id != c.world.class.ID {
		return nil, store.NewErrNotFound("class", id.String())
	}
	return c.world.class, nil
}

func (c worldClasses) GetAcademicUnitID(_ context.Context, id model.ClassID) (model.AcademicUnitID, error) {
	if id != c.world.class.ID {
		return "", store.NewErrNotFound("class", id.String())
	}
	return c.world.unitID, nil
}

type worldPeriods struct {
	store.AcademicPeriodStore
	world classWorld
}

func (p worldPeriods) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	if id != p.world.period.ID {
		return nil, store.NewErrNotFound("academic_period", id.String())
	}
	return p.world.period, nil
}

// recordedAttempt is one mutation attempt the real auditor recorded, with
// its Parameters decoded into the operation and value the service gave.
type recordedAttempt struct {
	*model.AuditEvent
	Operation string
	Value     map[string]any
}

// attemptsOf returns every mutation attempt the fixture's auditor recorded,
// in order, completed or not.
func attemptsOf(t testing.TB, fixture *accesstest.Fixture) []recordedAttempt {
	t.Helper()
	var attempts []recordedAttempt
	for _, event := range fixture.Audits.Attempts() {
		var parameters struct {
			Operation string         `json:"operation"`
			Value     map[string]any `json:"value"`
		}
		require.NoError(t, json.Unmarshal(event.Parameters, &parameters), "attempt parameters")
		attempts = append(attempts, recordedAttempt{AuditEvent: event, Operation: parameters.Operation, Value: parameters.Value})
	}
	return attempts
}

// onlyAttempt returns the one mutation attempt the fixture's auditor
// recorded.
func onlyAttempt(t testing.TB, fixture *accesstest.Fixture) recordedAttempt {
	t.Helper()
	attempts := attemptsOf(t, fixture)
	require.Len(t, attempts, 1, "recorded mutation attempts")
	return attempts[0]
}

// noCapabilities is a deployment with no providers, durable mail or MFA.
func noCapabilities() *identity.DeploymentCapabilities {
	return deploymentCapabilities(&capabilityProviders{}, false)
}

// deploymentCapabilities builds the production DeploymentCapabilities over
// the providers a test may change, with durable mail riding an enabled,
// healthy sender when durableMail is set.
func deploymentCapabilities(providers *capabilityProviders, durableMail bool) *identity.DeploymentCapabilities {
	return &identity.DeploymentCapabilities{
		Providers: providers, Mail: capabilityMailSender{enabled: durableMail}, Health: capabilityMailHealth{},
	}
}

// capabilityProviders is the live provider registry the capabilities read;
// a test changes what is configured by replacing its providers.
type capabilityProviders struct {
	mu        sync.Mutex
	providers []identity.AccessPolicyProviderCapability
}

func (p *capabilityProviders) set(providers ...identity.AccessPolicyProviderCapability) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providers = providers
}

func (p *capabilityProviders) Descriptors() []model.ExternalAuthenticationProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	descriptors := make([]model.ExternalAuthenticationProvider, 0, len(p.providers))
	for _, provider := range p.providers {
		descriptors = append(descriptors, provider.Descriptor)
	}
	return descriptors
}

func (p *capabilityProviders) Provider(id string) (identity.ExternalIdentityProvider, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, provider := range p.providers {
		if provider.Descriptor.ID == id {
			return capabilityProvider{descriptor: provider.Descriptor, autoProvision: provider.AutoProvision}, true
		}
	}
	return nil, false
}

// capabilityProvider is a configured provider; capabilities read only its
// descriptor and whether it may provision Users.
type capabilityProvider struct {
	identity.ExternalIdentityProvider
	descriptor    model.ExternalAuthenticationProvider
	autoProvision bool
}

func (p capabilityProvider) Descriptor() model.ExternalAuthenticationProvider { return p.descriptor }
func (p capabilityProvider) AutoProvision() bool                              { return p.autoProvision }

type capabilityMailSender struct {
	mail.Sender
	enabled bool
}

func (s capabilityMailSender) Enabled() bool { return s.enabled }

type capabilityMailHealth struct{}

func (capabilityMailHealth) Code() string { return mail.HealthHealthy }
