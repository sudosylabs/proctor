// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type accessPolicyStoreFake struct {
	store.AccessPolicyStore
	snapshot       *store.AccessPolicySnapshot
	blockers       []store.AccessPolicyBlocker
	preflightInput *store.AccessPolicyPreflight
	replaceInput   *store.AccessPolicyReplacement
	command        *store.CommandIdempotency
	result         *store.AccessPolicyReplacementResult
	preflightErr   error
	replaceErr     error
	// roleHolderMFAUsers are the Users HasRoleHolderMFARequirement reports as covered.
	roleHolderMFAUsers map[model.UserID]bool
	roleHolderMFAErr   error
}

func (s *accessPolicyStoreFake) Get(context.Context, int) (*store.AccessPolicySnapshot, error) {
	return s.snapshot, nil
}
func (s *accessPolicyStoreFake) Preflight(_ context.Context, input *store.AccessPolicyPreflight) (*store.AccessPolicyPreflightResult, error) {
	s.preflightInput = input
	if s.preflightErr != nil {
		return nil, s.preflightErr
	}
	return &store.AccessPolicyPreflightResult{Blockers: append([]store.AccessPolicyBlocker(nil), s.blockers...)}, nil
}
func (s *accessPolicyStoreFake) HasRoleHolderMFARequirement(_ context.Context, userID model.UserID, _ time.Time) (bool, error) {
	return s.roleHolderMFAUsers[userID], s.roleHolderMFAErr
}
func (s *accessPolicyStoreFake) Replace(_ context.Context, input *store.AccessPolicyReplacement, command *store.CommandIdempotency) (*store.AccessPolicyReplacementResult, error) {
	s.replaceInput, s.command = input, command
	return s.result, s.replaceErr
}

type accessPolicyCapabilitiesFake struct {
	snapshot identity.AccessPolicyCapabilitySnapshot
}

func (c *accessPolicyCapabilitiesFake) Snapshot() identity.AccessPolicyCapabilitySnapshot {
	return c.snapshot
}

type accessPolicyAuditFake struct {
	beginID  string
	beginErr error
	failErr  error
	attempt  access.Mutation
	failID   string
	failCode string
}

func (a *accessPolicyAuditFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	// This fake keeps no subject, so it refuses a Mutation that names one
	// instead of dropping it.
	// It stands in for access.MutationAuditAdapter, which records it.
	if !attempt.Subject.IsZero() {
		return "", apperror.New("audit.internal")
	}

	a.attempt = attempt
	return model.AuditEventID(a.beginID), a.beginErr
}

func (a *accessPolicyAuditFake) Fail(_ context.Context, id model.AuditEventID, code string) error {
	a.failID, a.failCode = id.String(), code
	return a.failErr
}

func accessPolicyFixture(t *testing.T) (*Service, *accessPolicyStoreFake, *accessPolicyCapabilitiesFake, *realtimetest.Publisher, *accessPolicyAuditFake, invocation.Invocation) {
	t.Helper()
	at := time.UnixMilli(1_000)
	institution, err := model.NewInstitution(model.NewInstitutionID(), "northbridge", "Northbridge", "", at)
	require.NoError(t, err)
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), at)
	persistence := &accessPolicyStoreFake{snapshot: &store.AccessPolicySnapshot{Policy: policy}}
	capabilities := &accessPolicyCapabilitiesFake{snapshot: identity.AccessPolicyCapabilitySnapshot{
		Providers:   []identity.AccessPolicyProviderCapability{{Descriptor: model.ExternalAuthenticationProvider{ID: "campus-oidc", DisplayName: "Campus", Type: "oidc"}, AutoProvision: true}},
		DurableMail: true,
	}}
	publisher := &realtimetest.Publisher{}
	audit := &accessPolicyAuditFake{beginID: model.NewID()}
	service, err := New(Dependencies{
		Store: &catalogFake{accessPolicies: persistence, institutions: &institutionStoreFake{institution: institution},
			retentionPolicies: &retentionPolicyStoreFake{}, retention: &retentionLifecycleFake{}},
		Authorizer: &resourceAuthorizerFake{}, Capabilities: capabilities, Auditor: audit, Publisher: publisher,
		CanonicalOrigin: "https://proctor.example.edu", SupportAddress: " Help@Example.edu ", PasswordMinimumLength: 12,
		RecentAuthenticationTTL: time.Minute, Now: func() time.Time { return at.Add(time.Minute) },
	})
	require.NoError(t, err)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: at, MFACompletedAt: model.OptionalTimeFrom(at.Add(30 * time.Second)), ClientType: model.SessionClientWeb}
	return service, persistence, capabilities, publisher, audit, invocation.New(principal, model.RequestMetadata{RequestID: "request-1"})
}

