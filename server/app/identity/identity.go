// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package identity owns who a caller is and how they prove it: local and
// external sign-in, Sessions and their assurance, two-step verification,
// personal access tokens, Desktop authorization, account recovery, account
// state, email addresses, and installation bootstrap. Service holds every one
// of these use cases, one file per concern.
//
// The mechanics the flows share stay inside the package behind them: the
// password hasher, attempt accounting, the authentication cache invalidator,
// and the TOTP mechanics. The parent app package builds the hasher, the
// attempt accounting and the invalidator once with the exported
// constructors, and shares them with the onboarding area's Invitations and
// public registration and with its own test mail and realtime propagation.
//
// Start with identity.go for the Service and its collaborators, then open the
// file named for the concern you are changing.
package identity

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// ErrAuthenticationCacheMiss and ErrAuthenticationCacheNotStored are the
// transport-neutral sentinels authentication uses for disposable cache
// outcomes. Composition adapters translate infrastructure cache errors into
// these values so authentication never imports platform.
var (
	ErrAuthenticationCacheMiss      = errors.New("authentication cache: key not found")
	ErrAuthenticationCacheNotStored = errors.New("authentication cache: conditional write not applied")
)

// resourceAuthorizer is the one authorization port. A use case authorizes an
// Action on a concrete Resource immediately before the work it guards.
type resourceAuthorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// institutionAuthorizer authorizes an Action on the installation's singleton
// Institution and returns that Resource for the caller's audit.
type institutionAuthorizer interface {
	AuthorizeInstitution(context.Context, invocation.Invocation, model.Action) (model.Resource, error)
}

// OperationalRecorder receives the outcome of each authentication entry point
// the package observes. It is optional; a Service built without one records
// nothing.
type OperationalRecorder interface {
	RecordOperational(subsystem, event string, err error)
}

func recordOperational(recorder OperationalRecorder, subsystem, event string, err error) {
	if recorder != nil {
		recorder.RecordOperational(subsystem, event, err)
	}
}

// RecoveryDiagnostics reports non-fatal recovery failures without depending
// on a concrete logger package.
type RecoveryDiagnostics interface {
	ErrorContext(ctx context.Context, message string, err error)
}

// PasswordPolicy is the immutable password-hashing projection composition
// supplies so password code does not import deployment config.
type PasswordPolicy struct {
	MinimumLength               int
	MaximumLength               int
	ArgonMemoryKiB              int
	ArgonIterations             int
	ArgonParallelism            int
	ArgonSaltBytes              int
	ArgonKeyBytes               int
	MaximumConcurrentOperations int
	HistoryCount                int
}

// PasswordWorkRecorder observes bounded runtime password work. Implementations
// must be concurrency-safe and return promptly; nil disables recording.
type PasswordWorkRecorder interface {
	Started()
	Finished(time.Duration)
	Rejected()
}

// AccountRecoveryPolicy is the operator projection for email verification and
// password reset TTLs and rate limits.
type AccountRecoveryPolicy struct {
	EmailVerificationTTL time.Duration
	PasswordResetTTL     time.Duration
	EmailChangeUndoTTL   time.Duration
	RateLimit            LoginRateLimitPolicy
}

// MFAPolicy is the enrollment and verification policy for TOTP MFA. Key-ring
// construction remains at the server composition root.
type MFAPolicy struct {
	Enabled           bool
	Issuer            string
	SetupTTL          time.Duration
	RecoveryCodeCount int
}

// Dependencies are the collaborators New distributes to the Service. Hasher,
// Attempts and Invalidator are the shared mechanics the parent builds once
// with NewPasswordHasher, NewAttemptAccounting and NewCacheInvalidator.
// MFARekeyWake is required only when the Store catalog has Jobs.
type Dependencies struct {
	Store        store.Catalog
	Cache        AuthenticationCache
	Registry     ExternalProviderSource
	Authorizer   *access.Authorizer
	Auditor      *access.Auditor
	Capabilities AccessPolicyCapabilitySource
	Effects      SecurityEffects
	Mail         *mail.Composer
	Hasher       *PasswordHasher
	Attempts     *AttemptAccounting
	Invalidator  *CacheInvalidator
	Invitations  InvitationAcceptor
	Operational  OperationalRecorder
	// MFASecretSealer seals TOTP secrets; it is nil until the MFA ring is
	// configured.
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

	AuthenticationDiagnostics AuthenticationDiagnostics
	RecoveryDiagnostics       RecoveryDiagnostics
}

