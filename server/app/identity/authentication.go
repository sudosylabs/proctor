// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authentication.go,
// server/channels/app/login.go, and server/channels/app/session.go. Proctor
// keeps the single application authentication flow, generic login failures,
// server-side revocable sessions, activity debouncing, and boundary-safe
// errors while using split hashed access/refresh credentials and rotation.

package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const activityCachePrefix = "authentication/activity/"

// SessionPolicy is the immutable session-lifetime policy consumed by
// authentication. Composition translates deployment configuration into this
// value so authentication does not depend on config.Config.
type SessionPolicy struct {
	AccessTTL              time.Duration
	RefreshTTL             time.Duration
	IdleTTL                time.Duration
	AbsoluteTTL            time.Duration
	ActivityUpdateInterval time.Duration
	MaximumPerUser         int
	// RetiredRetention is how long maintenance keeps a revoked or expired
	// Session, or a superseded credential, before deleting it.
	RetiredRetention time.Duration
}

// LoginRateLimitPolicy bounds attempts in fixed windows by identity and
// source. The account-failure fields apply only to local login: once one
// account has AccountFailuresBeforeDelay failed password verifications in the
// window, from any source and through any of its identifiers, each further
// attempt against it waits a delay that doubles per failure up to
// MaximumAccountFailureDelay. The delay never refuses a correct password, so
// failures sent by someone else cannot lock the account out.
type LoginRateLimitPolicy struct {
	Window                     time.Duration
	MaximumAttempts            int
	MaximumSourceAttempts      int
	AccountFailuresBeforeDelay int
	MaximumAccountFailureDelay time.Duration
}

// loginFailureDelayBase is the first delay applied once an account reaches
// its failure threshold; each further failure doubles it.
const loginFailureDelayBase = 250 * time.Millisecond

// loginFailureDelay returns the wait applied before verifying a password for
// an account that already has failures recorded in the current window.
func loginFailureDelay(failures int64, threshold int, maximum time.Duration) time.Duration {
	if threshold <= 0 || maximum <= 0 || failures < int64(threshold) {
		return 0
	}
	delay := loginFailureDelayBase
	for steps := failures - int64(threshold); steps > 0 && delay < maximum; steps-- {
		delay *= 2
	}
	return min(delay, maximum)
}

// waitContext waits for delay or until ctx ends.
func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// PersonalAccessTokenPolicy controls PAT lifetime bounds, per-user limits, and
// last-used write debouncing during bearer resolution.
type PersonalAccessTokenPolicy struct {
	MinimumLifetime        time.Duration
	MaximumLifetime        time.Duration
	LastUsedUpdateInterval time.Duration
	MaximumPerUser         int
}

// LoginCommand is the local-password login use-case input.
type LoginCommand struct {
	LoginID    string
	Password   string
	ClientType model.SessionClientType
	DeviceID   string
	DeviceName string
	MFACode    string
	Source     string
}

// LoginResult is the transport-neutral successful login outcome.
type LoginResult struct {
	User    *model.User
	Session *model.Session
	Tokens  *model.AuthenticationTokens
}

type localAuthenticationProof struct {
	AuthenticationGeneration int64
	MFARecoveryRequired      bool
	PasswordProof            store.PasswordCredentialProof
	User                     *model.User
	AuthenticationStrength   model.AuthenticationStrength
	AuthenticatedAt          int64
	MFACompletedAt           int64
	receipt                  authenticationAttemptReceipt
	failures                 authenticationFailureSubject
}

// CreateLocalUserCommand creates a local user with a password credential.
type CreateLocalUserCommand struct {
	User     *model.User
	Password string
}

// AuthenticationCache is the disposable cache authentication, DPoP and attempt
// accounting share. Composition adapts the platform cache to it and translates
// its outcomes into ErrAuthenticationCacheMiss and ErrAuthenticationCacheNotStored.
type AuthenticationCache interface {
	Get(context.Context, string) ([]byte, error)
	SetAlways(context.Context, string, []byte, time.Duration) error
	SetIfAbsent(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
	Add(context.Context, string, int64, time.Duration) (int64, error)
}

// beginLocalLogin, completeLocalLogin and recordLocalLoginFailure durably
// record every admitted local-password login outcome. Attempts rejected
// before rate-limit admission are not audited so an unauthenticated flood
// cannot grow the audit table without bound.
func (s *Service) beginLocalLogin(
	ctx context.Context,
	invocation invocation.Invocation,
	userID model.UserID,
	clientType model.SessionClientType,
) (model.AuditEventID, error) {
	if s.securityAudit == nil {
		return "", access.AuditUnavailable(errors.New("local login audit service is unavailable"))
	}

	event, err := s.securityAudit.BeginLocalLogin(ctx, userID, clientType, invocation.RequestMetadata())
	if err != nil {
		return "", err
	}

	return event.ID, nil
}

func (s *Service) completeLocalLogin(
	ctx context.Context,
	auditID model.AuditEventID,
	status model.AuditStatus,
	errorCode string,
	result any,
) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("local login audit service is unavailable"))
	}

	_, err := s.securityAudit.CompleteCriticalAction(ctx, auditID, status, errorCode, result)
	return err
}

