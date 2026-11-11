// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExternalAuthenticationRetainsExactPersistenceContracts(t *testing.T) {
	t.Parallel()

	root := reflect.TypeOf((*store.Store)(nil)).Elem()
	typeOfService := reflect.TypeOf(Service{})
	for index := 0; index < typeOfService.NumField(); index++ {
		require.NotEqual(t, root, typeOfService.Field(index).Type, "Service retains root Store in")
	}
}

func TestExternalAuthenticationBeginUsesControlledCredentials(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	stateToken := "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE"
	bindingToken := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUY"
	generated := []string{stateToken, bindingToken}
	next := 0
	databaseAt := at.Add(-2 * time.Hour)
	states := &externalLoginStateStoreFake{storeNow: databaseAt}
	attempts, err := NewAttemptAccounting(AttemptAccountingDependencies{Cache: newAuthenticationCacheFake()})
	require.NoError(t, err)
	service := &Service{externalProviders: externalProviderSourceFake{provider: externalProviderFake{}}, externalLoginStates: states, accessPolicy: allowAllAuthenticationAccessPolicy(), attempts: attempts, externalAuthenticationPolicy: ExternalAuthenticationPolicy{
		PublicURL: "https://proctor.example.test", LoginStateTTL: 10 * time.Minute,
		LoginRateLimit: LoginRateLimitPolicy{Window: time.Minute, MaximumSourceAttempts: 10},
	}, newCredential: func() string { value := generated[next]; next++; return value }, now: func() time.Time { return at }}

	result, err := service.beginExternalLogin(
		context.Background(), "campus", "/after", model.SessionClientWeb,
		"browser", "Browser", "127.0.0.1",
	)
	require.NoError(t, err)
	require.Equal(t, bindingToken, result.Binding, "result=%#v saved", result)
	require.NotNil(t, states.saved, "result=%#v saved", result)
	require.Equal(t, model.HashToken(stateToken), states.saved.StateHash, "result=%#v saved", result)
	require.Equal(t, model.HashToken(bindingToken), states.saved.BindingHash, "result=%#v saved", result)
	require.True(t, states.saved.CreatedAt.IsZero(), "result=%#v saved", result)
	require.True(t, states.saved.ExpiresAt.IsZero(), "result=%#v saved", result)
	require.Equal(t, 10*time.Minute, states.saveLifetime, "result=%#v saved", result)
	require.Equal(t, databaseAt.Add(10*time.Minute).UnixMilli(), result.ExpiresAt, "result=%#v saved", result)
}

func TestInvitationRequiredBeginHashesClaimIntoPurposeBoundState(t *testing.T) {
	t.Parallel()
	claim := model.NewCredentialToken()
	states := &externalLoginStateStoreFake{}
	service := externalAuthenticationBeginService(t, externalProviderSourceSet{
		provider: &recordingExternalProvider{}, ids: map[string]bool{"campus": true},
	}, newAuthenticationCacheFake(), 10)
	service.externalLoginStates = states
	service.accessPolicy = authenticationAccessPolicyFake{providers: map[string]bool{"campus": true},
		providerModes: map[string]model.ProviderAdmissionMode{"campus": model.ProviderAdmissionInvitationRequired}}

	result, err := service.beginWithInvitationClaim(context.Background(), "campus", "/join", model.SessionClientWeb,
		"", "", "127.0.0.1", claim)
	require.NoError(t, err)
	require.NotNil(t, result, "result")
	require.NotNil(t, states.saved, "result")
	require.Equal(t, model.ExternalAuthenticationPurposeInvitationAdmission, states.saved.Purpose, "result")
	require.Equal(t, model.HashInvitationClaim(claim), states.admissionClaimHash, "result")
	require.NotContains(t, states.saved.StateHash, claim, "raw Invitation claim escaped into durable/provider state")
	require.NotContains(t, result.RedirectURL, claim, "raw Invitation claim escaped into durable/provider state")
}