func TestAccessPolicyPreflightUsesCurrentDeploymentCapabilities(t *testing.T) {
	t.Parallel()
	service, persistence, _, _, _, invocation := accessPolicyFixture(t)
	settings := persistence.snapshot.Policy.Settings()
	settings.ProviderAdmissions["campus-oidc"] = model.ProviderAdmissionAutoProvision
	result, err := service.PreflightAccessPolicy(context.Background(), invocation, PreflightAccessPolicyCommand{ExpectedRevision: 1, Settings: settings})
	require.NoError(t, err)
	require.Empty(t, result.Blockers, "preflight = %#v input", result)
	require.NotNil(t, persistence.preflightInput, "preflight = %#v input", result)
	require.True(t, persistence.preflightInput.Capabilities.DurableMail, "preflight = %#v input", result)
	require.True(t, persistence.preflightInput.Capabilities.Providers["campus-oidc"].AutoProvision, "preflight = %#v input", result)
}

func TestAccessPolicyMutationRequiresInteractiveStrongRecentAuthentication(t *testing.T) {
	t.Parallel()
	service, persistence, _, _, _, call := accessPolicyFixture(t)
	settings := persistence.snapshot.Policy.Settings()
	cases := []struct {
		name      string
		principal model.Principal
		wantCode  string
	}{
		{name: "personal access token", principal: func() model.Principal {
			p := call.Principal()
			p.CredentialType = model.CredentialPersonalAccessToken
			p.SessionID = ""
			return p
		}(), wantCode: "authentication.token_invalid"},
		{name: "single factor", principal: func() model.Principal {
			p := call.Principal()
			p.AuthenticationStrength = model.AuthenticationSingleFactor
			p.MFACompletedAt = model.OptionalTime{}
			return p
		}(), wantCode: "authentication.strong_required"},
		{name: "stale", principal: func() model.Principal {
			p := call.Principal()
			p.AuthenticatedAt = time.UnixMilli(1)
			p.MFACompletedAt = model.OptionalTimeFrom(time.UnixMilli(1))
			return p
		}(), wantCode: "authentication.reauthentication_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invocation := invocation.New(tc.principal, model.RequestMetadata{})
			operations := []struct {
				name string
				run  func() error
			}{
				{name: "preflight", run: func() error {
					_, err := service.PreflightAccessPolicy(context.Background(), invocation,
						PreflightAccessPolicyCommand{ExpectedRevision: 1, Settings: settings})
					return err
				}},
				{name: "replace", run: func() error {
					_, err := service.ReplaceAccessPolicy(context.Background(), invocation,
						ReplaceAccessPolicyCommand{ExpectedRevision: 1, Settings: settings, IdempotencyKey: "assurance"})
					return err
				}},
			}
			for _, operation := range operations {
				t.Run(operation.name, func(t *testing.T) {
					err := operation.run()
					var appErr *apperror.Error
					require.ErrorAs(t, err, &appErr)
					require.Equal(t, tc.wantCode, appErr.Code())
				})
			}
		})
	}
}