func (s *Service) recordLocalLoginFailure(
	ctx context.Context,
	invocation invocation.Invocation,
	target model.UserID,
	clientType model.SessionClientType,
	errorCode string,
) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("local login audit service is unavailable"))
	}
	return s.securityAudit.RecordLocalLoginFailure(ctx, target, clientType, invocation.RequestMetadata(), errorCode)
}

type resolvedAuthentication struct {
	Credential *model.SessionCredential
	Session    *model.Session
	User       *model.User
}

// validatePrincipal revalidates the authoritative session and user state for
// a previously established session principal. Long-lived transports reach it
// through ValidateWebSocketPrincipal before continuing to trust the principal.
// ValidatePrincipal revalidates the authoritative Session and User state
// behind a principal that was established earlier. WebSocket admission and
// onboarding import call it before they trust that principal again; it grants
// nothing a fresh authentication would not.
func (s *Service) ValidatePrincipal(ctx context.Context, principal model.Principal) error {
	return s.validatePrincipal(ctx, principal)
}

func (s *Service) validatePrincipal(ctx context.Context, principal model.Principal) error {
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return access.InvalidTokenAppError()
	}

	session, err := s.sessions.Get(ctx, principal.SessionID)
	if err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return authenticationUnavailable(err)
	}

	if session.UserID != principal.UserID {
		return access.InvalidTokenAppError()
	}

	recovery, recoveryErr := s.mfaRecoveryState(ctx, principal.UserID)
	if recoveryErr != nil {
		return recoveryErr
	}

	if recovery.ReenrollmentRequired ||
		principal.AuthenticationGeneration != recovery.Generation ||
		session.AuthenticationGeneration != recovery.Generation ||
		session.MFARecoveryRequired ||
		session.MFAEnrollmentRequired {
		return access.InvalidTokenAppError()
	}

	session, err = s.enforceSessionExpiry(ctx, session, s.now().UTC())
	if err != nil {
		return err
	}

	if principal.HasRegisteredDesktopKey() {
		if session.DesktopRegistrationID != principal.DesktopRegistrationID ||
			session.DPoPKeyThumbprint != principal.DPoPKeyThumbprint ||
			s.desktopRegistrations == nil {
			return access.InvalidTokenAppError()
		}

		registration, registrationErr := s.desktopRegistrations.Get(ctx, principal.DesktopRegistrationID)
		if registrationErr != nil ||
			!registration.IsActive() ||
			registration.UserID != principal.UserID ||
			registration.KeyThumbprint != principal.DPoPKeyThumbprint {
			if registrationErr == nil || store.IsNotFound(registrationErr) {
				return access.InvalidTokenAppError()
			}
			return authenticationUnavailable(registrationErr)
		}
	}

	user, err := s.users.Get(ctx, principal.UserID)
	if err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return authenticationUnavailable(err)
	}

	if !user.IsActive() {
		return access.InvalidTokenAppError()
	}

	return nil
}

// requireSessionAuthentication refuses a Service whose sign-in and Session
// collaborators are incomplete. New calls it once, after setting them.
func (s *Service) requireSessionAuthentication() error {
	if s.personalAccessTokens == nil {
		return errors.New("personal access token store is required")
	}
	if s.diagnostics == nil {
		return errors.New("personal access token diagnostics are required")
	}
	if s.users == nil {
		return errors.New("authentication user store is required")
	}
	if s.passwords == nil {
		return errors.New("authentication password store is required")
	}
	if s.sessions == nil {
		return errors.New("authentication session store is required")
	}
	if s.sessionCredentials == nil {
		return errors.New("authentication session credential store is required")
	}
	if s.accessPolicy == nil {
		return errors.New("authentication access policy is required")
	}
	if s.cache == nil {
		return errors.New("authentication cache is required")
	}
	if s.attempts == nil {
		return errors.New("authentication attempt accounting is required")
	}
	if s.effects == nil {
		return errors.New("authentication security effects are required")
	}
	if s.auditor == nil {
		return errors.New("authentication audit is required")
	}
	if s.hasher == nil {
		return errors.New("password hasher is required")
	}
	if s.mfa == nil {
		return errors.New("authentication MFA verifier is required")
	}
	if s.newCredential == nil {
		return errors.New("authentication credential generator is required")
	}
	if s.now == nil {
		return errors.New("authentication clock is required")
	}
	if s.desktopRegistrations == nil || s.dpop == nil {
		return errors.New("authentication Desktop dependencies are required")
	}

	return nil
}

// CreateLocalUser creates a local User with a hashed password credential, its
// initial settings and its default profile picture Job.
func (s *Service) CreateLocalUser(
	ctx context.Context,
	command CreateLocalUserCommand,
) (*model.User, error) {
	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, PasswordHashError(err, "authentication.internal")
	}

	created, job, err := jobhandler.PrepareUserDefaultProfilePictureJob(command.User, s.now())
	if err != nil {
		return nil, apperror.New("authentication.user.invalid").Wrap(err)
	}

	credential := &model.PasswordCredential{UserID: created.ID, PasswordHash: hash}
	credential.PrepareCreate(model.NewPasswordCredentialID(), created.CreatedAt)
	settings, err := user.PrepareInitialSettingsDocument(created)
	if err != nil {
		return nil, apperror.New("authentication.user.invalid").Wrap(err)
	}

	result, err := s.users.Create(ctx, &store.UserCreation{
		User:                     created,
		Settings:                 settings,
		PasswordCredential:       credential,
		DefaultProfilePictureJob: job,
	})
	if err != nil {
		var conflict *store.ErrConflict
		if errors.As(err, &conflict) {
			return nil, apperror.New("authentication.user.conflict").Wrap(err)
		}

		return nil, authenticationUnavailable(err)
	}

	return result.User, nil
}

