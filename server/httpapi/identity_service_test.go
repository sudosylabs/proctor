// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// identityFixture is the real identity.Service the sign-in, Session, MFA,
// token and Desktop handlers call. It runs over the Stores a test supplies,
// the real authorizer and auditors, the real mail Composer and the real
// Argon2id hasher, so a handler test asserts what reached the Store.
type identityFixture struct {
	access    *accesstest.Fixture
	cache     *identityCache
	mail      *identityMailRenderer
	publisher *realtimetest.Publisher
	// mfaSealer is the MFA key ring; nil unless the options enable MFA.
	mfaSealer *secretseal.Sealer
	service   *identity.Service
}

// identityStores are the Stores the identity area reads. A nil field becomes
// a Store the test does not expect the request to reach: calling it panics.
// The access policy and the MFA key state have working defaults because the
// service reads them on construction or on nearly every request.
type identityStores struct {
	Users                        store.UserStore
	Passwords                    store.PasswordCredentialStore
	Sessions                     store.SessionStore
	SessionCredentials           store.SessionCredentialStore
	DesktopRegistrations         store.DesktopRegistrationStore
	PersonalAccessTokens         store.PersonalAccessTokenStore
	MFA                          store.MFAStore
	BrowserAuthentications       store.BrowserAuthenticationStore
	UserTokens                   store.UserTokenStore
	AcademicUnits                store.AcademicUnitStore
	AccessPolicy                 store.AccessPolicyStore
	DesktopCompatibilityPolicies store.DesktopCompatibilityPolicyStore
	ExternalLoginStates          store.ExternalLoginStateStore
	ExternalIdentities           store.ExternalIdentityStore
	Installations                store.InstallationStore
	// Jobs, when set, enables the MFA key ring operations.
	Jobs store.JobStore
}

// identityOptions describe the deployment the fixture's service runs in.
type identityOptions struct {
	Stores identityStores
	Grants []accesstest.Grant
	// MFA enables two-step verification with a sealed key ring.
	MFA bool
	// MFARetiringKeys are older MFA keys the ring can still open, so a rekey
	// can retire them.
	MFARetiringKeys []string
	// Providers are the configured external identity providers.
	Providers []identity.ExternalIdentityProvider
	// Invitations admits External Identities through an Invitation.
	Invitations identity.InvitationAcceptor
	// DesktopBuilds is the Desktop build catalog compatibility checks use.
	DesktopBuilds []model.DesktopBuildTuple
	// MailDisabled makes the deployment report no durable mail.
	MailDisabled bool
	// BootstrapSecret protects installation bootstrap.
	BootstrapSecret string
	// RecentAuthenticationTTL is how long a sign-in counts as recent; zero
	// means 15 minutes.
	RecentAuthenticationTTL time.Duration
}

// identityPublicURL is the origin the fixture's Desktop authorization and
// external sign-in policies issue for; it matches validHTTPSettings.
const identityPublicURL = "http://localhost:8065"