// Service holds every identity use case. Each concern keeps its own focused
// implementation, embedded here so the Service's method set is their exported
// methods.
type Service struct {
	*authenticationService
	*desktopAuthorizationService
	*desktopReauthenticationService
	*desktopRegistrationService
	*desktopCompatibilityService
	*selfSessionService
	*externalAuthenticationService
	*authenticationMethodService
	*mfaApplicationService
	*mfaKeyService
	*accountTokenService
	*personalAccessTokenAdministrationService
	*accountStateService
	*sessionAdministrationService
	*bootstrapService
}

// New builds the Service and every focused implementation it embeds.
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
	desktopCompatibility, err := newDesktopCompatibilityService(desktopCompatibilityServiceDependencies{Policies: deps.Store.DesktopCompatibilityPolicy(), Institutions: deps.Store.Institution(), Authorization: deps.Authorizer, Audit: deps.Auditor.MutationAudit(), Builds: deps.DesktopBuildCatalog, SkipBuildChecks: deps.SkipDesktopCompatibility, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Now: time.Now})
	if err != nil {
		return nil, err
	}
	service := &Service{desktopCompatibilityService: desktopCompatibility}
	authenticationAccess, err := service.constructSessionAuthentication(deps, mfa)
	if err != nil {
		return nil, err
	}
	if err := service.constructAccountServices(deps, authenticationAccess); err != nil {
		return nil, err
	}
	if err := service.constructExternalAuthentication(deps, authenticationAccess); err != nil {
		return nil, err
	}
	if err := service.constructAdministration(deps); err != nil {
		return nil, err
	}
	return service, nil
}

// constructSessionAuthentication builds DPoP, the authentication access policy, MFA, local authentication and desktop authorization.
func (s *Service) constructSessionAuthentication(deps Dependencies, mfa *mfaMechanics) (*currentAuthenticationAccessPolicy, error) {
	dpop, err := newDPoPSecurity(dpopSecurityDependencies{Cache: deps.Cache, Policy: dpopPolicy{Origin: deps.PublicURL,
		NonceLifetime: 5 * time.Minute, ProofLifetime: 5 * time.Minute, ClockSkew: time.Minute,
		NewNonce: model.NewCredentialToken, Now: time.Now}})
	if err != nil {
		return nil, err
	}
	authenticationAccess, err := newCurrentAuthenticationAccessPolicy(currentAuthenticationAccessPolicyDependencies{Policies: deps.Store.AccessPolicy(), MFAEnabled: deps.MFA.Enabled, Diagnostics: deps.AuthenticationDiagnostics, Now: time.Now})
	if err != nil {
		return nil, err
	}
	mfaApplication, err := newMFAApplicationService(mfaApplicationServiceDependencies{Users: deps.Store.User(), Credentials: deps.Store.MFA(), Passwords: deps.Store.PasswordCredential(), Sessions: deps.Store.Session(), Institutions: deps.Store.Institution(), Audit: mfaAuditAdapter{audit: deps.Auditor}, Effects: deps.Effects, Mail: deps.Mail, Mechanics: mfa, Security: mfaSecurityDependencies{authorization: deps.Authorizer, capabilities: deps.Capabilities, attempts: deps.Attempts, rateLimit: deps.LoginRateLimit}, RoleHolderMFA: authenticationAccess, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Now: time.Now})
	if err != nil {
		return nil, err
	}
	// Expand PAT policy used both at bearer resolution and administration.
	patResolver, err := newPersonalAccessTokenBearerResolver(personalAccessTokenBearerResolverDependencies{Tokens: deps.Store.PersonalAccessToken(), Policy: deps.PersonalAccessToken, Diagnostics: deps.AuthenticationDiagnostics})
	if err != nil {
		return nil, err
	}
	authentication, err := newAuthenticationService(authenticationServiceDependencies{Users: deps.Store.User(), Passwords: deps.Store.PasswordCredential(), Sessions: deps.Store.Session(), SessionCredentials: deps.Store.SessionCredential(), AccessPolicy: authenticationAccess, Cache: deps.Cache, Attempts: deps.Attempts, SecurityEffects: deps.Effects, Audit: deps.Auditor.MutationAudit(), LoginAudit: localLoginAuditAdapter{audit: deps.Auditor}, Hasher: deps.Hasher, MFA: mfaApplication, PersonalTokens: patResolver, SessionPolicy: deps.Sessions, LoginRateLimit: deps.LoginRateLimit, Diagnostics: deps.AuthenticationDiagnostics, NewCredential: model.NewCredentialToken, Now: time.Now, Desktop: authenticationDesktopDependencies{registrations: deps.Store.DesktopRegistration(), dpop: dpop}})
	if err != nil {
		return nil, err
	}
	authentication.operational = deps.Operational
	desktopPolicy := DesktopAuthorizationPolicy{Issuer: deps.PublicURL, AllowLoopbackHTTPDevelopment: deps.LoopbackHTTPDevelopment}
	desktopAttempts := desktopAuthorizationAttemptAccounting{attempts: deps.Attempts, policy: deps.LoginRateLimit}
	desktopAuthorization, err := newDesktopAuthorizationService(desktopAuthorizationServiceDependencies{Transactions: deps.Store.BrowserAuthentication(), Institutions: deps.Store.Institution(), AccessPolicy: authenticationAccess, Capabilities: deps.Capabilities, Audit: desktopAuthorizationAuditAdapter{audit: deps.Auditor}, Attempts: desktopAttempts, SessionPolicy: deps.Sessions, Policy: desktopPolicy, NewCredential: model.NewCredentialToken, Now: time.Now, Identity: desktopAuthorizationIdentityDependencies{users: deps.Store.User(), authentication: authentication,
		compatibility: s.desktopCompatibilityService, dpop: dpop}})
	if err != nil {
		return nil, err
	}
	desktopReauthentication, err := newDesktopReauthenticationService(desktopReauthenticationServiceDependencies{Transactions: deps.Store.BrowserAuthentication(), Institutions: deps.Store.Institution(), Users: deps.Store.User(), Capabilities: deps.Capabilities, Audit: desktopReauthenticationAuditAdapter{audit: deps.Auditor}, Attempts: desktopAttempts, Authentication: authentication, Policy: desktopPolicy, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, NewCredential: model.NewCredentialToken, Now: time.Now})
	if err != nil {
		return nil, err
	}
	s.mfaApplicationService, s.authenticationService = mfaApplication, authentication
	s.desktopAuthorizationService, s.desktopReauthenticationService = desktopAuthorization, desktopReauthentication
	return authenticationAccess, nil
}