// Login proves a local password, and a second factor when one is required, and
// issues a Session. Every admitted attempt is audited; Desktop sign-in uses
// Desktop authorization instead.
func (s *Service) Login(
	ctx context.Context,
	invocation invocation.Invocation,
	command LoginCommand,
) (_ *LoginResult, resultErr error) {
	defer func() { recordOperational(s.operational, "authentication", "login", resultErr) }()
	if command.ClientType == model.SessionClientDesktop || !command.ClientType.IsValid() {
		return nil, apperror.New("authentication.client_type.invalid").WithField("field", "client_type")
	}

	proof, subject, err := s.proveLocal(ctx, command)
	if err != nil {
		if !subject.admitted {
			return nil, err
		}

		if auditErr := s.recordLocalLoginFailure(
			ctx,
			invocation,
			subject.userID,
			command.ClientType,
			appErrorCode(err),
		); auditErr != nil {
			return nil, auditErr
		}

		return nil, err
	}

	auditID, err := s.beginLocalLogin(ctx, invocation, proof.User.ID, command.ClientType)
	if err != nil {
		return nil, err
	}

	savedSession, tokens, sessionErr := s.createSession(
		ctx,
		sessionIssuance{
			User:                     proof.User,
			ClientType:               command.ClientType,
			PasswordProof:            proof.PasswordProof,
			AuthenticationGeneration: proof.AuthenticationGeneration,
			MFARecoveryRequired:      proof.MFARecoveryRequired,
			DeviceID:                 command.DeviceID,
			DeviceName:               command.DeviceName,
			AuthenticationMethod:     "password",
			AuthenticationStrength:   proof.AuthenticationStrength,
			AuthenticatedAt:          proof.AuthenticatedAt,
			MFACompletedAt:           proof.MFACompletedAt,
			AuditEventID:             auditID,
		},
	)
	if sessionErr != nil {
		if auditErr := s.completeLocalLogin(
			ctx,
			auditID,
			model.AuditStatusFail,
			appErrorCode(sessionErr),
			nil,
		); auditErr != nil {
			return nil, auditErr
		}

		return nil, sessionErr
	}

	s.resetLocalAuthenticationAttempts(ctx, proof)
	return &LoginResult{User: proof.User, Session: savedSession, Tokens: tokens}, nil
}

// localAuthenticationSubject reports how far a failed local authentication
// progressed: whether it was admitted by rate limiting and, when known, which
// account it targeted.
type localAuthenticationSubject struct {
	admitted bool
	userID   model.UserID
}

// authenticateLocal proves a local identity without deciding which
// purpose-bound credential or Session will consume that proof.
func (s *Service) authenticateLocal(
	ctx context.Context,
	command LoginCommand,
) (*localAuthenticationProof, error) {
	proof, _, err := s.proveLocal(ctx, command)
	return proof, err
}

func (s *Service) proveLocal(
	ctx context.Context,
	command LoginCommand,
) (*localAuthenticationProof, localAuthenticationSubject, error) {
	var subject localAuthenticationSubject
	proof, err := s.proveLocalSubject(ctx, command, &subject)
	return proof, subject, err
}

func (s *Service) proveLocalSubject(
	ctx context.Context,
	command LoginCommand,
	subject *localAuthenticationSubject,
) (*localAuthenticationProof, error) {
	receipt, err := s.checkLoginRateLimit(ctx, command.LoginID, command.Source)
	if err != nil {
		return nil, err
	}

	subject.admitted = true
	if err = s.admitLocalLogin(ctx, command); err != nil {
		return nil, err
	}

	user, err := s.findLoginUser(ctx, command.LoginID)
	if err == nil && user != nil {
		subject.userID = user.ID
	}

	if err != nil && !store.IsNotFound(err) {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		return nil, authenticationUnavailable(err)
	}

	if err != nil {
		user = nil
	}

	failures, err := s.delayAfterLoginFailures(ctx, command.LoginID, user)
	if err != nil {
		return nil, err
	}

	if user == nil {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		return nil, s.recordLoginFailure(ctx, failures)
	}

	credential, err := s.passwords.GetByUser(ctx, user.ID)
	if err != nil {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		if !store.IsNotFound(err) {
			return nil, authenticationUnavailable(err)
		}

		return nil, s.recordLoginFailure(ctx, failures)
	}

	// Recovery state exists only for active Users. Do equivalent password work
	// before rejecting disabled accounts, without consulting their MFA state.
	if !user.IsActive() {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return nil, passwordWorkError(dummyErr, "authentication.internal")
		}
		return nil, s.recordLoginFailure(ctx, failures)
	}

	recovery, err := s.mfaRecoveryState(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	if verifyErr := s.hasher.Verify(ctx, credential.PasswordHash, command.Password); verifyErr != nil {
		if !errors.Is(verifyErr, ErrPasswordMismatch) {
			return nil, passwordWorkError(verifyErr, "authentication.internal")
		}
		return nil, s.recordLoginFailure(ctx, failures)
	}

	if !user.IsActive() {
		return nil, invalidCredentialsAppError()
	}

	// Prepare any hash upgrade before consuming a one-time second factor.
	// The guarded credential write still follows successful MFA below.
	var rehashed string
	if s.hasher.NeedsRehash(credential.PasswordHash) {
		var hashErr error
		rehashed, hashErr = s.hasher.Hash(ctx, command.Password)
		if hashErr != nil {
			return nil, passwordWorkError(hashErr, "authentication.internal")
		}
	}

	now := s.now()
	authenticationStrength, mfaCompletedAt, err := s.verifyLoginMFA(
		ctx, user.ID.String(), command.MFACode, now,
	)
	if err != nil {
		return nil, err
	}
	if err = s.applyLocalRehash(ctx, credential, rehashed); err != nil {
		return nil, err
	}

	return &localAuthenticationProof{
		User:                     user,
		AuthenticationStrength:   authenticationStrength,
		AuthenticationGeneration: recovery.Generation,
		MFARecoveryRequired:      recovery.ReenrollmentRequired,
		PasswordProof:            store.PasswordCredentialProof{ID: credential.ID, Revision: credential.Revision},
		AuthenticatedAt:          now.UnixMilli(),
		MFACompletedAt:           mfaCompletedAt,
		receipt:                  receipt,
		failures:                 failures,
	}, nil
}