func TestInvitationRequiredBeginRejectsWhenInvitationAdmissionIsDisabled(t *testing.T) {
	t.Parallel()

	provider := &recordingExternalProvider{}
	service := externalAuthenticationBeginService(t, externalProviderSourceSet{
		provider: provider, ids: map[string]bool{"campus": true},
	}, newAuthenticationCacheFake(), 10)
	service.accessPolicy = authenticationAccessPolicyFake{
		providers:                   map[string]bool{"campus": true},
		providerModes:               map[string]model.ProviderAdmissionMode{"campus": model.ProviderAdmissionInvitationRequired},
		invitationAdmissionDisabled: true,
	}

	result, err := service.beginWithInvitationClaim(
		context.Background(), "campus", "/join", model.SessionClientWeb,
		"", "", "192.0.2.9", model.NewCredentialToken(),
	)
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.external.account_not_linked"), "result=%#v err=%v", result, err)
	require.Equal(t, 0, provider.beginCalls, "disabled invitation admission began")
}

func TestExternalAuthenticationOrdinaryLoginRejectsDesktopBeforeProviderOrPersistence(t *testing.T) {
	t.Parallel()

	provider := &recordingExternalProvider{}
	states := &externalLoginStateStoreFake{}
	service := externalAuthenticationBeginService(t, externalProviderSourceSet{
		provider: provider, ids: map[string]bool{"campus": true},
	}, newAuthenticationCacheFake(), 10)
	service.externalLoginStates = states

	result, err := service.beginExternalLogin(context.Background(), "campus", "/", model.SessionClientDesktop, "desktop-1", "Desktop", "127.0.0.1")
	require.Nil(t, result, "desktop ordinary login result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.external.request.invalid"), "desktop ordinary login result=%#v err=%v", result, err)
	require.Equal(t, 0, provider.beginCalls, "desktop ordinary login reached provider/persistence: provider")
	require.Nil(t, states.saved, "desktop ordinary login reached provider/persistence: provider")
}

func TestExternalAuthenticationBeginHidesProviderDisabledByCurrentPolicy(t *testing.T) {
	t.Parallel()

	provider := &recordingExternalProvider{}
	service := externalAuthenticationBeginService(t, externalProviderSourceSet{
		provider: provider, ids: map[string]bool{"campus": true},
	}, newAuthenticationCacheFake(), 10)
	service.accessPolicy = authenticationAccessPolicyFake{providers: map[string]bool{}}
	result, err := service.beginExternalLogin(context.Background(), "campus", "/", model.SessionClientWeb, "", "", "192.0.2.9")
	require.Nil(t, result, "result=%#v err=%v", result, err)
	require.True(t, apperror.Is(err, "authentication.external.provider_not_found"), "result=%#v err=%v", result, err)
	require.Equal(t, 0, provider.beginCalls, "disabled provider began")
}

func TestExternalAuthenticationProviderListIncludesOnlyCurrentPolicySelections(t *testing.T) {
	t.Parallel()

	configured := []model.ExternalAuthenticationProvider{
		{ID: "campus-a", DisplayName: "Campus A", Type: "oidc"},
		{ID: "campus-b", DisplayName: "Campus B", Type: "cas"},
	}
	service := &Service{externalProviders: externalProviderSourceSet{descriptors: configured}, accessPolicy: authenticationAccessPolicyFake{providers: map[string]bool{"campus-b": true}}}
	providers, err := service.ExternalAuthenticationProviders(context.Background(), invocation.Invocation{})
	require.NoError(t, err)
	require.Equal(t, configured[1:], providers, "available providers")
}

func TestExternalAuthenticationRequiresFocusedDependencies(t *testing.T) {
	t.Parallel()

	valid := externalAuthenticationConstructorArgs{
		registry:    externalProviderSourceFake{provider: externalProviderFake{}},
		loginStates: &externalLoginStateStoreFake{}, institutions: externalInstitutionStoreFake{},
		identities: externalIdentityStoreFake{}, sessions: &externalSessionStoreFake{},
		attempts:    newExternalAuthenticationAttempts(t, newAuthenticationCacheFake()),
		invalidator: externalInvalidatorFake{}, audit: &securityAuditorFake{},
		diagnostics: newDiscardLogger(t), newCredential: model.NewCredentialToken,
		acceptor: &externalInvitationAcceptorFake{},
		now:      time.Now,
	}
	_, err := valid.build()
	require.NoError(t, err)
	tests := []struct {
		name  string
		clear func(*externalAuthenticationConstructorArgs)
	}{
		{"login states", func(a *externalAuthenticationConstructorArgs) { a.loginStates = nil }},
		{"institutions", func(a *externalAuthenticationConstructorArgs) { a.institutions = nil }},
		{"identities", func(a *externalAuthenticationConstructorArgs) { a.identities = nil }},
		{"sessions", func(a *externalAuthenticationConstructorArgs) { a.sessions = nil }},
		{"attempt accounting", func(a *externalAuthenticationConstructorArgs) { a.attempts = nil }},
		{"Invitation acceptor", func(a *externalAuthenticationConstructorArgs) { a.acceptor = nil }},
		{"generator", func(a *externalAuthenticationConstructorArgs) { a.newCredential = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.clear(&candidate)
			_, err := candidate.build()
			require.Error(t, err, "missing %s accepted", test.name)
		})
	}
}

