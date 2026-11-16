// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package identity owns who a caller is and how they prove it: sign-in,
// sessions, two-step verification, tokens, desktop authorization, account
// recovery and installation bootstrap. The parent app package builds the
// PasswordHasher, AttemptAccounting and CacheInvalidator once and shares them
// with onboarding.
package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// ErrAuthenticationCacheMiss and ErrAuthenticationCacheNotStored are the cache
// outcomes adapters must translate platform errors into, so this package never
// imports platform.
var (
	ErrAuthenticationCacheMiss      = errors.New("authentication cache: key not found")
	ErrAuthenticationCacheNotStored = errors.New("authentication cache: conditional write not applied")
)

// AuthenticationCache is the disposable cache shared by sign-in, DPoP and
// attempt accounting.
type AuthenticationCache interface {
	Get(context.Context, string) ([]byte, error)
	SetAlways(context.Context, string, []byte, time.Duration) error
	SetIfAbsent(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
	Add(context.Context, string, int64, time.Duration) (int64, error)
}

// Dependencies are the collaborators New hands to the Service; MFARekeyWake is
// required only when the store catalog has jobs.
type Dependencies struct {
	Store        store.Catalog
	Cache        AuthenticationCache
	Registry     ExternalProviderSource
	Authorizer   *access.Authorizer
	Auditor      *access.Auditor
	Capabilities *DeploymentCapabilities
	Publisher    realtime.Publisher
	Mail         *mail.Composer
	Hasher       *PasswordHasher
	Attempts     *AttemptAccounting
	Invalidator  *CacheInvalidator
	Invitations  InvitationAcceptor
	Operational  operational.Recorder
	// MFASecretSealer is nil until the MFA key ring is configured.
	MFASecretSealer *secretseal.Sealer
	MFARekeyWake    func()

	DesktopBuildCatalog      []model.DesktopBuildTuple
	SkipDesktopCompatibility bool

	NodeID                  string
	PublicURL               string
	LoopbackHTTPDevelopment bool

	MFA                     MFAPolicy
	Sessions                SessionPolicy
	LoginRateLimit          LoginRateLimitPolicy
	BootstrapProtection     BootstrapProtectionPolicy
	PersonalAccessToken     PersonalAccessTokenPolicy
	AccountRecovery         AccountRecoveryPolicy
	ExternalAuth            ExternalAuthenticationPolicy
	RecentAuthenticationTTL time.Duration

	AuthenticationDiagnostics *logging.Logger
	RecoveryDiagnostics       *logging.Logger
}

// Service holds every identity use case; build it with New.
type Service struct {
	users                        store.UserStore
	passwords                    store.PasswordCredentialStore
	sessions                     store.SessionStore
	sessionCredentials           store.SessionCredentialStore
	desktopRegistrations         store.DesktopRegistrationStore
	personalAccessTokens         store.PersonalAccessTokenStore
	browserAuthentications       store.BrowserAuthenticationStore
	institutions                 store.InstitutionStore
	desktopCompatibilityPolicies store.DesktopCompatibilityPolicyStore
	mfaCredentials               store.MFAStore
	userTokens                   store.UserTokenStore
	academicUnits                store.AcademicUnitStore
	jobs                         store.JobStore
	externalLoginStates          store.ExternalLoginStateStore
	externalIdentities           store.ExternalIdentityStore
	installations                store.InstallationStore

	accessPolicy *currentAuthenticationAccessPolicy
	dpop         *dpopSecurity
	cache        AuthenticationCache
	capabilities *DeploymentCapabilities
	attempts     *AttemptAccounting
	hasher       *PasswordHasher
	authorizer   *access.Authorizer
	auditor      access.MutationAuditAdapter
	// securityAudit records security actions outside an authenticated
	// mutation (sign-in, desktop handoffs, two-step changes, token changes).
	// When nil those flows fail closed rather than run unaudited.
	securityAudit *access.Auditor
	mail          *mail.Composer
	publisher     realtime.Publisher
	invalidator   realtime.AuthenticationInvalidator
	mfa           *mfaMechanics
	operational   operational.Recorder
	diagnostics   *logging.Logger
	// recoveryDiagnostics logs account recovery failures hidden from callers.
	recoveryDiagnostics *logging.Logger

	// invitations is bound to onboarding by composition after the Service is
	// built, so it can be nil during construction.
	externalProviders ExternalProviderSource
	invitations       InvitationAcceptor

	// skipDesktopBuildChecks admits any well-formed build; development only.
	desktopBuilds          *desktopBuildCatalog
	skipDesktopBuildChecks bool

	// mfaRekeyEnabled is false when the catalog lacks job or MFA stores, and
	// the key ring use cases are then unavailable.
	mfaRekeyEnabled bool
	mfaRekeyWake    func()

	// bootstrapSecretDigest is the only form of the bootstrap secret kept in
	// memory; bootstrapAvailable is false when none is configured.
	bootstrapSecretDigest          [sha256.Size]byte
	bootstrapAvailable             bool
	bootstrapRateLimitWindow       time.Duration
	bootstrapMaximumSourceAttempts int

	nodeID                       string
	publicURL                    string
	sessionPolicy                SessionPolicy
	loginRateLimit               LoginRateLimitPolicy
	personalAccessTokenPolicy    PersonalAccessTokenPolicy
	accountRecoveryPolicy        AccountRecoveryPolicy
	desktopPolicy                DesktopAuthorizationPolicy
	externalAuthenticationPolicy ExternalAuthenticationPolicy
	recentAuthenticationTTL      time.Duration
	newCredential                func() string
	now                          func() time.Time
	// wait applies a progressive login delay; tests replace it.
	wait func(context.Context, time.Duration) error
}

// New builds the Service and fails if any collaborator a use case needs is
// missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("identity store catalog is required")
	}

	mfa, err := newMFAMechanics(mfaMechanicsDependencies{Settings: deps.MFA, Sealer: deps.MFASecretSealer})
	if err != nil {
		return nil, err
	}

	if deps.MFASecretSealer != nil {
		if err = validateMFASecretKeys(context.Background(), deps.Store.MFA(), deps.MFASecretSealer); err != nil {
			return nil, err
		}
	}

	service := &Service{
		desktopCompatibilityPolicies: deps.Store.DesktopCompatibilityPolicy(),
		institutions:                 deps.Store.Institution(),
		authorizer:                   deps.Authorizer,
		auditor:                      deps.Auditor.MutationAudit(),
		skipDesktopBuildChecks:       deps.SkipDesktopCompatibility,
		recentAuthenticationTTL:      deps.RecentAuthenticationTTL,
		now:                          time.Now,
	}
	if err = service.requireDesktopCompatibility(deps.DesktopBuildCatalog); err != nil {
		return nil, err
	}
	if err = service.constructSessionAuthentication(deps, mfa); err != nil {
		return nil, err
	}
	if err := service.constructAccountServices(deps); err != nil {
		return nil, err
	}
	if err := service.constructExternalAuthentication(deps); err != nil {
		return nil, err
	}
	if err := service.constructAdministration(deps); err != nil {
		return nil, err
	}

	return service, nil
}