// admitLocalLogin rejects a disallowed or malformed local login after equivalent dummy password work.
func (s *Service) admitLocalLogin(ctx context.Context, command LoginCommand) error {
	localLoginAllowed, err := s.accessPolicy.AllowsLocalLogin(ctx)
	if err != nil {
		return authenticationUnavailable(err)
	}
	if !localLoginAllowed {
		if dummyErr := s.hasher.VerifyDummy(ctx, command.Password); dummyErr != nil {
			return passwordWorkError(dummyErr, "authentication.internal")
		}
		return invalidCredentialsAppError()
	}
	if command.LoginID == "" ||
		len(command.LoginID) > model.UserEmailMaxLength ||
		len(command.Password) > s.hasher.maximumPasswordLength() {
		if dummyErr := s.hasher.VerifyDummy(ctx, "invalid-password-length"); dummyErr != nil {
			return passwordWorkError(dummyErr, "authentication.internal")
		}
		return invalidCredentialsAppError()
	}
	return nil
}

// applyLocalRehash stores a prepared password hash upgrade guarded by the verified credential revision.
func (s *Service) applyLocalRehash(ctx context.Context, credential *model.PasswordCredential, rehashed string) error {
	if rehashed != "" {
		if rehashErr := s.passwords.Rehash(ctx, &store.PasswordCredentialRehash{
			ID:               credential.ID,
			UserID:           credential.UserID,
			ExpectedHash:     credential.PasswordHash,
			ExpectedRevision: credential.Revision,
			PasswordHash:     rehashed,
		}); rehashErr != nil {
			if errors.Is(rehashErr, store.ErrPasswordCredentialChanged) {
				return invalidCredentialsAppError()
			}
			return authenticationUnavailable(rehashErr)
		}
	}

	return nil
}

// delayAfterLoginFailures waits the progressive delay earned by earlier failed
// verifications and returns the failure subject for this attempt. The subject
// is the resolved account, so every identifier of one account shares one
// budget; an identifier that resolves to no account is its own subject and
// waits by the same rule, so the delay does not reveal whether it exists. The
// wait happens before password work, so it holds no hashing permit.
func (s *Service) delayAfterLoginFailures(
	ctx context.Context,
	loginID string,
	user *model.User,
) (authenticationFailureSubject, error) {
	identity := "identifier:" + normalizeLoginIdentity(loginID)
	if user != nil {
		identity = "account:" + user.ID.String()
	}

	subject, err := s.attempts.failureSubject(authenticationAttemptPurposeLocalLogin, identity)
	if err != nil {
		return authenticationFailureSubject{}, RateLimitUnavailableAppError(err)
	}

	failures, err := s.attempts.failures(ctx, subject, s.loginRateLimit.Window)
	if err != nil {
		return authenticationFailureSubject{}, RateLimitUnavailableAppError(err)
	}

	delay := loginFailureDelay(
		failures,
		s.loginRateLimit.AccountFailuresBeforeDelay,
		s.loginRateLimit.MaximumAccountFailureDelay,
	)
	if delay > 0 {
		if err = s.wait(ctx, delay); err != nil {
			return authenticationFailureSubject{}, authenticationUnavailable(err)
		}
	}

	return subject, nil
}

// recordLoginFailure counts one failed verification and returns the public
// credential failure. Losing the count only shortens a later delay, so a cache
// failure is diagnostic rather than a different answer.
func (s *Service) recordLoginFailure(
	ctx context.Context,
	subject authenticationFailureSubject,
) error {
	if err := s.attempts.recordFailure(ctx, subject, s.loginRateLimit.Window); err != nil {
		s.warn(ctx, "login failure count failed", err)
	}
	return invalidCredentialsAppError()
}

func (s *Service) resetLocalAuthenticationAttempts(ctx context.Context, proof *localAuthenticationProof) {
	if err := errors.Join(
		s.attempts.reset(ctx, proof.receipt, authenticationAttemptDimensionIdentitySource),
		s.attempts.clearFailures(ctx, proof.failures),
	); err != nil {
		s.warn(ctx, "login rate-limit reset failed", err)
	}
}