type externalAuthenticationConstructorArgs struct {
	registry      ExternalProviderSource
	loginStates   store.ExternalLoginStateStore
	institutions  store.InstitutionStore
	identities    store.ExternalIdentityStore
	sessions      store.SessionStore
	attempts      *AttemptAccounting
	invalidator   realtime.AuthenticationInvalidator
	audit         securityAuditor
	diagnostics   *logging.Logger
	acceptor      InvitationAcceptor
	newCredential func() string
	now           func() time.Time
}

// build sets the arguments on a Service and checks it as New does.
func (a externalAuthenticationConstructorArgs) build() (*Service, error) {
	events := []string{}
	service := &Service{externalProviders: a.registry, externalLoginStates: a.loginStates, institutions: a.institutions,
		externalIdentities: a.identities, sessions: a.sessions, accessPolicy: allowAllAuthenticationAccessPolicy(), attempts: a.attempts,
		invalidator: a.invalidator, securityAudit: a.audit,
		auditor:      &mutationAttemptAuditorFake{events: &events, beginID: model.NewAuditEventID().String()},
		capabilities: &accessPolicyCapabilitiesFake{}, invitations: a.acceptor, recentAuthenticationTTL: 15 * time.Minute,
		diagnostics: a.diagnostics, newCredential: a.newCredential, now: a.now}
	return service, service.requireExternalAuthentication()
}

func TestExternalAuthenticationInitiationUsesProviderScopedSourceAccounting(t *testing.T) {
	t.Parallel()

	cache := newAuthenticationCacheFake()
	provider := &recordingExternalProvider{}
	service := externalAuthenticationBeginService(
		t,
		externalProviderSourceSet{provider: provider, ids: map[string]bool{
			"campus-a": true,
			"campus-b": true,
		}},
		cache,
		1,
	)

	_, err := service.beginExternalLogin(
		context.Background(), "campus-a", "/", model.SessionClientWeb,
		"", "", "192.0.2.10:4000",
	)
	require.NoError(t, err)
	_, err = service.beginExternalLogin(
		context.Background(), " CAMPUS-A ", "/", model.SessionClientWeb,
		"", "", "192.0.2.10:5000",
	)
	require.True(t, apperror.Is(err, "authentication.rate_limited"), "normalized-source threshold error = %v", err)
	_, err = service.beginExternalLogin(
		context.Background(), "campus-b", "/", model.SessionClientWeb,
		"", "", "192.0.2.10:6000",
	)
	require.NoError(t, err)

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Len(t, cache.counters, 2, "counter keys")
	for key := range cache.counters {
		require.True(t, strings.HasPrefix(key, "authentication/attempts/external-authentication/source/"), "unexpected counter key %q", key)
		require.NotContains(t, key, "campus")
		require.NotContains(t, key, "192.0.2.10")
	}
}

func TestExternalAuthenticationInitiationAllowsMaximumThenLimitsNext(t *testing.T) {
	t.Parallel()

	provider := &recordingExternalProvider{}
	service := externalAuthenticationBeginService(
		t,
		externalProviderSourceSet{provider: provider, ids: map[string]bool{"campus": true}},
		newAuthenticationCacheFake(),
		2,
	)
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := service.beginExternalLogin(
			context.Background(), "campus", "/", model.SessionClientWeb,
			"", "", "198.51.100.7",
		)
		require.NoError(t, err, "attempt %d failed", attempt)
	}
	_, err := service.beginExternalLogin(
		context.Background(), "campus", "/", model.SessionClientWeb,
		"", "", "198.51.100.7",
	)
	require.True(t, apperror.Is(err, "authentication.rate_limited"), "attempt beyond maximum error = %v", err)
	require.Equal(t, 2, provider.beginCalls, "provider Begin calls")
}

