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
	"github.com/sudosylabs/proctor/server/logging"
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
// Action on a concrete Resource immediately before the work it guards, and
// personal access token creation asks which actions the caller may delegate.
type resourceAuthorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
	CanDelegateActionsAtScope(context.Context, model.Principal, []string, model.RoleScopeType, string) (bool, error)
	PersonalAccessTokenDelegation(context.Context, model.Principal) (access.PersonalAccessTokenDelegation, error)
}

var _ resourceAuthorizer = (*access.Authorizer)(nil)

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

	AuthenticationDiagnostics *logging.Logger
	RecoveryDiagnostics       *logging.Logger
}

// securityAuditor is the part of *access.Auditor that records security
// actions proved outside an authenticated mutation: a local password sign-in
// (the admitted attempt, its completion and a terminal failure), the Desktop
// authorization and reauthentication handoffs, which act for a proved account
// at Institution scope, the two-step verification transitions, recorded for
// the acting User as their subject, and the personal access token drafts that
// the token aggregate completes. Mutations are audited through the Service's
// access.MutationAuditor.
type securityAuditor interface {
	BeginLocalLogin(context.Context, model.UserID, model.SessionClientType, model.RequestMetadata) (*model.AuditEvent, error)
	BeginAccountActionAtScope(context.Context, access.AccountAuditActor, model.Action, model.Resource, model.RoleScopeType,
		string, model.RequestMetadata, any) (*model.AuditEvent, error)
	BeginCriticalActionForSubject(context.Context, model.Principal, model.UserID, model.Action, model.Resource,
		model.RequestMetadata, any, any) (*model.AuditEvent, error)
	PrepareCriticalAction(context.Context, model.Principal, model.Action, model.Resource, model.RequestMetadata,
		any, any) (*model.AuditEvent, error)
	CompleteCriticalAction(context.Context, model.AuditEventID, model.AuditStatus, string, any) (*model.AuditEvent, error)
	RecordLocalLoginFailure(context.Context, model.UserID, model.SessionClientType, model.RequestMetadata, string) error
}

var _ securityAuditor = (*access.Auditor)(nil)

// mailComposer is the part of *mail.Composer the Service prepares frozen
// account-token mail and security notices with.
type mailComposer interface {
	Enabled() bool
	PrepareEmailVerification(mail.AccountTokenPreparation) (*store.PreparedMail, error)
	PreparePasswordReset(mail.AccountTokenPreparation) (*store.PreparedMail, error)
	PreparePasswordChanged(mail.NoticePreparation) (*store.PreparedMail, error)
	PrepareEmailChangeWarning(mail.AccountTokenPreparation) (*store.PreparedMail, error)
	PrepareEmailChangeVerification(mail.AccountTokenPreparation) (*store.PreparedMail, error)
	PrepareEmailVerifiedByAdministrator(mail.NoticePreparation) (*store.PreparedMail, error)
	PrepareMFANotice(mail.NoticePreparation, mail.MFANoticeKind) (*store.PreparedMail, error)
	PreparePersonalAccessTokenSecurityNotice(mail.PersonalAccessTokenPreparation) (*store.PreparedMail, error)
	PrepareSessionsRevokedByAdministrator(mail.NoticePreparation) (*store.PreparedMail, error)
}

var _ mailComposer = (*mail.Composer)(nil)

// passwordHasher is the part of the one shared *PasswordHasher the Service
// hashes, verifies and checks password reuse with. Account recovery tests
// substitute password work failures through it.
type passwordHasher interface {
	Hash(context.Context, string) (string, error)
	ReuseCheck(string) store.PasswordReuseCheck
	Verify(context.Context, string, string) error
	VerifyDummy(context.Context, string) error
	NeedsRehash(string) bool
	maximumPasswordLength() int
}

var _ passwordHasher = (*PasswordHasher)(nil)