func (s *Service) createSession(
	ctx context.Context,
	command sessionIssuance,
) (*model.Session, *model.AuthenticationTokens, error) {
	user := command.User
	clientType := command.ClientType
	strength := command.AuthenticationStrength
	authenticatedAt := command.AuthenticatedAt
	mfaCompletedAt := command.MFACompletedAt
	if user == nil ||
		!user.IsActive() ||
		!clientType.IsValid() ||
		!strength.IsValid() ||
		authenticatedAt <= 0 {
		return nil, nil, apperror.New("authentication.session.invalid")
	}

	now := model.TimeUTC(s.now())
	nowMillis := now.UnixMilli()
	if authenticatedAt > nowMillis {
		authenticatedAt = nowMillis
	}

	if strength == model.AuthenticationMultiFactor {
		if mfaCompletedAt < authenticatedAt || mfaCompletedAt > nowMillis {
			mfaCompletedAt = authenticatedAt
		}
	} else {
		mfaCompletedAt = 0
	}

	enrollmentRequired, err := s.mfaEnrollmentRequired(ctx, user.ID, clientType, strength)
	if err != nil {
		return nil, nil, err
	}

	settings := s.sessionPolicy
	lifetimes, err := s.sessionLifetimes(ctx)
	if err != nil {
		return nil, nil, err
	}

	absoluteExpiresAt := model.TimeUTC(now.Add(lifetimes.Absolute))
	accessExpiresAt := sessionDeadline(now, settings.AccessTTL, absoluteExpiresAt)
	refreshExpiresAt := sessionDeadline(now, settings.RefreshTTL, absoluteExpiresAt)
	session := &model.Session{
		UserID:                   user.ID,
		AuthenticationGeneration: command.AuthenticationGeneration,
		MFARecoveryRequired:      command.MFARecoveryRequired,
		MFAEnrollmentRequired:    enrollmentRequired,
		ClientType:               clientType,
		DeviceID:                 command.DeviceID,
		DeviceName:               command.DeviceName,
		AuthenticationMethod:     command.AuthenticationMethod,
		AuthenticationProviderID: command.AuthenticationProviderID,
		ExternalIdentityID:       command.ExternalIdentityID,
		AuthenticationStrength:   strength,
		AuthenticatedAt:          model.TimeFromMillis(authenticatedAt),
		MFACompletedAt:           model.OptionalTimeFromMillis(mfaCompletedAt),
		LastActivityAt:           now,
		IdleExpiresAt:            sessionDeadline(now, lifetimes.Idle, absoluteExpiresAt),
		ExpiresAt:                absoluteExpiresAt,
	}
	accessToken := s.newCredential()
	refreshToken := s.newCredential()
	savedSession, credentials, saveErr := s.sessions.Save(
		ctx,
		&store.SessionCreation{
			Session:              session,
			PasswordProof:        command.PasswordProof,
			ExternalLoginStateID: command.ExternalLoginStateID,
			Credentials: []*model.SessionCredential{
				{
					Kind:      model.SessionCredentialAccess,
					TokenHash: model.HashToken(accessToken),
					ExpiresAt: accessExpiresAt,
				},
				{
					Kind:      model.SessionCredentialRefresh,
					TokenHash: model.HashToken(refreshToken),
					ExpiresAt: refreshExpiresAt,
				},
			},
			MaximumActive: settings.MaximumPerUser,
			AuditEventID:  command.AuditEventID,
			AuditAt:       now,
		},
	)
	if saveErr != nil {
		return nil, nil, sessionCreationError(command, saveErr)
	}

	if !hasAccessCredential(credentials) {
		return nil, nil, authenticationUnavailable(
			errors.New("saved session has no access credential"),
		)
	}

	return savedSession, &model.AuthenticationTokens{
		TokenType:        "Bearer",
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpiresAt,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

// sessionLifetimes returns the idle and absolute lengths a Session gets now:
// the deployment lifetimes, shortened by the current Access Policy.
func (s *Service) sessionLifetimes(ctx context.Context) (sessionLifetimes, error) {
	lifetimes, err := s.accessPolicy.SessionLifetimes(ctx, s.sessionPolicy)
	if err != nil {
		return sessionLifetimes{}, authenticationUnavailable(err)
	}
	return lifetimes, nil
}

// mfaEnrollmentRequired decides whether a new single-factor Session of the
// User must be restricted to MFA enrollment because the Access Policy
// requires role holders to use two-step verification. Only a Web Session can carry
// the restriction; any other client is refused and sent to the browser.
func (s *Service) mfaEnrollmentRequired(
	ctx context.Context,
	userID model.UserID,
	clientType model.SessionClientType,
	strength model.AuthenticationStrength,
) (bool, error) {
	if strength == model.AuthenticationMultiFactor {
		return false, nil
	}

	required, err := s.accessPolicy.RequiresMFA(ctx, userID)
	if err != nil {
		return false, authenticationUnavailable(err)
	}
	if required && clientType != model.SessionClientWeb {
		return false, mfaEnrollmentRequiredAppError()
	}

	return required, nil
}

// sessionCreationError maps a Session persistence failure to the public authentication error.
func sessionCreationError(command sessionIssuance, saveErr error) error {
	if errors.Is(saveErr, store.ErrAuthenticationMethodDisabled) ||
		errors.Is(saveErr, store.ErrPasswordCredentialChanged) ||
		errors.Is(saveErr, store.ErrAuthenticationGenerationChanged) ||
		errors.Is(saveErr, store.ErrMFAReenrollmentRequired) {
		if command.AuthenticationProviderID != "" {
			return invalidExternalAuthenticationError()
		}
		return invalidCredentialsAppError()
	}

	var conflict *store.ErrConflict
	if errors.As(saveErr, &conflict) {
		switch conflict.Constraint {
		case "sessions_maximum_per_user":
			return apperror.New("authentication.sessions.maximum_reached")
		case "active_attempt_session_lock":
			return apperror.New("authentication.desktop_authorization.account_session_locked")
		}
	}

	return authenticationUnavailable(saveErr)
}

// hasAccessCredential reports whether the saved Session credentials include an access credential.
func hasAccessCredential(credentials []*model.SessionCredential) bool {
	for _, savedCredential := range credentials {
		if savedCredential.Kind == model.SessionCredentialAccess {
			return true
		}
	}
	return false
}

func sessionDeadline(now time.Time, lifetime time.Duration, absoluteExpiresAt time.Time) time.Time {
	expiresAt := model.TimeUTC(now.Add(lifetime))
	if expiresAt.After(absoluteExpiresAt) {
		return absoluteExpiresAt
	}
	return expiresAt
}

func (s *Service) checkLoginRateLimit(
	ctx context.Context,
	loginID string,
	source string,
) (authenticationAttemptReceipt, error) {
	settings := s.loginRateLimit
	receipt, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeLocalLogin,
		window:  settings.Window,
		limits: []AttemptLimit{
			{
				dimension: authenticationAttemptDimensionIdentitySource,
				maximum:   settings.MaximumAttempts,
				identity:  loginID,
				source:    source,
			},
			{
				dimension: authenticationAttemptDimensionSource,
				maximum:   settings.MaximumSourceAttempts,
				source:    source,
			},
		},
	})
	if err != nil {
		return authenticationAttemptReceipt{}, RateLimitUnavailableAppError(err)
	}
	if limited {
		return authenticationAttemptReceipt{}, apperror.New("authentication.rate_limited")
	}
	return receipt, nil
}