// newIdentityFixture builds the real identity.Service over the options.
func newIdentityFixture(t *testing.T, options identityOptions) *identityFixture {
	t.Helper()
	fixture := &identityFixture{
		cache:     newIdentityCache(),
		mail:      &identityMailRenderer{},
		publisher: &realtimetest.Publisher{},
	}
	fixture.access = accesstest.New(t, accesstest.Options{Grants: options.Grants, Users: options.Stores.Users})
	stores := options.Stores.withDefaults(t)

	logger, _ := routetest.NewLogger(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	sender := &identityMailSender{enabled: !options.MailDisabled}
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: fixture.mail, Sender: sender, Sealer: sealer})
	require.NoError(t, err)
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: identityPasswordPolicy()})
	require.NoError(t, err)
	attempts, err := identity.NewAttemptAccounting(identity.AttemptAccountingDependencies{Cache: fixture.cache})
	require.NoError(t, err)
	invalidator, err := identity.NewCacheInvalidator(identity.CacheInvalidatorDependencies{Cache: fixture.cache, Diagnostics: logger})
	require.NoError(t, err)

	providers := identityProviders{byID: map[string]identity.ExternalIdentityProvider{}}
	for _, provider := range options.Providers {
		providers.descriptors = append(providers.descriptors, provider.Descriptor())
		providers.byID[provider.Descriptor().ID] = provider
	}
	invitations := options.Invitations
	if invitations == nil {
		invitations = refusedInvitations{}
	}
	if options.MFA {
		fixture.mfaSealer, err = secretseal.New(secretseal.Settings{
			EncryptionKey: key, DecryptionKeys: options.MFARetiringKeys, MaximumPlaintext: secretseal.MaximumPlaintextBytes,
		})
		require.NoError(t, err)
	}
	loginRateLimit := identity.LoginRateLimitPolicy{
		Window: time.Minute, MaximumAttempts: 20, MaximumSourceAttempts: 100,
		AccountFailuresBeforeDelay: 50, MaximumAccountFailureDelay: time.Second,
	}

	recentTTL := options.RecentAuthenticationTTL
	if recentTTL == 0 {
		recentTTL = 15 * time.Minute
	}

	service, err := identity.New(identity.Dependencies{
		Store:      identityCatalog{stores: stores, institution: fixture.access.Institution},
		Cache:      fixture.cache,
		Registry:   providers,
		Authorizer: fixture.access.Authorizer,
		Auditor:    fixture.access.Auditor,
		Capabilities: &identity.DeploymentCapabilities{
			Providers: providers, Mail: sender, Health: mail.NewHealth(true), MFAEnabled: options.MFA,
		},
		Publisher:               fixture.publisher,
		Mail:                    composer,
		Hasher:                  hasher,
		Attempts:                attempts,
		Invalidator:             invalidator,
		Invitations:             invitations,
		MFASecretSealer:         fixture.mfaSealer,
		MFARekeyWake:            func() {},
		DesktopBuildCatalog:     options.DesktopBuilds,
		NodeID:                  "node-a",
		PublicURL:               identityPublicURL,
		LoopbackHTTPDevelopment: true,
		MFA:                     identity.MFAPolicy{Enabled: options.MFA, Issuer: "Proctor", SetupTTL: 10 * time.Minute, RecoveryCodeCount: 10},
		Sessions: identity.SessionPolicy{
			AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour, IdleTTL: 2 * time.Hour,
			AbsoluteTTL: 7 * 24 * time.Hour, ActivityUpdateInterval: time.Minute,
			MaximumPerUser: 10, RetiredRetention: time.Hour,
		},
		LoginRateLimit:      loginRateLimit,
		BootstrapProtection: identity.BootstrapProtectionPolicy{Secret: options.BootstrapSecret},
		PersonalAccessToken: identity.PersonalAccessTokenPolicy{
			MinimumLifetime: time.Hour, MaximumLifetime: 365 * 24 * time.Hour,
			LastUsedUpdateInterval: time.Minute, MaximumPerUser: 50,
		},
		AccountRecovery: identity.AccountRecoveryPolicy{
			EmailVerificationTTL: 24 * time.Hour, PasswordResetTTL: time.Hour, EmailChangeUndoTTL: 7 * 24 * time.Hour,
			RateLimit: identity.LoginRateLimitPolicy{Window: time.Minute, MaximumAttempts: 20, MaximumSourceAttempts: 100},
		},
		ExternalAuth:              identity.ExternalAuthenticationPolicy{LoginStateTTL: 10 * time.Minute},
		RecentAuthenticationTTL:   recentTTL,
		AuthenticationDiagnostics: logger,
		RecoveryDiagnostics:       logger,
	})
	require.NoError(t, err)
	fixture.service = service
	return fixture
}

// identityPasswordPolicy is the production password policy with the cheapest
// Argon2id parameters the hasher accepts.
func identityPasswordPolicy() identity.PasswordPolicy {
	return identity.PasswordPolicy{
		MinimumLength: 8, MaximumLength: 128, MaximumConcurrentOperations: 2,
		ArgonMemoryKiB: 19 * 1024, ArgonIterations: 1, ArgonParallelism: 1,
		ArgonSaltBytes: 16, ArgonKeyBytes: 32,
	}
}

// hashPassword encodes password with the fixture's hasher settings, so a
// stored credential verifies against what a request sends.
func hashPassword(t *testing.T, password string) string {
	t.Helper()
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: identityPasswordPolicy()})
	require.NoError(t, err)
	encoded, err := hasher.Hash(t.Context(), password)
	require.NoError(t, err)
	return encoded
}

// identityCatalog serves the fixture's Stores to identity.New.
type identityCatalog struct {
	store.Catalog
	stores      identityStores
	institution *model.Institution
}

