// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package identity owns who a caller is and how they prove it: local and
// external sign-in, Sessions and their assurance, two-step verification,
// personal access tokens, Desktop authorization, account recovery,
// authentication methods, account state, email addresses, and installation
// bootstrap.
//
// Service holds every one of these use cases as its own methods. Its fields
// are the Store contracts the use cases call and one of each collaborator:
// one authorizer, one security auditor beside the mutation auditor, one mail
// composer, one realtime.Publisher for the security effects that follow a
// commit, one password hasher and one attempt accounting. When one flow needs
// another, such as external sign-in issuing a Session or handing a proof to
// Desktop authorization, it calls the other method directly. New builds the
// Service once and refuses it when a collaborator is missing. Each concern keeps its methods in the file named
// for it.
//
// The mechanics the flows share stay inside the package behind them: the
// password hasher, attempt accounting, the authentication cache invalidator,
// DPoP, the access-policy view and the TOTP mechanics. The parent app package
// builds the hasher, the attempt accounting and the invalidator once with the
// exported constructors, and shares them with the onboarding area's
// Invitations and public registration and with its own test mail and realtime
// propagation.
//
// Start with identity.go for the Service and its collaborators, then open the
// file named for the concern you are changing.
package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime"
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
// Action on a concrete Resource immediately before the work it guards,
// enabling or disabling an account authorizes managing that User's account
// state, and personal access token creation asks which actions the caller may
// delegate.
type resourceAuthorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
	AuthorizeAccountStateManage(context.Context, invocation.Invocation, model.UserID) error
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
	Publisher    realtime.Publisher
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
// (the admitted attempt, its completion and a terminal failure), an external
// sign-in (the attempt before its Session is issued, its completion and a
// failed provider callback), the Desktop authorization and reauthentication
// handoffs, which act for a proved account at Institution scope, the two-step
// verification transitions, recorded for the acting User as their subject,
// and the personal access token drafts that the token aggregate completes.
// Mutations are audited through the Service's access.MutationAuditor.
type securityAuditor interface {
	BeginLocalLogin(context.Context, model.UserID, model.SessionClientType, model.RequestMetadata) (*model.AuditEvent, error)
	BeginAuthentication(
		context.Context,
		model.UserID,
		string,
		string,
		model.SessionClientType,
		model.RequestMetadata,
		model.InstitutionID,
	) (*model.AuditEvent, error)
	BeginAccountActionAtScope(
		context.Context,
		access.AccountAuditActor,
		model.Action,
		model.Resource,
		model.RoleScopeType,
		string,
		model.RequestMetadata,
		any,
	) (*model.AuditEvent, error)
	BeginCriticalActionForSubject(
		context.Context,
		model.Principal,
		model.UserID,
		model.Action,
		model.Resource,
		model.RequestMetadata,
		any,
		any,
	) (*model.AuditEvent, error)
	PrepareCriticalAction(
		context.Context,
		model.Principal,
		model.Action,
		model.Resource,
		model.RequestMetadata,
		any,
		any,
	) (*model.AuditEvent, error)
	CompleteCriticalAction(context.Context, model.AuditEventID, model.AuditStatus, string, any) (*model.AuditEvent, error)
	RecordLocalLoginFailure(context.Context, model.UserID, model.SessionClientType, model.RequestMetadata, string) error
	RecordExternalAuthenticationFailure(context.Context, string, string, model.RequestMetadata, model.InstitutionID, string) error
}

var _ securityAuditor = (*access.Auditor)(nil)

// mailComposer is the part of *mail.Composer the Service prepares frozen
// account-token mail and security notices with.
type mailComposer interface {
	Enabled() bool
	PrepareAccountStateChanged(mail.NoticePreparation, bool) (*store.PreparedMail, error)
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

// Service holds every identity use case: sign-in, Sessions, Desktop
// authorization, reauthentication, registration and compatibility, two-step
// verification and the MFA key ring, account tokens and email changes,
// personal access tokens, external authentication, authentication methods,
// account state and installation bootstrap. Its fields are the Store
// contracts those use cases call and one of each collaborator.
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
	// securityAudit is nil when composition supplies no Auditor; local and
	// external sign-in, the Desktop handoffs, two-step verification
	// transitions and personal access token changes then fail closed with the
	// audit unavailable.
	securityAudit securityAuditor
	mail          mailComposer
	publisher     realtime.Publisher
	// invalidator is the shared *CacheInvalidator; external reauthentication
	// drops the cached access credentials it replaces.
	invalidator realtime.AuthenticationInvalidator
	// mfa is the TOTP and recovery-code mechanics, which own secret sealing.
	mfa         *mfaMechanics
	operational OperationalRecorder
	diagnostics *logging.Logger
	// recoveryDiagnostics logs account recovery failures hidden from callers.
	recoveryDiagnostics *logging.Logger

	// externalProviders is the configured provider registry. invitations
	// admits an External Identity through its Invitation; composition binds
	// it to the onboarding area after the Service is built.
	externalProviders ExternalProviderSource
	invitations       InvitationAcceptor

	// desktopBuilds is the immutable verified Desktop build catalog;
	// skipDesktopBuildChecks admits any well-formed build in development.
	desktopBuilds          *desktopBuildCatalog
	skipDesktopBuildChecks bool

	// mfaRekeyEnabled is set only when the catalog has Job and MFA stores;
	// without it the MFA key ring use cases are unavailable. mfaRekeyWake
	// wakes the Job engine after a rekey is queued.
	mfaRekeyEnabled bool
	mfaRekeyWake    func()

	// bootstrapSecretDigest is the only form of the deployment bootstrap
	// secret the Service keeps; bootstrapAvailable is false when no secret is
	// configured. They and the bootstrap rate limit are fixed by New.
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

// New builds the Service and checks every collaborator its use cases need.
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

// constructSessionAuthentication builds DPoP and the authentication access
// policy, and sets and checks the Service's two-step verification, sign-in,
// Session and Desktop authorization and reauthentication collaborators.
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
	if deps.Hasher != nil {
		s.hasher = deps.Hasher
	}

	s.mail, s.publisher, s.capabilities = deps.Mail, deps.Publisher, deps.Capabilities
	if deps.Auditor != nil {
		s.securityAudit = deps.Auditor
	}

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

// constructExternalAuthentication sets and checks the external
// authentication and authentication method collaborators. The external
// policy's public URL, node and rate limit default to the Service's own.
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

// constructAdministration fixes the bootstrap secret digest and rate limit
// and, when the catalog has Jobs and MFA credentials, enables the MFA key
// ring.
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

// MaintenanceTasks returns the bounded periodic passes that expire and purge
// browser authentication transactions, external login states and personal
// access token preparations. Every node may run them.
func MaintenanceTasks(catalog store.Catalog) []job.PeriodicTask {
	return []job.PeriodicTask{
		{
			Name:     "browser-authentication-maintenance",
			Interval: browserAuthenticationMaintenanceInterval,
			Runner:   browserAuthenticationMaintenancePeriodicRunner{transactions: catalog.BrowserAuthentication()},
		},
		{
			Name:     "external-authentication-maintenance",
			Interval: externalAuthenticationMaintenanceInterval,
			Runner:   externalAuthenticationMaintenancePeriodicRunner{states: catalog.ExternalLoginState()},
		},
		{
			Name:     "personal-access-token-maintenance",
			Interval: personalAccessTokenMaintenanceInterval,
			Runner:   personalAccessTokenMaintenancePeriodicRunner{tokens: catalog.PersonalAccessToken()},
		},
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