func (s *Service) findLoginUser(ctx context.Context, loginID string) (*model.User, error) {
	loginID = strings.ToLower(strings.TrimSpace(loginID))
	if strings.Contains(loginID, "@") {
		return s.users.GetByEmail(ctx, loginID)
	}
	return s.users.GetByUsername(ctx, loginID)
}

// AuthenticateAccess resolves a session access token into its Principal.
func (s *Service) AuthenticateAccess(
	ctx context.Context,
	rawToken string,
) (*model.Principal, error) {
	principal, err := s.authenticateAccess(ctx, rawToken)
	recordOperational(s.operational, "authentication", "access_token", err)
	return principal, err
}

// AuthenticateBearer accepts the two Authorization-header credential classes:
// a CLI session access credential or a personal access token. Cookie
// authentication remains session-only and therefore calls AuthenticateAccess.
func (s *Service) AuthenticateBearer(
	ctx context.Context,
	rawToken string,
) (*model.Principal, error) {
	principal, err := s.authenticateAccess(ctx, rawToken)
	if err == nil {
		recordOperational(s.operational, "authentication", "bearer", nil)
		return principal, nil
	}
	if failure, ok := apperror.As(err); !ok || failure.Code() != "authentication.token_invalid" {
		recordOperational(s.operational, "authentication", "bearer", err)
		return nil, err
	}
	principal, err = s.resolvePersonalAccessTokenBearer(ctx, rawToken, s.now())
	recordOperational(s.operational, "authentication", "bearer", err)
	return principal, err
}

func (s *Service) authenticateAccess(
	ctx context.Context,
	rawToken string,
) (*model.Principal, error) {
	if !validRawCredential(rawToken) {
		return nil, access.InvalidTokenAppError()
	}

	now := model.TimeUTC(s.now())
	tokenHash := model.HashToken(rawToken)
	// Resolve every new authentication decision from the authoritative Store.
	// Positive cache entries cannot preserve access after committed revocation,
	// credential rotation, or account disablement when invalidation is lost.
	credential, session, err := s.sessionCredentials.GetSessionByTokenHash(
		ctx, tokenHash, model.SessionCredentialAccess,
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}

	user, err := s.users.Get(ctx, session.UserID)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}

	resolved := &resolvedAuthentication{Credential: credential, Session: session, User: user}
	if !resolved.User.IsActive() || resolved.Credential.IsExpiredAt(now) {
		return nil, access.InvalidTokenAppError()
	}

	resolved.Session, err = s.enforceSessionExpiry(ctx, resolved.Session, now)
	if err != nil {
		return nil, err
	}
	if resolved.Session.ClientType == model.SessionClientDesktop {
		return nil, access.InvalidTokenAppError()
	}
	if err := s.updateActivity(ctx, resolved, now); err != nil {
		return nil, err
	}

	principal := &model.Principal{
		UserID:                   resolved.User.ID,
		AuthenticationGeneration: resolved.Session.AuthenticationGeneration,
		MFARecoveryRequired:      resolved.Session.MFARecoveryRequired,
		MFAEnrollmentRequired:    resolved.Session.MFAEnrollmentRequired,
		SessionID:                resolved.Session.ID,
		CredentialID:             model.PrincipalCredentialID(resolved.Credential.ID),
		CredentialType:           model.CredentialSessionAccess,
		AuthenticationMethod:     resolved.Session.AuthenticationMethod,
		AuthenticationProviderID: resolved.Session.AuthenticationProviderID,
		ExternalIdentityID:       resolved.Session.ExternalIdentityID,
		AuthenticationStrength:   resolved.Session.AuthenticationStrength,
		ClientType:               resolved.Session.ClientType,
		AuthenticatedAt:          resolved.Session.AuthenticatedAt,
		ReauthenticatedAt:        resolved.Session.ReauthenticatedAt,
		MFACompletedAt:           resolved.Session.MFACompletedAt,
	}
	if principal.ValidateMFARecovery() != nil {
		return nil, authenticationUnavailable(
			errors.New("resolved principal is invalid"),
		)
	}

	return principal, nil
}