func (c identityCatalog) User() store.UserStore { return c.stores.Users }
func (c identityCatalog) PasswordCredential() store.PasswordCredentialStore {
	return c.stores.Passwords
}
func (c identityCatalog) Session() store.SessionStore { return c.stores.Sessions }
func (c identityCatalog) SessionCredential() store.SessionCredentialStore {
	return c.stores.SessionCredentials
}
func (c identityCatalog) DesktopRegistration() store.DesktopRegistrationStore {
	return c.stores.DesktopRegistrations
}
func (c identityCatalog) PersonalAccessToken() store.PersonalAccessTokenStore {
	return c.stores.PersonalAccessTokens
}
func (c identityCatalog) MFA() store.MFAStore { return c.stores.MFA }
func (c identityCatalog) BrowserAuthentication() store.BrowserAuthenticationStore {
	return c.stores.BrowserAuthentications
}
func (c identityCatalog) UserToken() store.UserTokenStore       { return c.stores.UserTokens }
func (c identityCatalog) AcademicUnit() store.AcademicUnitStore { return c.stores.AcademicUnits }
func (c identityCatalog) Institution() store.InstitutionStore {
	return identityInstitution{institution: c.institution}
}
func (c identityCatalog) AccessPolicy() store.AccessPolicyStore { return c.stores.AccessPolicy }
func (c identityCatalog) DesktopCompatibilityPolicy() store.DesktopCompatibilityPolicyStore {
	return c.stores.DesktopCompatibilityPolicies
}
func (c identityCatalog) ExternalLoginState() store.ExternalLoginStateStore {
	return c.stores.ExternalLoginStates
}
func (c identityCatalog) ExternalIdentity() store.ExternalIdentityStore {
	return c.stores.ExternalIdentities
}
func (c identityCatalog) Installation() store.InstallationStore { return c.stores.Installations }
func (c identityCatalog) Job() store.JobStore                   { return c.stores.Jobs }

// withDefaults fills every Store the test left out.
func (s identityStores) withDefaults(t *testing.T) identityStores {
	t.Helper()
	if s.Users == nil {
		s.Users = struct{ store.UserStore }{}
	}
	if s.Passwords == nil {
		s.Passwords = struct{ store.PasswordCredentialStore }{}
	}
	if s.Sessions == nil {
		s.Sessions = struct{ store.SessionStore }{}
	}
	if s.SessionCredentials == nil {
		s.SessionCredentials = struct{ store.SessionCredentialStore }{}
	}
	if s.DesktopRegistrations == nil {
		s.DesktopRegistrations = struct{ store.DesktopRegistrationStore }{}
	}
	if s.PersonalAccessTokens == nil {
		s.PersonalAccessTokens = struct{ store.PersonalAccessTokenStore }{}
	}
	if s.MFA == nil {
		s.MFA = &identityMFAKeys{}
	}
	if s.BrowserAuthentications == nil {
		s.BrowserAuthentications = struct {
			store.BrowserAuthenticationStore
		}{}
	}
	if s.UserTokens == nil {
		s.UserTokens = struct{ store.UserTokenStore }{}
	}
	if s.AcademicUnits == nil {
		s.AcademicUnits = struct{ store.AcademicUnitStore }{}
	}
	if s.AccessPolicy == nil {
		s.AccessPolicy = newIdentityAccessPolicy(t, nil)
	}
	if s.DesktopCompatibilityPolicies == nil {
		s.DesktopCompatibilityPolicies = struct {
			store.DesktopCompatibilityPolicyStore
		}{}
	}
	if s.ExternalLoginStates == nil {
		s.ExternalLoginStates = struct{ store.ExternalLoginStateStore }{}
	}
	if s.ExternalIdentities == nil {
		s.ExternalIdentities = struct{ store.ExternalIdentityStore }{}
	}
	if s.Installations == nil {
		s.Installations = struct{ store.InstallationStore }{}
	}
	return s
}

// identityInstitution is the one Institution the authorizer also knows.
type identityInstitution struct {
	store.InstitutionStore
	institution *model.Institution
}

func (s identityInstitution) Get(context.Context, model.InstitutionID) (*model.Institution, error) {
	return s.institution, nil
}

func (s identityInstitution) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

// identityAccessPolicy is the current Access Policy: local sign-in and
// Desktop authorization enabled unless a test changes the policy.
type identityAccessPolicy struct {
	store.AccessPolicyStore
	policy *model.AccessPolicy
}

// newIdentityAccessPolicy stores the initial policy after change, which may
// be nil.
func newIdentityAccessPolicy(t *testing.T, change func(*model.AccessPolicy)) *identityAccessPolicy {
	t.Helper()
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.UnixMilli(1_000))
	policy.LocalLoginEnabled = true
	policy.InvitationLocalCredentialEnabled = true
	policy.DesktopAuthorizationEnabled = true
	if change != nil {
		change(policy)
	}
	require.NoError(t, policy.Validate())
	return &identityAccessPolicy{policy: policy}
}

func (s *identityAccessPolicy) Get(context.Context, int) (*store.AccessPolicySnapshot, error) {
	return &store.AccessPolicySnapshot{Policy: s.policy}, nil
}

func (s *identityAccessPolicy) HasRoleHolderMFARequirement(context.Context, model.UserID, time.Time) (bool, error) {
	return false, nil
}