func TestExternalAuthenticationInitiationPreservesValidationAndFailureOrdering(t *testing.T) {
	t.Parallel()

	cache := &externalAuthenticationAttemptFaultCache{
		authenticationCacheFake: newAuthenticationCacheFake(),
		err:                     errors.New("counter unavailable"),
	}
	provider := &recordingExternalProvider{}
	service := externalAuthenticationBeginService(
		t,
		externalProviderSourceSet{provider: provider, ids: map[string]bool{"campus": true}},
		cache,
		2,
	)

	_, err := service.beginExternalLogin(
		context.Background(), "missing", "/", model.SessionClientWeb,
		"", "", "203.0.113.8",
	)
	require.True(t, apperror.Is(err, "authentication.external.provider_not_found"), "missing-provider error = %v", err)
	require.Equal(t, 0, cache.addCalls, "provider validation occurred after")
	_, err = service.beginExternalLogin(
		context.Background(), "campus", "https://unsafe.example", model.SessionClientWeb,
		"", "", "203.0.113.8",
	)
	require.True(t, apperror.Is(err, "authentication.external.request.invalid"), "invalid-request error = %v", err)
	require.Equal(t, 0, cache.addCalls, "request validation occurred after")
	_, err = service.beginExternalLogin(
		context.Background(), "campus", "/", model.SessionClientWeb,
		"", "", "203.0.113.8",
	)
	require.True(t, apperror.Is(err, "authentication.rate_limit_unavailable"), "counter failure error = %v", err)
	require.Equal(t, 0, provider.beginCalls, "provider began before accounting succeeded")
}

func externalAuthenticationBeginService(
	t *testing.T,
	registry ExternalProviderSource,
	cache AttemptCache,
	maximum int,
) *Service {
	t.Helper()
	attempts := newExternalAuthenticationAttempts(t, cache)
	return &Service{externalProviders: registry, externalLoginStates: &externalLoginStateStoreFake{}, accessPolicy: allowAllAuthenticationAccessPolicy(), attempts: attempts, externalAuthenticationPolicy: ExternalAuthenticationPolicy{
		PublicURL:     "https://proctor.example.test",
		LoginStateTTL: 10 * time.Minute,
		LoginRateLimit: LoginRateLimitPolicy{
			Window: time.Minute, MaximumSourceAttempts: maximum,
		},
	}, newCredential: model.NewCredentialToken, now: time.Now}
}

func newExternalAuthenticationAttempts(
	t *testing.T,
	cache AttemptCache,
) *AttemptAccounting {
	t.Helper()
	attempts, err := NewAttemptAccounting(AttemptAccountingDependencies{Cache: cache})
	require.NoError(t, err)
	return attempts
}

type externalProviderSourceSet struct {
	provider    ExternalIdentityProvider
	ids         map[string]bool
	descriptors []model.ExternalAuthenticationProvider
}

func (s externalProviderSourceSet) Descriptors() []model.ExternalAuthenticationProvider {
	return append([]model.ExternalAuthenticationProvider(nil), s.descriptors...)
}

func (s externalProviderSourceSet) Provider(id string) (ExternalIdentityProvider, bool) {
	return s.provider, s.ids[id]
}

type recordingExternalProvider struct{ beginCalls int }

func (*recordingExternalProvider) Descriptor() model.ExternalAuthenticationProvider {
	return model.ExternalAuthenticationProvider{ID: "campus", Type: "oidc"}
}

func (*recordingExternalProvider) AutoProvision() bool { return false }

func (p *recordingExternalProvider) Begin(
	context.Context,
	ExternalProviderBeginRequest,
) (*ExternalProviderBeginResponse, error) {
	p.beginCalls++
	return &ExternalProviderBeginResponse{RedirectURL: "https://identity.example.test/login"}, nil
}

func (*recordingExternalProvider) State(model.ExternalAuthenticationCallback) (string, error) {
	return "", nil
}

func (*recordingExternalProvider) Complete(
	context.Context,
	ExternalProviderCompleteRequest,
) (*model.ExternalAuthenticationAssertion, error) {
	return nil, nil
}

type externalAuthenticationAttemptFaultCache struct {
	*authenticationCacheFake
	err      error
	addCalls int
}