func (s *Service) updateActivity(
	ctx context.Context,
	resolved *resolvedAuthentication,
	now time.Time,
) error {
	now = model.TimeUTC(now)
	settings := s.sessionPolicy
	if now.Sub(resolved.Session.LastActivityAt) < settings.ActivityUpdateInterval {
		return nil
	}

	key := activityCachePrefix + resolved.Session.ID.String()
	err := s.cache.SetIfAbsent(
		ctx,
		key,
		[]byte{1},
		settings.ActivityUpdateInterval,
	)
	if errors.Is(err, ErrAuthenticationCacheNotStored) {
		return nil
	}

	if err != nil {
		s.warn(ctx, "session activity debounce cache failed", err)
		return nil
	}

	lifetimes, err := s.sessionLifetimes(ctx)
	if err != nil {
		return err
	}

	idleExpiresAt := sessionDeadline(now, lifetimes.Idle, resolved.Session.ExpiresAt)
	if err := s.sessions.UpdateActivity(
		ctx,
		&store.SessionActivityUpdate{ID: resolved.Session.ID, LastActivityAt: now, IdleExpiresAt: idleExpiresAt},
	); err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return authenticationUnavailable(err)
	}

	resolved.Session.LastActivityAt = now
	resolved.Session.IdleExpiresAt = idleExpiresAt
	if resolved.Session.UpdatedAt.Before(now) {
		resolved.Session.UpdatedAt = now
	}

	return nil
}

// RefreshSession rotates a Session's access and refresh credentials for a
// valid refresh token, verifying the DPoP proof of a Desktop Session.
func (s *Service) RefreshSession(
	ctx context.Context,
	_ invocation.Invocation,
	command RefreshSessionCommand,
) (_ *model.Session, _ *model.AuthenticationTokens, resultErr error) {
	defer func() { recordOperational(s.operational, "authentication", "refresh", resultErr) }()
	rawRefreshToken := command.RefreshToken
	if !validRawCredential(rawRefreshToken) {
		return nil, nil, access.InvalidTokenAppError()
	}

	_, session, err := s.sessionCredentials.GetSessionByTokenHash(
		ctx,
		model.HashToken(rawRefreshToken),
		model.SessionCredentialRefresh,
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, nil, access.InvalidTokenAppError()
		}
		return nil, nil, authenticationUnavailable(err)
	}

	if session.ClientType == model.SessionClientDesktop {
		if command.DPoP == nil {
			return nil, nil, access.InvalidTokenAppError()
		}
		if err = s.validateDPoPRefresh(ctx, rawRefreshToken, command.DPoP.Proof, command.DPoP.Method, command.DPoP.Path); err != nil {
			return nil, nil, err
		}
	} else if command.DPoP != nil {
		return nil, nil, access.InvalidTokenAppError()
	}

	now := model.TimeUTC(s.now())
	mfaRequired := false
	if session.AuthenticationStrength != model.AuthenticationMultiFactor {
		// A User who became a role holder mid-Session, or whose Session predates the
		// policy, is restricted here; a Desktop Session is refused instead.
		mfaRequired, err = s.accessPolicy.RequiresMFA(ctx, session.UserID)
		if err != nil {
			return nil, nil, authenticationUnavailable(err)
		}
		if mfaRequired && session.ClientType != model.SessionClientWeb {
			return nil, nil, mfaEnrollmentRequiredAppError()
		}
	}

	settings := s.sessionPolicy
	lifetimes, err := s.sessionLifetimes(ctx)
	if err != nil {
		return nil, nil, err
	}

	accessToken := s.newCredential()
	refreshToken := s.newCredential()
	rotation, err := s.sessionCredentials.RotateRefresh(
		ctx,
		&store.SessionRefreshRotation{TokenHash: model.HashToken(rawRefreshToken), Access: &model.SessionCredential{
			TokenHash: model.HashToken(accessToken),
			ExpiresAt: model.TimeUTC(now.Add(settings.AccessTTL)),
		}, Refresh: &model.SessionCredential{
			TokenHash: model.HashToken(refreshToken),
			ExpiresAt: model.TimeUTC(now.Add(settings.RefreshTTL)),
		}, Now: now, IdleExpiresAt: model.TimeUTC(now.Add(lifetimes.Idle)), MFARequiredByPolicy: mfaRequired},
	)
	if err != nil {
		if errors.Is(err, store.ErrMFAEnrollmentRequired) {
			return nil, nil, mfaEnrollmentRequiredAppError()
		}

		var conflict *store.ErrConflict
		if store.IsNotFound(err) ||
			errors.As(err, &conflict) ||
			errors.Is(err, store.ErrAuthenticationGenerationChanged) ||
			errors.Is(err, store.ErrMFAReenrollmentRequired) {
			return nil, nil, access.InvalidTokenAppError()
		}

		return nil, nil, authenticationUnavailable(err)
	}

	if rotation.ReplayDetected || rotation.Expired {
		s.sessionsRevoked(
			ctx,
			rotation.Session.UserID,
			[]model.SessionID{rotation.Session.ID},
			rotation.RevokedAccessHashes,
		)
	} else {
		s.authenticationCacheInvalidated(
			ctx,
			rotation.Session.UserID.String(),
			rotation.RevokedAccessHashes,
		)
	}

	if rotation.ReplayDetected || rotation.Expired {
		return nil, nil, access.InvalidTokenAppError()
	}

	user, err := s.users.Get(ctx, rotation.Session.UserID)
	if err != nil || !user.IsActive() {
		if err == nil {
			var revokedAccessHashes []string
			revokedAccessHashes, err = s.sessions.Withdraw(
				ctx,
				&store.SessionWithdrawal{
					ID:        rotation.Session.ID,
					UserID:    rotation.Session.UserID,
					RevokedAt: now,
					Reason:    model.SessionRevocationInactiveUser,
				},
			)
			if err == nil {
				s.sessionsRevoked(
					ctx,
					rotation.Session.UserID,
					[]model.SessionID{rotation.Session.ID},
					revokedAccessHashes,
				)
			}
		}

		return nil, nil, access.InvalidTokenAppError()
	}

	return rotation.Session, &model.AuthenticationTokens{
		TokenType:        tokenTypeForSession(rotation.Session),
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  rotation.AccessCredential.ExpiresAt,
		RefreshExpiresAt: rotation.RefreshCredential.ExpiresAt,
	}, nil
}