// identityMFAKeys reports a key ring with no fenced primary, so any sealer
// configuration is accepted.
type identityMFAKeys struct{ store.MFAStore }

func (*identityMFAKeys) InspectKeyState(context.Context) (*store.MFAKeyState, error) {
	return &store.MFAKeyState{}, nil
}

// identityCache is the in-memory authentication cache the DPoP, attempt and
// invalidation mechanics share.
type identityCache struct {
	mu       sync.Mutex
	values   map[string][]byte
	counters map[string]int64
}

func newIdentityCache() *identityCache {
	return &identityCache{values: map[string][]byte{}, counters: map[string]int64{}}
}

func (c *identityCache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	if !ok {
		return nil, identity.ErrAuthenticationCacheMiss
	}
	return append([]byte(nil), value...), nil
}

func (c *identityCache) SetAlways(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func (c *identityCache) SetIfAbsent(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; ok {
		return identity.ErrAuthenticationCacheNotStored
	}
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func (c *identityCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key)
	delete(c.counters, key)
	return nil
}

func (c *identityCache) Add(_ context.Context, key string, delta int64, _ time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counters[key] += delta
	return c.counters[key], nil
}

// identityMailRenderer records the templates the real Composer rendered.
type identityMailRenderer struct {
	mu       sync.Mutex
	requests []mail.RenderRequest
}

func (r *identityMailRenderer) Render(request mail.RenderRequest) (mail.FrozenContent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return mail.FrozenContent{Subject: "Notice", Text: "Notice", HTML: "<p>Notice</p>"}, nil
}

// templates returns the template of each rendered message, in order.
func (r *identityMailRenderer) templates() []model.MailTemplateKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]model.MailTemplateKey, 0, len(r.requests))
	for _, request := range r.requests {
		keys = append(keys, request.Key)
	}
	return keys
}

// identityMailSender is the deployment's mail transport. The Composer only
// asks whether it is enabled and who mail is from; delivery Jobs send.
type identityMailSender struct{ enabled bool }

func (s *identityMailSender) Enabled() bool { return s.enabled }

func (s *identityMailSender) From() mail.Address {
	return mail.Address{Name: "Proctor", Address: "no-reply@example.test"}
}

func (s *identityMailSender) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, nil
}

// identityProviders is the external provider registry.
type identityProviders struct {
	descriptors []model.ExternalAuthenticationProvider
	byID        map[string]identity.ExternalIdentityProvider
}

func (p identityProviders) Descriptors() []model.ExternalAuthenticationProvider { return p.descriptors }

func (p identityProviders) Provider(id string) (identity.ExternalIdentityProvider, bool) {
	provider, ok := p.byID[id]
	return provider, ok
}

// refusedInvitations admits no External Identity through an Invitation.
type refusedInvitations struct{}

func (refusedInvitations) AcceptExternalIdentity(
	context.Context, *model.ExternalLoginState, *model.ExternalAuthenticationAssertion,
	store.AccessDeploymentCapabilities, model.RequestMetadata, string,
) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	return nil, store.NewErrNotFound("invitation", "")
}

// identityUsers holds the Users the identity area finds by ID, username or
// email, and counts the sign-in lookups it answered.
type identityUsers struct {
	store.UserStore
	mu      sync.Mutex
	byID    map[model.UserID]*model.User
	lookups int
}

func newIdentityUsers(users ...*model.User) *identityUsers {
	s := &identityUsers{byID: map[model.UserID]*model.User{}}
	for _, user := range users {
		s.byID[user.ID] = user
	}
	return s
}

// lookupCount is how many username or email lookups reached the Store.
func (s *identityUsers) lookupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
}

func (s *identityUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("user", id.String())
	}
	copied := *user
	return &copied, nil
}

func (s *identityUsers) GetByUsername(_ context.Context, username string) (*model.User, error) {
	return s.find(func(user *model.User) bool { return user.Username == username })
}

func (s *identityUsers) GetByEmail(_ context.Context, email string) (*model.User, error) {
	return s.find(func(user *model.User) bool { return user.Email == email })
}

func (s *identityUsers) find(match func(*model.User) bool) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	for _, user := range s.byID {
		if match(user) {
			copied := *user
			return &copied, nil
		}
	}
	return nil, store.NewErrNotFound("user", "")
}

// identityPasswords holds each User's password credential.
type identityPasswords struct {
	store.PasswordCredentialStore
	byUser map[model.UserID]*model.PasswordCredential
}

func (s *identityPasswords) GetByUser(_ context.Context, userID model.UserID) (*model.PasswordCredential, error) {
	credential, ok := s.byUser[userID]
	if !ok {
		return nil, store.NewErrNotFound("password_credential", userID.String())
	}
	copied := *credential
	return &copied, nil
}