func (s *Service) constructSessionAuthentication(deps Dependencies, mfa *mfaMechanics) error {
	dpop, err := newDPoPSecurity(dpopSecurityDependencies{Cache: deps.Cache, Policy: dpopPolicy{
		Origin:        deps.PublicURL,
		NonceLifetime: 5 * time.Minute,
		ProofLifetime: 5 * time.Minute,
		ClockSkew:     time.Minute,
		NewNonce:      model.NewCredentialToken,
		Now:           time.Now,
	}})
	if err != nil {
		return err
	}

	authenticationAccess, err := newCurrentAuthenticationAccessPolicy(currentAuthenticationAccessPolicyDependencies{
		Policies:    deps.Store.AccessPolicy(),
		MFAEnabled:  deps.MFA.Enabled,
		Diagnostics: deps.AuthenticationDiagnostics,
		Now:         time.Now,
	})
	if err != nil {
		return err
	}

	s.users, s.passwords = deps.Store.User(), deps.Store.PasswordCredential()
	s.sessions, s.sessionCredentials = deps.Store.Session(), deps.Store.SessionCredential()
	s.desktopRegistrations, s.personalAccessTokens = deps.Store.DesktopRegistration(), deps.Store.PersonalAccessToken()
	s.mfaCredentials, s.mfa = deps.Store.MFA(), mfa
	s.accessPolicy, s.dpop, s.cache, s.attempts = authenticationAccess, dpop, deps.Cache, deps.Attempts
	s.hasher = deps.Hasher
	s.mail, s.publisher, s.capabilities = deps.Mail, deps.Publisher, deps.Capabilities
	s.securityAudit = deps.Auditor

	s.operational, s.diagnostics = deps.Operational, deps.AuthenticationDiagnostics
	s.sessionPolicy, s.loginRateLimit, s.personalAccessTokenPolicy = deps.Sessions, deps.LoginRateLimit, deps.PersonalAccessToken
	s.newCredential, s.wait = model.NewCredentialToken, waitContext
	if err = s.requireMFA(); err != nil {
		return err
	}
	if err = s.requireSessionAuthentication(); err != nil {
		return err
	}

	s.browserAuthentications = deps.Store.BrowserAuthentication()
	s.desktopPolicy = DesktopAuthorizationPolicy{Issuer: deps.PublicURL, AllowLoopbackHTTPDevelopment: deps.LoopbackHTTPDevelopment}
	if err = s.requireDesktopAuthorization(); err != nil {
		return err
	}

	return s.requireDesktopReauthentication()
}