func tokenTypeForSession(session *model.Session) string {
	if session != nil && session.ClientType == model.SessionClientDesktop {
		return "DPoP"
	}
	return "Bearer"
}

// Logout revokes the caller's current Session.
func (s *Service) Logout(ctx context.Context, invocation invocation.Invocation, _ LogoutCommand) (resultErr error) {
	defer func() { recordOperational(s.operational, "authentication", "logout", resultErr) }()
	principal := invocation.Principal()
	if principal.ValidateMFARecovery() != nil {
		return access.InvalidTokenAppError()
	}

	return s.revokeSession(
		ctx,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation:  "logout",
			Value:      map[string]any{"session_id": principal.SessionID.String()},
		},
		&store.SessionRevocation{
			SessionID: principal.SessionID,
			UserID:    principal.UserID,
			Reason:    model.SessionRevocationUserLogout,
		},
		authenticationUnavailable,
		s.now,
	)
}

func (s *Service) authenticationCacheInvalidated(
	ctx context.Context,
	userID string,
	hashes []string,
) {
	s.effects.AuthenticationCacheInvalidated(ctx, model.UserID(userID), hashes)
}

func (s *Service) sessionsRevoked(
	ctx context.Context,
	userID model.UserID,
	sessionIDs []model.SessionID,
	hashes []string,
) {
	s.effects.SessionsRevoked(ctx, userID, sessionIDs, hashes)
}

func (s *Service) enforceSessionExpiry(
	ctx context.Context,
	session *model.Session,
	now time.Time,
) (*model.Session, error) {
	now = model.TimeUTC(now)
	if session == nil {
		return nil, access.InvalidTokenAppError()
	}
	if !session.IsExpiredAt(now) {
		return session, nil
	}

	result, err := s.sessions.EnforceExpiry(
		ctx,
		&store.SessionExpiryEnforcement{ID: session.ID, UserID: session.UserID, At: now},
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}

	if result == nil || result.Session == nil {
		return nil, authenticationUnavailable(errors.New("session expiry result is incomplete"))
	}
	if !result.Expired {
		return result.Session, nil
	}

	s.sessionsRevoked(
		ctx,
		result.Session.UserID,
		[]model.SessionID{result.Session.ID},
		result.TokenHashes,
	)
	return nil, access.InvalidTokenAppError()
}

func (s *Service) warn(ctx context.Context, message string, err error) {
	if s.diagnostics == nil {
		return
	}
	s.diagnostics.WarnContext(ctx, message, logging.Err(err))
}

func validRawCredential(token string) bool {
	return model.IsValidCredentialToken(token)
}

func invalidCredentialsAppError() error {
	return apperror.New("authentication.credentials_invalid")
}

// mfaEnrollmentRequiredAppError refuses a sign-in or refresh that cannot
// continue until the User sets up two-step verification in the browser.
func mfaEnrollmentRequiredAppError() error {
	return apperror.New("authentication.mfa.enrollment_required")
}

func authenticationUnavailable(err error) error {
	return apperror.New("authentication.internal").Wrap(err)
}

// RateLimitUnavailableAppError reports that attempt accounting failed, so the
// attempt is refused rather than admitted unbounded.
func RateLimitUnavailableAppError(err error) error {
	return apperror.New("authentication.rate_limit_unavailable").Wrap(err)
}