func (c *externalAuthenticationAttemptFaultCache) Add(
	context.Context,
	string,
	int64,
	time.Duration,
) (int64, error) {
	c.addCalls++
	return 0, c.err
}

type externalProviderSourceFake struct{ provider ExternalIdentityProvider }

func (s externalProviderSourceFake) Descriptors() []model.ExternalAuthenticationProvider { return nil }

func (s externalProviderSourceFake) Provider(id string) (ExternalIdentityProvider, bool) {
	return s.provider, id == "campus"
}

type externalProviderFake struct{}

func (externalProviderFake) Descriptor() model.ExternalAuthenticationProvider {
	return model.ExternalAuthenticationProvider{ID: "campus", Type: "oidc"}
}

func (externalProviderFake) AutoProvision() bool { return false }

func (externalProviderFake) Begin(context.Context, ExternalProviderBeginRequest) (*ExternalProviderBeginResponse, error) {
	return &ExternalProviderBeginResponse{RedirectURL: "https://identity.example.test/login"}, nil
}

func (externalProviderFake) State(model.ExternalAuthenticationCallback) (string, error) {
	return "", nil
}

func (externalProviderFake) Complete(context.Context, ExternalProviderCompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	return nil, nil
}

type externalLoginStateStoreFake struct {
	store.ExternalLoginStateStore
	saved              *model.ExternalLoginState
	saveLifetime       time.Duration
	storeNow           time.Time
	get                *model.ExternalLoginState
	consumeCalls       int
	consumeResult      *model.ExternalLoginState
	admissionClaimHash string
	admissionErr       error
}

type externalInstitutionStoreFake struct{ store.InstitutionStore }

type externalIdentityStoreFake struct{ store.ExternalIdentityStore }

// externalSessionStoreFake stands in for the Session store external sign-in
// issues Sessions through. It records each Session creation it received and
// saves the Session with the credentials it was given, unless saveErr is set.
type externalSessionStoreFake struct {
	store.SessionStore
	saves   []*store.SessionCreation
	saved   *model.Session
	saveErr error
}

func (s *externalSessionStoreFake) Save(_ context.Context, input *store.SessionCreation) (*model.Session, []*model.SessionCredential, error) {
	s.saves = append(s.saves, input)
	if s.saveErr != nil {
		return nil, nil, s.saveErr
	}
	saved := *input.Session
	saved.ID = model.NewSessionID()
	s.saved = &saved
	return s.saved, input.Credentials, nil
}

// externalRecoveryStateStoreFake stands in for the MFA store external
// sign-in reads a User's two-step verification recovery state from.
type externalRecoveryStateStoreFake struct {
	store.MFAStore
	state *model.UserMFARecovery
	reads int
}

func (s *externalRecoveryStateStoreFake) GetRecoveryState(_ context.Context, userID model.UserID) (*model.UserMFARecovery, error) {
	s.reads++
	if s.state != nil {
		return s.state, nil
	}
	return &model.UserMFARecovery{UserID: userID}, nil
}

type providerConnectionIdentityStoreFake struct {
	store.ExternalIdentityStore
	linked *store.ExternalIdentityLink
}

type externalIdentityResolutionStoreFake struct {
	store.ExternalIdentityStore
	resolution *store.ExternalIdentityResolutionRequest
}

type externalInvitationAcceptorFake struct {
	state      *model.ExternalLoginState
	assertion  *model.ExternalAuthenticationAssertion
	capability store.AccessDeploymentCapabilities
	metadata   model.RequestMetadata
	method     string
	result     *store.ExternalIdentityInvitationAcceptanceResult
	err        error
}

func (s *externalInvitationAcceptorFake) AcceptExternalIdentity(_ context.Context, state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion, capability store.AccessDeploymentCapabilities,
	metadata model.RequestMetadata, method string,
) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	s.state, s.assertion, s.capability, s.metadata, s.method = state, assertion, capability, metadata, method
	return s.result, s.err
}

func (s *externalIdentityResolutionStoreFake) ResolveOrProvision(_ context.Context, input *store.ExternalIdentityResolutionRequest) (*store.ExternalIdentityResolution, error) {
	s.resolution = input
	return nil, store.NewErrNotFound("external_identity", input.Identity.Provider)
}

func (s *providerConnectionIdentityStoreFake) Link(_ context.Context, input *store.ExternalIdentityLink) (*store.AuthenticationMethodMutationResult, error) {
	s.linked = input
	return &store.AuthenticationMethodMutationResult{Identity: input.Identity}, nil
}

type providerConnectionInstitutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
}

type admissionModeProvider struct {
	providerConnectionProvider
	autoProvision bool
}

func (p admissionModeProvider) AutoProvision() bool { return p.autoProvision }

func (s providerConnectionInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

type providerConnectionProvider struct {
	state       string
	assertion   *model.ExternalAuthenticationAssertion
	completeErr error
}

func (p providerConnectionProvider) Descriptor() model.ExternalAuthenticationProvider {
	return model.ExternalAuthenticationProvider{ID: "campus", Type: "oidc"}
}

func (providerConnectionProvider) AutoProvision() bool { return false }

func (providerConnectionProvider) Begin(context.Context, ExternalProviderBeginRequest) (*ExternalProviderBeginResponse, error) {
	return nil, errors.New("unused")
}

func (p providerConnectionProvider) State(model.ExternalAuthenticationCallback) (string, error) {
	return p.state, nil
}

func (p providerConnectionProvider) Complete(context.Context, ExternalProviderCompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	return p.assertion, p.completeErr
}

type externalInvalidatorFake struct{}

func (externalInvalidatorFake) InvalidateAccessCredentials(context.Context, []string) {}

func (externalInvalidatorFake) InvalidateSessionActivity(context.Context, []model.SessionID) {}

func (s *externalLoginStateStoreFake) Save(_ context.Context, input *store.ExternalLoginStateCreation) (*model.ExternalLoginState, error) {
	state := input.State
	lifetime := input.Lifetime
	s.saved = state
	s.saveLifetime = lifetime
	result := *state
	at := s.storeNow
	if at.IsZero() {
		at = model.NowUTC()
	}
	result.ExpiresAt = model.TimeUTC(at).Add(lifetime)
	result.PrepareCreate(model.NewExternalLoginStateID(), at)
	return &result, nil
}

func (s *externalLoginStateStoreFake) SaveInvitationAdmission(_ context.Context, input *store.ExternalLoginStateInvitationAdmission) (*model.ExternalLoginState, error) {
	state := input.State
	lifetime := input.Lifetime
	claimHash := input.ClaimHash
	s.admissionClaimHash = claimHash
	if s.admissionErr != nil {
		return nil, s.admissionErr
	}
	state = cloneExternalLoginStateForTest(state)
	state.InvitationID = model.NewInvitationID()
	return s.Save(context.Background(), &store.ExternalLoginStateCreation{State: state, Lifetime: lifetime})
}

func cloneExternalLoginStateForTest(state *model.ExternalLoginState) *model.ExternalLoginState {
	result := *state
	return &result
}

func (s *externalLoginStateStoreFake) GetByStateHash(context.Context, string) (*model.ExternalLoginState, error) {
	if s.get == nil {
		return nil, store.NewErrNotFound("external_login_state", "")
	}
	return s.get, nil
}

func (s *externalLoginStateStoreFake) Consume(ctx context.Context, input *store.ExternalLoginStateConsumption) (*model.ExternalLoginState, error) {
	s.consumeCalls++
	if s.consumeResult != nil {
		result := *s.consumeResult
		if !result.ConsumedAt.Valid {
			at := s.storeNow
			if at.IsZero() {
				at = model.NowUTC()
			}
			result.ConsumedAt = model.OptionalTimeFrom(at)
			result.UpdatedAt = model.TimeUTC(at)
		}
		return &result, nil
	}
	return nil, errors.New("desktop state must not be consumed")
}

type desktopCallbackExternalProvider struct{ state string }

func (desktopCallbackExternalProvider) Descriptor() model.ExternalAuthenticationProvider {
	return model.ExternalAuthenticationProvider{ID: "campus", Type: "oidc"}
}

func (desktopCallbackExternalProvider) AutoProvision() bool { return false }

func (desktopCallbackExternalProvider) Begin(context.Context, ExternalProviderBeginRequest) (*ExternalProviderBeginResponse, error) {
	return nil, errors.New("not used")
}

func (p desktopCallbackExternalProvider) State(model.ExternalAuthenticationCallback) (string, error) {
	return p.state, nil
}

func (desktopCallbackExternalProvider) Complete(context.Context, ExternalProviderCompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	return nil, errors.New("not used")
}