func (s *Service) constructAccountServices(deps Dependencies) error {
	s.userTokens, s.academicUnits = deps.Store.UserToken(), deps.Store.AcademicUnit()
	s.recoveryDiagnostics, s.accountRecoveryPolicy = deps.RecoveryDiagnostics, deps.AccountRecovery
	s.nodeID, s.publicURL = deps.NodeID, deps.PublicURL
	if err := s.requireAccountTokens(); err != nil {
		return err
	}
	if err := s.requirePersonalAccessTokenAdministration(); err != nil {
		return err
	}
	return s.requireDesktopRegistration()
}

// The external policy's public URL, node and rate limit default to the
// Service's own.
func (s *Service) constructExternalAuthentication(deps Dependencies) error {
	externalPolicy := deps.ExternalAuth
	if externalPolicy.PublicURL == "" {
		externalPolicy.PublicURL = deps.PublicURL
	}
	if externalPolicy.NodeID == "" {
		externalPolicy.NodeID = deps.NodeID
	}
	if externalPolicy.LoginRateLimit == (LoginRateLimitPolicy{}) {
		externalPolicy.LoginRateLimit = deps.LoginRateLimit
	}
	s.externalProviders, s.invitations, s.externalAuthenticationPolicy = deps.Registry, deps.Invitations, externalPolicy
	s.externalLoginStates, s.externalIdentities = deps.Store.ExternalLoginState(), deps.Store.ExternalIdentity()
	s.invalidator = deps.Invalidator
	if err := s.requireExternalAuthentication(); err != nil {
		return err
	}
	return s.requireAuthenticationMethods()
}

func (s *Service) constructAdministration(deps Dependencies) error {
	s.installations = deps.Store.Installation()
	s.fixBootstrapProtection(deps.LoginRateLimit, deps.BootstrapProtection)
	if deps.Store.Job() == nil || deps.Store.MFA() == nil {
		return nil
	}
	s.jobs, s.mfaRekeyWake = deps.Store.Job(), deps.MFARekeyWake
	if err := s.requireMFARekey(); err != nil {
		return err
	}
	s.mfaRekeyEnabled = true
	return nil
}