// constructAccountServices builds self-service Sessions, account tokens, token administration and Desktop registrations.
func (s *Service) constructAccountServices(deps Dependencies, authenticationAccess *currentAuthenticationAccessPolicy) error {
	selfSessions, err := newSelfSessionService(selfSessionServiceDependencies{Sessions: deps.Store.Session(), Audit: deps.Auditor.MutationAudit(), Effects: deps.Effects, Now: time.Now})
	if err != nil {
		return err
	}
	accountTokens, err := newAccountTokenService(accountTokenServiceDependencies{Users: deps.Store.User(), Passwords: deps.Store.PasswordCredential(), Tokens: deps.Store.UserToken(), AccessPolicy: authenticationAccess, Institutions: deps.Store.Institution(), Mail: deps.Mail, Attempts: deps.Attempts, Hasher: deps.Hasher, Audit: accountTokenAuditRecorder{nodeID: deps.NodeID}, Effects: deps.Effects, Diagnostics: deps.RecoveryDiagnostics, Policy: deps.AccountRecovery, PublicURL: deps.PublicURL, NewToken: model.NewCredentialToken, Now: time.Now})
	if err != nil {
		return err
	}
	accountTokens.administration = userEmailAdministration{
		authorization: deps.Authorizer, audit: deps.Auditor.MutationAudit(),
		recentAuthenticationTTL: deps.RecentAuthenticationTTL,
	}
	personalAccessTokenAdministration, err := newPersonalAccessTokenAdministrationService(personalAccessTokenAdministrationServiceDependencies{Tokens: deps.Store.PersonalAccessToken(), Users: deps.Store.User(), AcademicUnits: deps.Store.AcademicUnit(), Institutions: deps.Store.Institution(), Audit: personalAccessTokenAuditAdapter{audit: deps.Auditor}, Authorization: deps.Authorizer, Mail: deps.Mail, Policy: deps.PersonalAccessToken, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, NewCredential: model.NewCredentialToken, Now: time.Now})
	if err != nil {
		return err
	}
	desktopRegistrations, err := newDesktopRegistrationService(desktopRegistrationServiceDependencies{Registrations: deps.Store.DesktopRegistration(), Audit: deps.Auditor.MutationAudit(), Effects: deps.Effects, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Now: time.Now})
	if err != nil {
		return err
	}
	s.selfSessionService, s.accountTokenService = selfSessions, accountTokens
	s.personalAccessTokenAdministrationService, s.desktopRegistrationService = personalAccessTokenAdministration, desktopRegistrations
	return nil
}