// Service holds every identity use case. Sign-in, Sessions, Desktop
// authorization, reauthentication, registration and compatibility, two-step
// verification and the MFA key ring, account tokens and email changes, and
// personal access token administration live on the Service itself: its fields
// are the Store contracts those use cases call and one of each collaborator.
// The remaining concerns keep their own focused implementation, embedded here
// so the Service's method set is their exported methods.
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

	// accessPolicy is the one *currentAuthenticationAccessPolicy every flow
	// shares; dpop is the one DPoP nonce and replay guard, shared with
	// Desktop authorization.
	accessPolicy authenticationAccessPolicy
	dpop         *dpopSecurity
	cache        AuthenticationCache
	capabilities AccessPolicyCapabilitySource
	attempts     *AttemptAccounting
	// hasher is the one shared *PasswordHasher; it is nil when composition
	// supplies none, and construction then refuses.
	hasher     passwordHasher
	authorizer resourceAuthorizer
	auditor    access.MutationAuditor
	// securityAudit is nil when composition supplies no Auditor; local
	// sign-in, the Desktop handoffs, two-step verification transitions and
	// personal access token changes then fail closed with the audit
	// unavailable.
	securityAudit securityAuditor
	mail          mailComposer
	effects       SecurityEffects
	// mfa is the TOTP and recovery-code mechanics, which own secret sealing.
	mfa         *mfaMechanics
	operational OperationalRecorder
	diagnostics *logging.Logger
	// recoveryDiagnostics logs account recovery failures hidden from callers.
	recoveryDiagnostics *logging.Logger

	// desktopBuilds is the immutable verified Desktop build catalog;
	// skipDesktopBuildChecks admits any well-formed build in development.
	desktopBuilds          *desktopBuildCatalog
	skipDesktopBuildChecks bool

	// mfaRekeyEnabled is set only when the catalog has Job and MFA stores;
	// without it the MFA key ring use cases are unavailable. mfaRekeyWake
	// wakes the Job engine after a rekey is queued.
	mfaRekeyEnabled bool
	mfaRekeyWake    func()

	nodeID                    string
	publicURL                 string
	sessionPolicy             SessionPolicy
	loginRateLimit            LoginRateLimitPolicy
	personalAccessTokenPolicy PersonalAccessTokenPolicy
	accountRecoveryPolicy     AccountRecoveryPolicy
	desktopPolicy             DesktopAuthorizationPolicy
	recentAuthenticationTTL   time.Duration
	newCredential             func() string
	now                       func() time.Time
	// wait applies a progressive login delay; tests replace it.
	wait func(context.Context, time.Duration) error

	*externalAuthenticationService
	*authenticationMethodService
	*accountStateService
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
	service := &Service{
		desktopCompatibilityPolicies: deps.Store.DesktopCompatibilityPolicy(), institutions: deps.Store.Institution(),
		authorizer: deps.Authorizer, auditor: deps.Auditor.MutationAudit(), skipDesktopBuildChecks: deps.SkipDesktopCompatibility,
		recentAuthenticationTTL: deps.RecentAuthenticationTTL, now: time.Now,
	}
	if err = service.requireDesktopCompatibility(deps.DesktopBuildCatalog); err != nil {
		return nil, err
	}
	authenticationAccess, err := service.constructSessionAuthentication(deps, mfa)
	if err != nil {
		return nil, err
	}
	if err := service.constructAccountServices(deps); err != nil {
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

// constructSessionAuthentication builds DPoP and the authentication access
// policy, and sets and checks the Service's two-step verification, sign-in,
// Session and Desktop authorization and reauthentication collaborators.
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
	s.users, s.passwords = deps.Store.User(), deps.Store.PasswordCredential()
	s.sessions, s.sessionCredentials = deps.Store.Session(), deps.Store.SessionCredential()
	s.desktopRegistrations, s.personalAccessTokens = deps.Store.DesktopRegistration(), deps.Store.PersonalAccessToken()
	s.mfaCredentials, s.mfa = deps.Store.MFA(), mfa
	s.accessPolicy, s.dpop, s.cache, s.attempts = authenticationAccess, dpop, deps.Cache, deps.Attempts
	if deps.Hasher != nil {
		s.hasher = deps.Hasher
	}
	s.mail, s.effects, s.capabilities = deps.Mail, deps.Effects, deps.Capabilities
	if deps.Auditor != nil {
		s.securityAudit = deps.Auditor
	}
	s.operational, s.diagnostics = deps.Operational, deps.AuthenticationDiagnostics
	s.sessionPolicy, s.loginRateLimit, s.personalAccessTokenPolicy = deps.Sessions, deps.LoginRateLimit, deps.PersonalAccessToken
	s.newCredential, s.wait = model.NewCredentialToken, waitContext
	if err = s.requireMFA(); err != nil {
		return nil, err
	}
	if err = s.requireSessionAuthentication(); err != nil {
		return nil, err
	}
	s.browserAuthentications = deps.Store.BrowserAuthentication()
	s.desktopPolicy = DesktopAuthorizationPolicy{Issuer: deps.PublicURL, AllowLoopbackHTTPDevelopment: deps.LoopbackHTTPDevelopment}
	if err = s.requireDesktopAuthorization(); err != nil {
		return nil, err
	}
	if err = s.requireDesktopReauthentication(); err != nil {
		return nil, err
	}
	return authenticationAccess, nil
}

// constructAccountServices sets and checks the account token and personal
// access token administration collaborators and checks the Desktop
// Registration collaborators.
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
	externalAuthentication, err := newExternalAuthenticationService(externalAuthenticationServiceDependencies{Registry: deps.Registry, LoginStates: deps.Store.ExternalLoginState(), Institutions: deps.Store.Institution(), Identities: deps.Store.ExternalIdentity(), Sessions: deps.Store.Session(), AccessPolicy: authenticationAccess, Attempts: deps.Attempts, Authentication: s, Invalidator: deps.Invalidator, Audit: deps.Auditor, MutationAudit: deps.Auditor.MutationAudit(), Capabilities: deps.Capabilities, InvitationAcceptor: deps.Invitations, DesktopAuthorization: s, DesktopReauthentication: s, Policy: externalPolicy, RecentAuthenticationTTL: deps.RecentAuthenticationTTL, Diagnostics: deps.AuthenticationDiagnostics, NewCredential: model.NewCredentialToken, Now: time.Now})
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

// constructAdministration builds account state and bootstrap and, when the
// catalog has Jobs and MFA credentials, enables the MFA key ring.
func (s *Service) constructAdministration(deps Dependencies) error {
	s.accountStateService = newAccountStateService(accountStateServiceDependencies{Users: deps.Store.User(), Authorization: deps.Authorizer, Capabilities: deps.Capabilities, Audit: deps.Auditor.MutationAudit(), Mail: deps.Mail, Effects: accountStateRealtimeEffects{effects: deps.Effects}, Now: time.Now})
	s.bootstrapService = newBootstrapService(bootstrapServiceDependencies{Installations: deps.Store.Installation(), Hasher: deps.Hasher, Attempts: deps.Attempts, RateLimit: deps.LoginRateLimit, Protection: deps.BootstrapProtection, NodeID: deps.NodeID, Now: time.Now, RecoveryPolicy: administratorRecoveryPolicy{capabilities: deps.Capabilities, mfaEnabled: deps.MFA.Enabled}})
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

// ValidRekeyKeyID reports whether value is a key ring key identifier: exactly
// 32 lowercase hexadecimal characters. Mail and MFA rekey commands share it.
func ValidRekeyKeyID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