func TestAccessPolicyReplacementIsIdempotentAndPublishesOnlyCommittedRevision(t *testing.T) {
	t.Parallel()
	service, persistence, _, publisher, audit, invocation := accessPolicyFixture(t)
	next := persistence.snapshot.Policy.Clone()
	transition, err := next.Replace(1, func() model.AccessPolicySettings {
		value := next.Settings()
		value.DesktopAuthorizationEnabled = false
		return value
	}(), invocation.Principal().UserID, time.UnixMilli(2_000))
	require.NoError(t, err)
	persistence.result = &store.AccessPolicyReplacementResult{Snapshot: &store.AccessPolicySnapshot{Policy: next, History: []*model.AccessPolicyTransition{transition}}, Changed: true}
	settings := persistence.snapshot.Policy.Settings()
	settings.DesktopAuthorizationEnabled = false
	result, err := service.ReplaceAccessPolicy(context.Background(), invocation, ReplaceAccessPolicyCommand{ExpectedRevision: 1, Settings: settings, IdempotencyKey: "policy-1"})
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Policy.Revision, "result=%#v command=%#v replacement=%#v effects", result, persistence.command, persistence.replaceInput)
	require.NotNil(t, persistence.command, "result=%#v command=%#v replacement=%#v effects", result, persistence.command, persistence.replaceInput)
	require.Equal(t, "access_policy.replace", persistence.command.Operation, "result=%#v command=%#v replacement=%#v effects", result, persistence.command, persistence.replaceInput)
	require.NotNil(t, persistence.replaceInput, "result=%#v command=%#v replacement=%#v effects", result, persistence.command, persistence.replaceInput)
	require.Equal(t, model.AuditEventID(audit.beginID), persistence.replaceInput.AuditEventID, "result=%#v command=%#v replacement=%#v effects", result, persistence.command, persistence.replaceInput)
	require.Equal(t, "replace", audit.attempt.Operation, "result=%#v command=%#v replacement=%#v effects", result, persistence.command, persistence.replaceInput)
	require.Equal(t, model.RoleScopeInstitution, audit.attempt.ScopeType, "result=%#v command=%#v replacement=%#v effects", result, persistence.command, persistence.replaceInput)
	policyChanged, err := realtime.NewAccessPolicyChangedEvent(service.institutions.(*institutionStoreFake).institution.ID, 2)
	require.NoError(t, err)
	// Contract: a replacement that revoked and shortened no Session publishes
	// only the refetch signal of the committed revision, and reports nothing.
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: policyChanged}}, publisher.Calls())
}

func TestAccessPolicyReplacementResolvesExactReplayBeforeRevisionPreflight(t *testing.T) {
	t.Parallel()
	service, persistence, _, publisher, audit, invocation := accessPolicyFixture(t)
	replayed := persistence.snapshot.Policy.Clone()
	replayed.Revision = 2
	replayed.UpdatedAt = replayed.UpdatedAt.Add(time.Second)
	persistence.preflightErr = &store.ErrAccessPolicyRevisionConflict{CurrentRevision: 2}
	persistence.result = &store.AccessPolicyReplacementResult{
		Snapshot: &store.AccessPolicySnapshot{Policy: replayed}, Changed: true, Replayed: true,
	}
	settings := persistence.snapshot.Policy.Settings()
	settings.DesktopAuthorizationEnabled = false
	result, err := service.ReplaceAccessPolicy(context.Background(), invocation, ReplaceAccessPolicyCommand{
		ExpectedRevision: 1, Settings: settings, IdempotencyKey: "lost-response",
	})
	require.NoError(t, err, "replay result = %#v", result)
	require.NotNil(t, result.Policy, "replay result = %#v", result)
	require.Equal(t, int64(2), result.Policy.Revision, "replay result = %#v", result)
	require.Nil(t, persistence.preflightInput, "replacement performed a revision-sensitive preflight before replay resolution")
	require.Empty(t, publisher.Calls())
	require.Equal(t, "replace", audit.attempt.Operation, "replay audit = %#v replacement = %#v", audit.attempt, persistence.replaceInput)
	require.Equal(t, model.AuditEventID(audit.beginID), persistence.replaceInput.AuditEventID, "replay audit = %#v replacement = %#v", audit.attempt, persistence.replaceInput)
}