// constructExternalAuthentication builds external authentication and authentication method management.
func (s *Service) constructExternalAuthentication(deps Dependencies, authenticationAccess *currentAuthenticationAccessPolicy) error {
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
	externalAuthentication, err := newExternalAuthenticationService(externalAuthenticationServiceDependencies{Registry: deps.Registry, LoginStates: deps.Store.ExternalLoginState(), Institutions: deps.Store.Institution(), Identities: deps.Store.ExternalIdentity(), Sessions: deps.Store.Session(), AccessPolicy: authenticationAccess, Attempts: deps.Attempts, Authentication: s.authenticationService, Invalidator: deps.Invalidator, Audit: deps.Auditor, MutationAudit: deps.Auditor.MutationAudit(), Capabilities: deps.Capabilities, InvitationAcceptor: deps.Invitations, DesktopAuthorization: s.desktopAuthorizationService, DesktopReauthentication: s.desktopReauthenticationService, Policy: externalPolicy, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Diagnostics: deps.AuthenticationDiagnostics, NewCredential: model.NewCredentialToken, Now: time.Now})
	if err != nil {
		return err
	}
	authenticationMethods, err := newAuthenticationMethodService(authenticationMethodServiceDependencies{Users: deps.Store.User(), Mail: deps.Mail, Passwords: deps.Store.PasswordCredential(), Identities: deps.Store.ExternalIdentity(), Providers: deps.Registry, Capabilities: deps.Capabilities, Hasher: deps.Hasher, Audit: deps.Auditor.MutationAudit(), Effects: deps.Effects, RecentTTL: deps.RecentAuthenticationTTL, Now: time.Now})
	if err != nil {
		return err
	}
	s.externalAuthenticationService, s.authenticationMethodService = externalAuthentication, authenticationMethods
	return nil
}

// constructAdministration builds account state, Session administration,
// bootstrap and, when the catalog has Jobs, the MFA key ring.
func (s *Service) constructAdministration(deps Dependencies) error {
	s.accountStateService = newAccountStateService(accountStateServiceDependencies{Users: deps.Store.User(), Authorization: deps.Authorizer, Capabilities: deps.Capabilities, Audit: deps.Auditor.MutationAudit(), Mail: deps.Mail, Effects: accountStateRealtimeEffects{effects: deps.Effects}, Now: time.Now})
	s.sessionAdministrationService = newSessionAdministrationService(sessionAdministrationServiceDependencies{Sessions: deps.Store.Session(), Users: deps.Store.User(), Authorization: deps.Authorizer, Audit: deps.Auditor.MutationAudit(), Mail: deps.Mail, Effects: deps.Effects, Now: time.Now})
	s.bootstrapService = newBootstrapService(bootstrapServiceDependencies{Installations: deps.Store.Installation(), Hasher: deps.Hasher, Attempts: deps.Attempts, RateLimit: deps.LoginRateLimit, Protection: deps.BootstrapProtection, NodeID: deps.NodeID, Now: time.Now, RecoveryPolicy: administratorRecoveryPolicy{capabilities: deps.Capabilities, mfaEnabled: deps.MFA.Enabled}})
	if deps.Store.Job() == nil || deps.Store.MFA() == nil {
		return nil
	}
	mfaKeys, err := newMFAKeyService(mfaKeyServiceDependencies{Keys: deps.Store.MFA(), Jobs: deps.Store.Job(),
		Authorization: mfaKeyAuthorization{authorization: deps.Authorizer, institutions: deps.Store.Institution()},
		Audit:         deps.Auditor.MutationAudit(), Sealer: deps.MFASecretSealer,
		RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Now: time.Now, Wake: deps.MFARekeyWake})
	if err != nil {
		return err
	}
	s.mfaKeyService = mfaKeys
	return nil
}

// MaintenanceTasks returns the bounded periodic passes that expire and purge
// browser authentication transactions, external login states and personal
// access token preparations. Every node may run them.
func MaintenanceTasks(catalog store.Catalog) []job.PeriodicTask {
	return []job.PeriodicTask{
		{Name: "browser-authentication-maintenance", Interval: browserAuthenticationMaintenanceInterval,
			Runner: browserAuthenticationMaintenancePeriodicRunner{transactions: catalog.BrowserAuthentication()}},
		{Name: "external-authentication-maintenance", Interval: externalAuthenticationMaintenanceInterval,
			Runner: externalAuthenticationMaintenancePeriodicRunner{states: catalog.ExternalLoginState()}},
		{Name: "personal-access-token-maintenance", Interval: personalAccessTokenMaintenanceInterval,
			Runner: personalAccessTokenMaintenancePeriodicRunner{tokens: catalog.PersonalAccessToken()}},
	}
}

func sessionIDs(sessions []*model.Session) []model.SessionID {
	ids := make([]model.SessionID, 0, len(sessions))
	for _, session := range sessions {
		if session != nil {
			ids = append(ids, session.ID)
		}
	}
	return ids
}

func validRekeyKeyID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