func TestAccessPolicyReplacementFingerprintIncludesSessionRevocationChoice(t *testing.T) {
	t.Parallel()
	service, persistence, _, _, _, invocation := accessPolicyFixture(t)
	persistence.result = &store.AccessPolicyReplacementResult{Snapshot: persistence.snapshot}
	settings := persistence.snapshot.Policy.Settings()
	settings.DesktopAuthorizationEnabled = false
	_, err := service.ReplaceAccessPolicy(context.Background(), invocation, ReplaceAccessPolicyCommand{
		ExpectedRevision: 1, Settings: settings, RevokeExistingSessions: true, IdempotencyKey: "policy-revoke",
	})
	require.NoError(t, err)
	require.NotNil(t, persistence.replaceInput)
	require.True(t, persistence.replaceInput.Preflight.RevokeExistingSessions)
	withRevocation := persistence.command.Fingerprint
	persistence.result = &store.AccessPolicyReplacementResult{Snapshot: persistence.snapshot}
	_, err = service.ReplaceAccessPolicy(context.Background(), invocation, ReplaceAccessPolicyCommand{
		ExpectedRevision: 1, Settings: settings, RevokeExistingSessions: false, IdempotencyKey: "policy-revoke",
	})
	require.NoError(t, err)
	require.NotEqual(t, withRevocation, persistence.command.Fingerprint, "session revocation choice was omitted from the idempotency fingerprint")
}

func TestAccessPolicyReplacementMapsConflictingIdempotencyReuse(t *testing.T) {
	t.Parallel()
	service, persistence, _, _, _, invocation := accessPolicyFixture(t)
	persistence.replaceErr = &store.ErrIdempotencyConflict{}
	settings := persistence.snapshot.Policy.Settings()
	settings.DesktopAuthorizationEnabled = false
	_, err := service.ReplaceAccessPolicy(context.Background(), invocation, ReplaceAccessPolicyCommand{
		ExpectedRevision: 1, Settings: settings, IdempotencyKey: "reused-key",
	})
	require.True(t, apperror.Is(err, "idempotency.conflict"), "error = %v", err)
	require.Nil(t, persistence.preflightInput, "conflicting reuse was preceded by a revision-sensitive preflight")
}

func TestAccessPolicyReplacementCompletesRevisionBlockerAndStoreFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		storeErr error
		wantCode string
	}{
		{name: "revision", storeErr: &store.ErrAccessPolicyRevisionConflict{CurrentRevision: 2}, wantCode: "access_policy.revision_conflict"},
		{name: "blocker", storeErr: &store.ErrAccessPolicyBlocked{Blockers: []store.AccessPolicyBlocker{{Code: store.AccessPolicyBlockerLastAdministratorPath}}}, wantCode: "access_policy.blocked"},
		{name: "store", storeErr: errors.New("database unavailable"), wantCode: "access_policy.unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, persistence, _, publisher, audit, invocation := accessPolicyFixture(t)
			persistence.replaceErr = tc.storeErr
			settings := persistence.snapshot.Policy.Settings()
			settings.DesktopAuthorizationEnabled = false
			_, err := service.ReplaceAccessPolicy(context.Background(), invocation, ReplaceAccessPolicyCommand{
				ExpectedRevision: 1, Settings: settings, IdempotencyKey: "failed-" + tc.name,
			})
			require.True(t, apperror.Is(err, tc.wantCode), "error = %v, want %s", err, tc.wantCode)
			require.Equal(t, "replace", audit.attempt.Operation, "audit = %#v fail=%q", audit.attempt, audit.failID)
			require.Equal(t, audit.beginID, audit.failID, "audit = %#v fail=%q", audit.attempt, audit.failID)
			require.Equal(t, tc.wantCode, audit.failCode, "audit = %#v fail=%q", audit.attempt, audit.failID)
			require.Empty(t, publisher.Calls())
		})
	}
}

func TestAccessPolicyReadFailsSafelyForMissingSnapshot(t *testing.T) {
	t.Parallel()
	service, persistence, _, _, _, invocation := accessPolicyFixture(t)
	for _, snapshot := range []*store.AccessPolicySnapshot{nil, {}} {
		persistence.snapshot = snapshot
		_, err := service.GetAccessPolicy(context.Background(), invocation)
		require.True(t, apperror.Is(err, "access_policy.unavailable"), "snapshot %#v error = %v", snapshot, err)
	}
}

func TestPublicAccessDiscoveryFiltersUnavailableProvidersAndRestoresByStableID(t *testing.T) {
	t.Parallel()
	service, persistence, capabilities, _, _, _ := accessPolicyFixture(t)
	policy := persistence.snapshot.Policy.Clone()
	policy.ProviderAdmissions["campus-oidc"] = model.ProviderAdmissionLinkedOnly
	persistence.snapshot.Policy = policy

	capabilities.snapshot.Providers = nil
	discovery, err := service.DiscoverAccess(context.Background(), invocation.Invocation{})
	require.NoError(t, err)
	require.Equal(t, "https://proctor.example.edu", discovery.CanonicalOrigin)
	require.Empty(t, discovery.Providers)
	require.True(t, discovery.Initialized)
	capabilities.snapshot.Providers = []identity.AccessPolicyProviderCapability{{Descriptor: model.ExternalAuthenticationProvider{ID: "campus-oidc", DisplayName: "Campus", Type: "oidc"}}}
	discovery, err = service.DiscoverAccess(context.Background(), invocation.Invocation{})
	require.NoError(t, err, "restored-provider discovery = %#v", discovery)
	require.Len(t, discovery.Providers, 1, "restored-provider discovery = %#v", discovery)
	require.Equal(t, "campus-oidc", discovery.Providers[0].ID, "restored-provider discovery = %#v", discovery)
}

func TestPublicAccessDiscoveryPublishesOperatorPresentationBeforeInitialization(t *testing.T) {
	t.Parallel()
	service, _, _, _, _, _ := accessPolicyFixture(t)
	discovery, err := service.DiscoverAccess(context.Background(), invocation.Invocation{})
	require.NoError(t, err)
	require.Equal(t, "help@example.edu", discovery.SupportAddress)
	require.Equal(t, 12, discovery.PasswordMinimumLength)

	service.institutions = &institutionStoreFake{err: store.NewErrNotFound("institution", "singleton")}
	discovery, err = service.DiscoverAccess(context.Background(), invocation.Invocation{})
	require.NoError(t, err)
	require.False(t, discovery.Initialized)
	require.Equal(t, "help@example.edu", discovery.SupportAddress)
	require.Equal(t, 12, discovery.PasswordMinimumLength)
}

func TestAccessPolicyServiceRejectsInvalidPublicPresentation(t *testing.T) {
	t.Parallel()
	service, _, _, _, _, _ := accessPolicyFixture(t)
	for name, mutate := range map[string]func(*Dependencies){
		"support address":         func(deps *Dependencies) { deps.SupportAddress = "not an address" },
		"password minimum length": func(deps *Dependencies) { deps.PasswordMinimumLength = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			deps := dependenciesOf(service)
			mutate(&deps)
			_, err := New(deps)
			require.Error(t, err)
		})
	}
	deps := dependenciesOf(service)
	deps.SupportAddress = ""
	empty, err := New(deps)
	require.NoError(t, err)
	discovery, err := empty.DiscoverAccess(context.Background(), invocation.Invocation{})
	require.NoError(t, err)
	require.Empty(t, discovery.SupportAddress)
}
