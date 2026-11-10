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
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type DesktopAuthorizationPolicy struct {
	Issuer                       string
	AllowLoopbackHTTPDevelopment bool
	TransactionLifetime          time.Duration
	CodeLifetime                 time.Duration
}

type desktopAuthorizationAttemptOperation uint8

const (
	desktopAuthorizationAttemptStart desktopAuthorizationAttemptOperation = iota + 1
	desktopAuthorizationAttemptExchange
	desktopAuthorizationAttemptReauthenticationStart
	desktopAuthorizationAttemptReauthenticationComplete
)

func (o desktopAuthorizationAttemptOperation) qualifier() (string, bool) {
	switch o {
	case desktopAuthorizationAttemptStart:
		return "start", true
	case desktopAuthorizationAttemptExchange:
		return "exchange", true
	case desktopAuthorizationAttemptReauthenticationStart:
		return "reauthentication_start", true
	case desktopAuthorizationAttemptReauthenticationComplete:
		return "reauthentication_complete", true
	default:
		return "", false
	}
}

// checkDesktopAuthorizationAttempt counts one public Desktop authorization or
// reauthentication attempt against its identity and source limits under the
// login rate-limit policy.
func (s *Service) checkDesktopAuthorizationAttempt(ctx context.Context, operation desktopAuthorizationAttemptOperation, identity, source string) error {
	qualifier, valid := operation.qualifier()
	if !valid || s.attempts == nil || s.loginRateLimit.Window <= 0 || s.loginRateLimit.MaximumAttempts <= 0 || s.loginRateLimit.MaximumSourceAttempts <= 0 {
		return RateLimitUnavailableAppError(errors.New("desktop authorization attempt accounting is unavailable"))
	}
	_, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeDesktopAuthorization, qualifier: qualifier, window: s.loginRateLimit.Window,
		limits: []AttemptLimit{
			{dimension: authenticationAttemptDimensionIdentity, maximum: s.loginRateLimit.MaximumAttempts, identity: identity},
			{dimension: authenticationAttemptDimensionSource, maximum: s.loginRateLimit.MaximumSourceAttempts, source: source},
		},
	})
	if err != nil {
		return RateLimitUnavailableAppError(err)
	}
	if limited {
		return apperror.New("authentication.rate_limited")
	}
	return nil
}

type StartDesktopAuthorizationCommand struct {
	CallbackURL      string
	State            string
	CodeChallenge    string
	DeviceID         string
	DeviceName       string
	PublicJWK        model.DesktopPublicJWK
	DesktopRelease   string
	DesktopBuildID   string
	Platform         model.DesktopPlatform
	Architecture     model.DesktopArchitecture
	RealtimeProtocol int
	Source           string
}

type DesktopAuthorizationStart struct {
	AuthorizationURL string
	ExpiresAt        int64
	DPoPNonce        string
}

type BindDesktopAuthorizationCommand struct{ Handle, BrowserProof, State string }
type DesktopAuthorizationBinding struct {
	Binding   string
	ExpiresAt int64
}

type DesktopAuthorizationAccount struct {
	ID          model.UserID
	Username    string
	DisplayName string
	// Email is the authenticated account's own address, shown so the person
	// can confirm which account the Desktop will use.
	Email string
}

type DesktopAuthorizationBrowserContext struct {
	State             model.BrowserAuthenticationState
	Account           *DesktopAuthorizationAccount
	LocalLoginEnabled bool
	ExternalProviders []model.ExternalAuthenticationProvider
	DeviceName        string
	// RequestedAt is when Desktop started this authorization request.
	RequestedAt int64
	ExpiresAt   int64
}

type AuthenticateDesktopAuthorizationCommand struct{ Binding string }
type AuthenticateDesktopAuthorizationLocallyCommand struct {
	Binding  string
	LoginID  string
	Password string
	MFACode  string
	Source   string
}
type ApproveDesktopAuthorizationCommand struct{ Binding, State string }
type DesktopAuthorizationApproval struct {
	RedirectURL string
	ExpiresAt   int64
}
type ExchangeDesktopAuthorizationCommand struct {
	Code, State, CodeVerifier, Source string
	DPoPProof                         string
	PublicJWK                         model.DesktopPublicJWK
	DesktopRelease, DesktopBuildID    string
	Platform                          model.DesktopPlatform
	Architecture                      model.DesktopArchitecture
	RealtimeProtocol                  int
}
type DesktopAuthorizationExchangeResult struct {
	Session      *model.Session
	Registration *model.DesktopRegistration
	Tokens       *model.AuthenticationTokens
	DPoPNonce    string
}

// requireDesktopAuthorization defaults the Desktop authorization lifetimes and
// refuses a Service whose Desktop authorization collaborators or policy are
// incomplete. New calls it once, after sign-in is set up.
func (s *Service) requireDesktopAuthorization() error {
	if s.browserAuthentications == nil || s.institutions == nil {
		return errors.New("desktop authorization persistence is required")
	}
	if s.accessPolicy == nil || s.capabilities == nil {
		return errors.New("desktop authorization access policy is required")
	}
	if s.newCredential == nil || s.now == nil {
		return errors.New("desktop authorization security dependencies are required")
	}
	if s.desktopPolicy.TransactionLifetime == 0 {
		s.desktopPolicy.TransactionLifetime = model.BrowserAuthenticationTransactionLifetime
	}
	if s.desktopPolicy.CodeLifetime == 0 {
		s.desktopPolicy.CodeLifetime = model.DesktopAuthorizationCodeLifetime
	}
	if s.desktopPolicy.TransactionLifetime <= 0 ||
		s.desktopPolicy.TransactionLifetime > model.BrowserAuthenticationTransactionLifetime ||
		s.desktopPolicy.CodeLifetime <= 0 || s.desktopPolicy.CodeLifetime > model.DesktopAuthorizationCodeLifetime {
		return errors.New("desktop authorization lifetime policy is invalid")
	}
	if err := model.ValidateBrowserAuthenticationIssuer(s.desktopPolicy.Issuer, s.desktopPolicy.AllowLoopbackHTTPDevelopment); err != nil {
		return errors.New("desktop authorization issuer policy is invalid")
	}
	if s.sessionPolicy.AccessTTL <= 0 || s.sessionPolicy.RefreshTTL <= 0 || s.sessionPolicy.IdleTTL <= 0 ||
		s.sessionPolicy.AbsoluteTTL <= 0 || s.sessionPolicy.MaximumPerUser < 1 {
		return errors.New("desktop authorization session policy is invalid")
	}
	if s.users == nil || s.dpop == nil {
		return errors.New("desktop authorization identity dependencies are invalid")
	}
	return nil
}

// BindDesktopAuthorization exchanges the hosted page's one-use handle and
// fragment proof for a browser binding.
func (s *Service) BindDesktopAuthorization(ctx context.Context, _ invocation.Invocation, command BindDesktopAuthorizationCommand) (*DesktopAuthorizationBinding, error) {
	if !model.IsValidCredentialToken(command.Handle) || !model.IsValidCredentialToken(command.BrowserProof) ||
		!model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	binding := s.newCredential()
	if !model.IsValidCredentialToken(binding) || binding == command.Handle || binding == command.BrowserProof {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	bound, err := s.browserAuthentications.BindDesktopAuthorization(ctx, &store.DesktopAuthorizationBinding{
		HandleHash: model.HashToken(command.Handle), BrowserProofHash: model.HashToken(command.BrowserProof),
		StateHash: model.HashToken(command.State), BindingHash: model.HashToken(binding),
	})
	if err != nil {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	if bound == nil || bound.ExpiresAt.IsZero() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	return &DesktopAuthorizationBinding{Binding: binding, ExpiresAt: bound.ExpiresAt.UnixMilli()}, nil
}

// DesktopAuthorizationContext projects the bound Desktop authorization: the
// authenticated account, or the sign-in methods it may still use.
func (s *Service) DesktopAuthorizationContext(ctx context.Context, _ invocation.Invocation, binding string) (*DesktopAuthorizationBrowserContext, error) {
	if !model.IsValidCredentialToken(binding) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	current, err := s.browserAuthentications.GetDesktopAuthorizationContext(ctx, model.HashToken(binding))
	if err != nil || current == nil {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	if current.CreatedAt.IsZero() || current.ExpiresAt.Before(current.CreatedAt) {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	result := &DesktopAuthorizationBrowserContext{State: current.State, DeviceName: current.DeviceName,
		RequestedAt: current.CreatedAt.UnixMilli(), ExpiresAt: current.ExpiresAt.UnixMilli()}
	if current.State == model.BrowserAuthenticationStateAuthenticated {
		user, getErr := s.users.Get(ctx, current.UserID)
		if getErr != nil || user == nil || !user.IsActive() {
			return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(getErr)
		}
		result.Account = &DesktopAuthorizationAccount{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email}
		return result, nil
	}
	if current.State != model.BrowserAuthenticationStateBound {
		return nil, apperror.New("authentication.desktop_authorization.rejected")
	}
	local, policyErr := s.accessPolicy.AllowsLocalLogin(ctx)
	if policyErr != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(policyErr)
	}
	if local {
		local, policyErr = s.accessPolicy.AllowsDesktopAuthorization(ctx, "password", "")
		if policyErr != nil {
			return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(policyErr)
		}
	}
	result.LocalLoginEnabled = local
	for _, configured := range s.capabilities.Snapshot().Providers {
		allowed, allowErr := s.accessPolicy.AllowsDesktopAuthorization(ctx, configured.Descriptor.Type, configured.Descriptor.ID)
		if allowErr != nil {
			return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(allowErr)
		}
		if allowed {
			result.ExternalProviders = append(result.ExternalProviders, configured.Descriptor)
		}
	}
	return result, nil
}

// AuthenticateDesktopAuthorizationSession records the calling browser Web
// Session as the proof of the bound Desktop authorization.
func (s *Service) AuthenticateDesktopAuthorizationSession(ctx context.Context, invocation invocation.Invocation, command AuthenticateDesktopAuthorizationCommand) (*DesktopAuthorizationBrowserContext, error) {
	principal := invocation.Principal()
	if principal.MFAEnrollmentRequired {
		return nil, mfaEnrollmentRequiredAppError()
	}
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess ||
		principal.ClientType != model.SessionClientWeb || !model.IsValidCredentialToken(command.Binding) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	result, err := s.recordDesktopAuthorizationProof(ctx, command.Binding, store.DesktopAuthorizationAuthentication{
		SourceSessionID: principal.SessionID, SourceCredentialID: model.SessionCredentialID(principal.CredentialID),
		UserID: principal.UserID, AuthenticationMethod: principal.AuthenticationMethod,
		AuthenticationProviderID: principal.AuthenticationProviderID, ExternalIdentityID: principal.ExternalIdentityID,
		AuthenticationStrength: principal.AuthenticationStrength, AuthenticatedAt: principal.AuthenticatedAt,
		MFACompletedAt: principal.MFACompletedAt,
	})
	if err != nil || result.Denied {
		return nil, err
	}
	return s.DesktopAuthorizationContext(ctx, invocation, command.Binding)
}

// AuthenticateDesktopAuthorizationLocally proves a local password, and a
// second factor when one is required, for the bound Desktop authorization
// without creating a Web Session.
func (s *Service) AuthenticateDesktopAuthorizationLocally(ctx context.Context, invocation invocation.Invocation, command AuthenticateDesktopAuthorizationLocallyCommand) (*DesktopAuthorizationBrowserContext, error) {
	if !model.IsValidCredentialToken(command.Binding) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	proof, err := s.authenticateLocal(ctx, LoginCommand{
		LoginID: command.LoginID, Password: command.Password, MFACode: command.MFACode,
		ClientType: model.SessionClientWeb, Source: command.Source,
	})
	if err != nil {
		return nil, err
	}
	if proof.MFARecoveryRequired {
		return nil, invalidCredentialsAppError()
	}
	result, err := s.recordDesktopAuthorizationProof(ctx, command.Binding, store.DesktopAuthorizationAuthentication{
		PasswordProof: proof.PasswordProof,
		UserID:        proof.User.ID, AuthenticationMethod: "password", AuthenticationStrength: proof.AuthenticationStrength,
		AuthenticatedAt: model.TimeFromMillis(proof.AuthenticatedAt), MFACompletedAt: model.OptionalTimeFromMillis(proof.MFACompletedAt),
	})
	if err != nil {
		return nil, err
	}
	if !result.Denied {
		s.resetLocalAuthenticationAttempts(ctx, proof)
	}
	return s.DesktopAuthorizationContext(ctx, invocation, command.Binding)
}

func (s *Service) recordDesktopAuthorizationProof(ctx context.Context, binding string, input store.DesktopAuthorizationAuthentication) (*store.DesktopAuthorizationAuthenticationResult, error) {
	if err := s.requireDesktopMFAByPolicy(ctx, input.UserID, input.AuthenticationStrength); err != nil {
		return nil, err
	}
	input.BindingHash = model.HashToken(binding)
	input.Capabilities = AccessDeploymentCapabilities(s.capabilities.Snapshot())
	result, err := s.browserAuthentications.AuthenticateDesktopAuthorization(ctx, &input)
	if err != nil {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	if result == nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	if result.Denied {
		return result, apperror.New("authentication.desktop_authorization.account_session_locked")
	}
	return result, nil
}

// requireDesktopMFAByPolicy refuses a single-factor Desktop sign-in of a
// User the Access Policy requires to use two-step verification. A Desktop
// Session cannot be restricted to enrollment, so the person sets up two-step
// verification in the browser and signs in again.
func (s *Service) requireDesktopMFAByPolicy(ctx context.Context, userID model.UserID, strength model.AuthenticationStrength) error {
	if strength == model.AuthenticationMultiFactor {
		return nil
	}
	required, err := s.accessPolicy.RequiresMFA(ctx, userID)
	if err != nil {
		return apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if required {
		return mfaEnrollmentRequiredAppError()
	}
	return nil
}

func (s *Service) resolveExternalAuthentication(ctx context.Context, binding, state string) (desktopExternalAuthenticationTarget, error) {
	if !model.IsValidCredentialToken(binding) || !model.IsValidCredentialToken(state) {
		return desktopExternalAuthenticationTarget{}, apperror.New("authentication.desktop_authorization.invalid")
	}
	current, err := s.browserAuthentications.GetDesktopAuthorizationContext(ctx, model.HashToken(binding))
	if err != nil || current == nil || current.State != model.BrowserAuthenticationStateBound || !current.ID.IsValid() {
		return desktopExternalAuthenticationTarget{}, desktopAuthorizationStoreErrors.Translate(err)
	}
	return desktopExternalAuthenticationTarget{transactionID: current.ID, returnTo: "/authorize/desktop?state=" + url.QueryEscape(state)}, nil
}

func (s *Service) authenticateExternal(ctx context.Context, transactionID model.BrowserAuthenticationTransactionID,
	user *model.User, identity *model.ExternalIdentity, method string, assertion *model.ExternalAuthenticationAssertion,
) error {
	if !transactionID.IsValid() || user == nil || !user.IsActive() || identity == nil ||
		identity.UserID != user.ID || assertion == nil || assertion.ProviderID != identity.Provider {
		return apperror.New("authentication.desktop_authorization.invalid")
	}
	if err := s.requireDesktopMFAByPolicy(ctx, user.ID, assertion.AuthenticationStrength); err != nil {
		return err
	}
	authenticatedAt := model.TimeFromMillis(assertion.AuthenticatedAt)
	var mfaCompletedAt model.OptionalTime
	if assertion.AuthenticationStrength == model.AuthenticationMultiFactor {
		mfaCompletedAt = model.OptionalTimeFrom(authenticatedAt)
	}
	input := &store.DesktopAuthorizationAuthentication{
		TransactionID: transactionID, UserID: user.ID, AuthenticationMethod: method,
		AuthenticationProviderID: identity.Provider, ExternalIdentityID: identity.ID,
		AuthenticationStrength: assertion.AuthenticationStrength, AuthenticatedAt: authenticatedAt,
		MFACompletedAt: mfaCompletedAt, Capabilities: AccessDeploymentCapabilities(s.capabilities.Snapshot()),
	}
	result, err := s.browserAuthentications.AuthenticateDesktopAuthorization(ctx, input)
	if err != nil {
		return desktopAuthorizationStoreErrors.Translate(err)
	}
	if result == nil {
		return apperror.New("authentication.desktop_authorization.unavailable")
	}
	if result.Denied {
		return apperror.New("authentication.desktop_authorization.account_session_locked")
	}
	return nil
}

// ResetDesktopAuthorizationAccount forgets the account the bound Desktop
// authorization authenticated so another can sign in.
func (s *Service) ResetDesktopAuthorizationAccount(ctx context.Context, _ invocation.Invocation, binding string) error {
	if !model.IsValidCredentialToken(binding) {
		return apperror.New("authentication.desktop_authorization.invalid")
	}
	if err := s.browserAuthentications.ResetDesktopAuthorizationAccount(ctx, &store.DesktopAuthorizationAccountReset{BindingHash: model.HashToken(binding)}); err != nil {
		return desktopAuthorizationStoreErrors.Translate(err)
	}
	return nil
}

// ApproveDesktopAuthorization issues the one-use code for the account bound to
// the browser transaction. The binding cookie and pinned state are the
// approval proof; a caller that also presents a Principal must be that same
// account, so one signed-in User can never approve a transaction
// authenticated as another.
func (s *Service) ApproveDesktopAuthorization(ctx context.Context, invocation invocation.Invocation, command ApproveDesktopAuthorizationCommand) (*DesktopAuthorizationApproval, error) {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	current, err := s.browserAuthentications.GetDesktopAuthorizationContext(ctx, model.HashToken(command.Binding))
	if err != nil || current == nil || current.State != model.BrowserAuthenticationStateAuthenticated || !current.UserID.IsValid() {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if caller := invocation.Principal(); caller.UserID != "" && caller.UserID != current.UserID {
		failure := apperror.New("authentication.desktop_authorization.rejected")
		if auditErr := s.recordRejectedDesktopIssue(ctx, invocation, current.UserID, institution.ID, failure.Code()); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}
		return nil, failure
	}
	audit, err := s.beginDesktopIssueAudit(ctx, invocation, current.UserID, institution.ID)
	if err != nil || audit == nil || !audit.ID.IsValid() {
		return nil, apperror.New("audit.unavailable").Wrap(err)
	}
	code := s.newCredential()
	if !model.IsValidCredentialToken(code) {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	at := model.TimeUTC(s.now())
	issued, err := s.browserAuthentications.IssueCode(ctx, &store.DesktopAuthorizationCodeIssue{
		BindingHash: model.HashToken(command.Binding), StateHash: model.HashToken(command.State),
		CodeHash: model.HashToken(code), ExpectedUserID: current.UserID,
		CodeLifetime: s.desktopPolicy.CodeLifetime,
		Capabilities: AccessDeploymentCapabilities(s.capabilities.Snapshot()), AuditEventID: audit.ID, AuditAt: at,
	})
	if err != nil {
		failure := desktopAuthorizationStoreErrors.Translate(err)
		if auditErr := s.failDesktopAudit(ctx, audit.ID, apperror.CodeOf(failure, "authentication.desktop_authorization.unavailable")); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}
		return nil, failure
	}
	if issued == nil || model.ValidateDesktopAuthorizationCallback(issued.CallbackURL) != nil || issued.CodeExpiresAt.IsZero() {
		failure := apperror.New("authentication.desktop_authorization.unavailable")
		if auditErr := s.failDesktopAudit(ctx, audit.ID, failure.Code()); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}
		return nil, failure
	}
	redirect, err := desktopAuthorizationRedirectURL(issued.CallbackURL, code, command.State)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	return &DesktopAuthorizationApproval{RedirectURL: redirect, ExpiresAt: issued.CodeExpiresAt.UnixMilli()}, nil
}

// CancelDesktopAuthorization ends the bound Desktop authorization.
func (s *Service) CancelDesktopAuthorization(ctx context.Context, _ invocation.Invocation, command ApproveDesktopAuthorizationCommand) error {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return apperror.New("authentication.desktop_authorization.invalid")
	}
	err := s.browserAuthentications.Cancel(ctx, &store.DesktopAuthorizationCancellation{
		BindingHash: model.HashToken(command.Binding), StateHash: model.HashToken(command.State),
	})
	if err != nil {
		return desktopAuthorizationStoreErrors.Translate(err)
	}
	return nil
}

// ExchangeDesktopAuthorization redeems the one-use code with its PKCE
// verifier and DPoP proof for a Desktop Session and Desktop Registration.
func (s *Service) ExchangeDesktopAuthorization(ctx context.Context, invocation invocation.Invocation, command ExchangeDesktopAuthorizationCommand) (*DesktopAuthorizationExchangeResult, error) {
	if !model.IsValidCredentialToken(command.Code) || !model.IsValidCredentialToken(command.State) || !model.IsValidPKCEVerifier(command.CodeVerifier) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	if s.dpop == nil || command.PublicJWK.Validate() != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	thumbprint, err := command.PublicJWK.Thumbprint()
	if err != nil {
		return nil, apperror.New("authentication.dpop.invalid")
	}
	codeHash := model.HashToken(command.Code)
	if err = s.checkDesktopAuthorizationAttempt(ctx, desktopAuthorizationAttemptExchange, codeHash, command.Source); err != nil {
		return nil, err
	}
	resolved, err := s.browserAuthentications.ResolveDesktopAuthorizationExchange(ctx, &store.DesktopAuthorizationExchangeProof{
		CodeHash: codeHash, StateHash: model.HashToken(command.State), CodeChallenge: model.PKCES256Challenge(command.CodeVerifier),
		Issuer: strings.TrimSuffix(s.desktopPolicy.Issuer, "/"), ExpectedKeyThumbprint: thumbprint,
		DesktopRelease: command.DesktopRelease, DesktopBuildID: command.DesktopBuildID, DesktopPlatform: command.Platform,
		DesktopArchitecture: command.Architecture, DesktopRealtimeProtocol: command.RealtimeProtocol,
	})
	if err != nil {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}
	if resolved == nil || !resolved.ID.IsValid() || !resolved.UserID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	if err = s.verifyDesktopExchangeProof(ctx, command, resolved.ID, thumbprint); err != nil {
		return nil, err
	}
	compatibility, err := s.evaluateDesktopExchangeCompatibility(ctx, invocation, command)
	if err != nil {
		return nil, err
	}
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	mfaRequired, err := s.accessPolicy.RequiresMFA(ctx, resolved.UserID)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	lifetimes, err := s.accessPolicy.SessionLifetimes(ctx, s.sessionPolicy)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	audit, err := s.beginDesktopExchangeAudit(ctx, invocation, resolved.UserID, institution.ID)
	if err != nil || audit == nil || !audit.ID.IsValid() {
		return nil, apperror.New("audit.unavailable").Wrap(err)
	}
	at := model.TimeUTC(s.now())
	accessToken, refreshToken := s.newCredential(), s.newCredential()
	if !model.IsValidCredentialToken(accessToken) || !model.IsValidCredentialToken(refreshToken) {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	result, err := s.browserAuthentications.Exchange(ctx, &store.DesktopAuthorizationExchange{
		CodeHash: codeHash, StateHash: model.HashToken(command.State),
		CodeChallenge: model.PKCES256Challenge(command.CodeVerifier), Issuer: strings.TrimSuffix(s.desktopPolicy.Issuer, "/"),
		ExpectedPublicJWK: command.PublicJWK, ExpectedKeyThumbprint: thumbprint,
		DesktopRelease: command.DesktopRelease, DesktopBuildID: command.DesktopBuildID,
		DesktopPlatform: command.Platform, DesktopArchitecture: command.Architecture,
		DesktopRealtimeProtocol:            command.RealtimeProtocol,
		DesktopCompatibilityPolicyRevision: compatibility.PolicyRevision,
		AccessTokenHash:                    model.HashToken(accessToken), RefreshTokenHash: model.HashToken(refreshToken),
		AccessLifetime:  min(s.sessionPolicy.AccessTTL, lifetimes.Idle),
		RefreshLifetime: min(s.sessionPolicy.RefreshTTL, lifetimes.Absolute),
		IdleLifetime:    lifetimes.Idle, AbsoluteLifetime: lifetimes.Absolute,
		MaximumActive: s.sessionPolicy.MaximumPerUser, Capabilities: AccessDeploymentCapabilities(s.capabilities.Snapshot()),
		MFARequiredByPolicy: mfaRequired, AuditEventID: audit.ID, AuditAt: at,
	})
	if err != nil {
		failure := desktopAuthorizationStoreErrors.Translate(err)
		if auditErr := s.failDesktopAudit(ctx, audit.ID, apperror.CodeOf(failure, "authentication.desktop_authorization.unavailable")); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}
		return nil, failure
	}
	if result != nil && result.Denied {
		return nil, apperror.New("authentication.desktop_authorization.account_session_locked")
	}
	if !validDesktopAuthorizationExchangeResult(result, s.sessionPolicy) {
		failure := apperror.New("authentication.desktop_authorization.unavailable")
		if auditErr := s.failDesktopAudit(ctx, audit.ID, failure.Code()); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}
		return nil, failure
	}
	nonce, nonceErr := s.dpop.IssueNonce(ctx, dpopBinding{Kind: dpopBindingSession,
		SessionID: result.Session.ID, DesktopRegistrationID: result.Registration.ID,
		KeyThumbprint: result.Session.DPoPKeyThumbprint, Origin: s.dpop.deps.Policy.Origin})
	if nonceErr != nil {
		return nil, s.withdrawUndisclosedDesktopSession(ctx, invocation, institution.ID, result, nonceErr)
	}
	return &DesktopAuthorizationExchangeResult{Session: result.Session, Registration: result.Registration,
		Tokens: &model.AuthenticationTokens{TokenType: "DPoP", AccessToken: accessToken,
			RefreshToken: refreshToken, AccessExpiresAt: result.AccessExpiresAt, RefreshExpiresAt: result.RefreshExpiresAt},
		DPoPNonce: nonce}, nil
}

// verifyDesktopExchangeProof verifies the token request's DPoP proof against the authorization's bound key.
func (s *Service) verifyDesktopExchangeProof(
	ctx context.Context, command ExchangeDesktopAuthorizationCommand,
	authorizationID model.BrowserAuthenticationTransactionID, thumbprint string,
) error {
	binding := dpopBinding{Kind: dpopBindingAuthorization, AuthorizationID: authorizationID,
		KeyThumbprint: thumbprint, Origin: s.dpop.deps.Policy.Origin}
	target, targetErr := canonicalDPoPTarget(s.dpop.deps.Policy.Origin + "/api/v1/auth/desktop/token")
	if targetErr != nil {
		return apperror.New("authentication.desktop_authorization.unavailable").Wrap(targetErr)
	}
	if _, err := s.dpop.Verify(ctx, command.DPoPProof, "POST", target, "", &command.PublicJWK, binding); err != nil {
		return err
	}
	return nil
}

// evaluateDesktopExchangeCompatibility requires the exchanging desktop build to be compatible and ready.
func (s *Service) evaluateDesktopExchangeCompatibility(
	ctx context.Context, invocation invocation.Invocation, command ExchangeDesktopAuthorizationCommand,
) (DesktopCompatibilityResult, error) {
	compatibility, err := s.EvaluateDesktopCompatibility(ctx, invocation, desktopCompatibilityQueryFromAuthorization(command))
	if err != nil {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if compatibility.PolicyRevision < 1 {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.unavailable")
	}
	if compatibility.Compatibility != DesktopCompatibilityCompatible {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.incompatible").WithField("reason", compatibility.Reason)
	}
	if compatibility.Availability != model.DesktopAvailabilityReady {
		return DesktopCompatibilityResult{}, apperror.New("authentication.desktop_authorization.unavailable").WithField("reason", string(compatibility.Availability))
	}
	return compatibility, nil
}

// withdrawUndisclosedDesktopSession revokes and audits the withdrawal of an exchanged Session whose nonce could not be issued.
func (s *Service) withdrawUndisclosedDesktopSession(
	ctx context.Context, invocation invocation.Invocation, institutionID model.InstitutionID,
	result *store.DesktopAuthorizationExchangeResult, nonceErr error,
) error {
	// The credentials were never disclosed, but the SQL exchange has
	// committed. Revoke that exact Session synchronously so a cache outage
	// cannot consume the per-user Session allowance with inaccessible
	// Sessions. The active Desktop Registration remains reusable by a fresh
	// authorization after nonce storage recovers.
	// The exchange's success audit already committed, so a separate
	// withdrawal record says the issued Session was taken back.
	revokeCtx := context.WithoutCancel(ctx)
	withdrawal, auditErr := s.beginDesktopWithdrawalAudit(revokeCtx, invocation, result.Session.UserID,
		institutionID, result.Session.ID)
	hashes, revokeErr := s.sessions.Withdraw(revokeCtx, &store.SessionWithdrawal{ID: result.Session.ID, UserID: result.Session.UserID, RevokedAt: s.now(), Reason: model.SessionRevocationDesktopAuthorizationFailed})
	if auditErr == nil && withdrawal != nil {
		if revokeErr != nil {
			auditErr = s.failDesktopAudit(revokeCtx, withdrawal.ID, "authentication.desktop_authorization.unavailable")
		} else {
			auditErr = s.completeDesktopAudit(revokeCtx, withdrawal.ID, map[string]any{
				"session_id": result.Session.ID, "reason": string(model.SessionRevocationDesktopAuthorizationFailed),
			})
		}
	}
	if revokeErr != nil {
		return apperror.New("authentication.desktop_authorization.unavailable").Wrap(errors.Join(nonceErr, revokeErr, auditErr))
	}
	s.effects.SessionsRevoked(revokeCtx, result.Session.UserID,
		[]model.SessionID{result.Session.ID}, hashes)
	if auditErr != nil {
		return apperror.New("audit.unavailable").Wrap(errors.Join(nonceErr, auditErr))
	}
	return nonceErr
}

func validDesktopAuthorizationExchangeResult(result *store.DesktopAuthorizationExchangeResult, policy SessionPolicy) bool {
	if result == nil || result.Session == nil || result.Registration == nil || result.Registration.Validate() != nil ||
		result.Session.DesktopRegistrationID != result.Registration.ID ||
		result.Session.DPoPKeyThumbprint != result.Registration.KeyThumbprint || result.Session.Validate() != nil ||
		result.Session.ClientType != model.SessionClientDesktop || result.Session.ArchivedAt.Valid || result.Session.RevokedAt.Valid ||
		result.AccessExpiresAt.IsZero() || result.RefreshExpiresAt.IsZero() ||
		!result.AccessExpiresAt.After(result.Session.CreatedAt) || !result.RefreshExpiresAt.After(result.Session.CreatedAt) {
		return false
	}
	return !result.AccessExpiresAt.After(result.Session.CreatedAt.Add(policy.AccessTTL)) &&
		!result.RefreshExpiresAt.After(result.Session.CreatedAt.Add(policy.RefreshTTL))
}

func desktopAuthorizationRedirectURL(callback, code, state string) (string, error) {
	if model.ValidateDesktopAuthorizationCallback(callback) != nil {
		return "", model.ErrInvalidDesktopAuthorizationCallback
	}
	parsed, err := url.Parse(callback)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("code", code)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

var desktopAuthorizationStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationMethodDisabled, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrPasswordCredentialChanged, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationGenerationChanged, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrMFAReenrollmentRequired, Code: "authentication.desktop_authorization.rejected"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrMFAEnrollmentRequired, Code: "authentication.mfa.enrollment_required"},
		{Kind: apperror.StoreConflict, Constraint: "sessions_maximum_per_user", Code: "authentication.sessions.maximum_reached"},
	},
	NotFound:    "authentication.desktop_authorization.rejected",
	Unavailable: "authentication.desktop_authorization.unavailable",
}

// beginDesktopIssueAudit records the approval attempt with the approving
// account as the actor and the browser request's identity, never as an
// actor-less system event.
func (s *Service) beginDesktopIssueAudit(ctx context.Context, invocation invocation.Invocation, userID model.UserID,
	institutionID model.InstitutionID,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil || !userID.IsValid() || !institutionID.IsValid() {
		return nil, apperror.New("audit.unavailable")
	}
	return s.securityAudit.BeginAccountActionAtScope(ctx, access.AccountAuditActor{UserID: userID,
		SessionID: invocation.Principal().SessionID, ClientType: model.SessionClientWeb},
		model.ActionAuthenticationDesktopAuthorization,
		model.Resource{Type: model.ResourceUser, ID: userID.String()}, model.RoleScopeInstitution,
		institutionID.String(), invocation.RequestMetadata(), map[string]any{"operation": "issue_code"})
}

// beginDesktopExchangeAudit records the Desktop Session issuance attempt for
// the account that approved the code, with the Desktop request's network
// identity.
func (s *Service) beginDesktopExchangeAudit(ctx context.Context, invocation invocation.Invocation, userID model.UserID,
	institutionID model.InstitutionID,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil || !userID.IsValid() || !institutionID.IsValid() {
		return nil, apperror.New("audit.unavailable")
	}
	return s.securityAudit.BeginAccountActionAtScope(ctx, access.AccountAuditActor{UserID: userID, ClientType: model.SessionClientDesktop},
		model.ActionAuthenticationDesktopSessionIssue,
		model.Resource{Type: model.ResourceUser, ID: userID.String()}, model.RoleScopeInstitution,
		institutionID.String(), invocation.RequestMetadata(), map[string]any{"operation": "exchange"})
}

// recordRejectedDesktopIssue durably records an approval refused because the
// approving browser Session belongs to another account than the one the
// handoff authenticated: the approving Session's account is the actor and the
// handoff's account the resource.
func (s *Service) recordRejectedDesktopIssue(ctx context.Context, invocation invocation.Invocation,
	userID model.UserID, institutionID model.InstitutionID, errorCode string,
) error {
	caller := invocation.Principal()
	if s.securityAudit == nil || !caller.UserID.IsValid() || !userID.IsValid() || !institutionID.IsValid() {
		return apperror.New("audit.unavailable")
	}
	event, err := s.securityAudit.BeginAccountActionAtScope(ctx, access.AccountAuditActor{UserID: caller.UserID,
		SessionID: caller.SessionID, ClientType: model.SessionClientWeb},
		model.ActionAuthenticationDesktopAuthorization,
		model.Resource{Type: model.ResourceUser, ID: userID.String()}, model.RoleScopeInstitution,
		institutionID.String(), invocation.RequestMetadata(), map[string]any{"operation": "issue_code"})
	if err != nil {
		return err
	}
	return s.failDesktopAudit(ctx, event.ID, errorCode)
}

// beginDesktopWithdrawalAudit records the attempt to take back a Desktop
// Session whose exchange committed but whose credentials were never
// disclosed.
func (s *Service) beginDesktopWithdrawalAudit(ctx context.Context, invocation invocation.Invocation,
	userID model.UserID, institutionID model.InstitutionID, sessionID model.SessionID,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil || !userID.IsValid() || !institutionID.IsValid() || !sessionID.IsValid() {
		return nil, apperror.New("audit.unavailable")
	}
	return s.securityAudit.BeginAccountActionAtScope(ctx, access.AccountAuditActor{UserID: userID, ClientType: model.SessionClientDesktop},
		model.ActionAuthenticationDesktopSessionIssue,
		model.Resource{Type: model.ResourceUser, ID: userID.String()}, model.RoleScopeInstitution,
		institutionID.String(), invocation.RequestMetadata(),
		map[string]any{"operation": "withdraw", "value": map[string]any{"session_id": sessionID}})
}

func (s *Service) completeDesktopAudit(ctx context.Context, auditID model.AuditEventID, result map[string]any) error {
	if s.securityAudit == nil {
		return apperror.New("audit.unavailable")
	}
	_, appErr := s.securityAudit.CompleteCriticalAction(ctx, auditID, model.AuditStatusSuccess, "", result)
	return appErr
}

func (s *Service) failDesktopAudit(ctx context.Context, auditID model.AuditEventID, errorCode string) error {
	if s.securityAudit == nil {
		return apperror.New("audit.unavailable")
	}
	_, appErr := s.securityAudit.CompleteCriticalAction(ctx, auditID, model.AuditStatusFail, errorCode, nil)
	return appErr
}

// StartDesktopAuthorization begins a Desktop authorization for a compatible
// Desktop build and returns the hosted page to open.
func (s *Service) StartDesktopAuthorization(ctx context.Context, invocation invocation.Invocation, command StartDesktopAuthorizationCommand) (*DesktopAuthorizationStart, error) {
	if err := s.checkDesktopAuthorizationAttempt(ctx, desktopAuthorizationAttemptStart, model.HashToken(command.State), command.Source); err != nil {
		return nil, err
	}
	if model.ValidateDesktopAuthorizationCallback(command.CallbackURL) != nil ||
		!model.IsValidCredentialToken(command.State) ||
		!model.IsValidCredentialToken(command.CodeChallenge) ||
		command.PublicJWK.Validate() != nil || len(command.DeviceID) > model.SessionDeviceIDMaxLength ||
		utf8.RuneCountInString(command.DeviceName) > model.SessionDeviceNameMaxRunes {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	if s.dpop == nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	compatibility, err := s.EvaluateDesktopCompatibility(ctx, invocation, desktopCompatibilityQueryFromStart(command))
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if compatibility.Compatibility != DesktopCompatibilityCompatible {
		return nil, apperror.New("authentication.desktop_authorization.incompatible").WithField("reason", compatibility.Reason)
	}
	if compatibility.Availability != model.DesktopAvailabilityReady {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").WithField("reason", string(compatibility.Availability))
	}
	thumbprint, err := command.PublicJWK.Thumbprint()
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}
	enabled, err := s.hasAuthenticationChoice(ctx)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if !enabled {
		return nil, apperror.New("authentication.desktop_authorization.disabled")
	}
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	handle := s.newCredential()
	proof := s.newCredential()
	if !model.IsValidCredentialToken(handle) || !model.IsValidCredentialToken(proof) || handle == proof {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	creation := &store.DesktopAuthorizationCreation{
		ID: model.NewBrowserAuthenticationTransactionID(), InstitutionID: institution.ID,
		Issuer:     strings.TrimSuffix(s.desktopPolicy.Issuer, "/"),
		HandleHash: model.HashToken(handle), BrowserProofHash: model.HashToken(proof),
		StateHash: model.HashToken(command.State), CallbackURL: command.CallbackURL,
		CodeChallenge: command.CodeChallenge,
		DeviceID:      command.DeviceID, DeviceName: command.DeviceName,
		ProposedPublicJWK: command.PublicJWK, ProposedKeyThumbprint: thumbprint,
		DesktopRelease: command.DesktopRelease, DesktopBuildID: command.DesktopBuildID,
		DesktopPlatform: command.Platform, DesktopArchitecture: command.Architecture,
		DesktopRealtimeProtocol: command.RealtimeProtocol,
		Lifetime:                s.desktopPolicy.TransactionLifetime,
	}
	saved, err := s.browserAuthentications.CreateDesktopAuthorization(ctx, creation)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	if saved == nil || saved.ID != creation.ID || saved.ExpiresAt.IsZero() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}
	nonce, err := s.dpop.IssueNonce(ctx, dpopBinding{Kind: dpopBindingAuthorization,
		AuthorizationID: creation.ID, KeyThumbprint: thumbprint, Origin: s.dpop.deps.Policy.Origin})
	if err != nil {
		return nil, err
	}
	authorizationURL, err := desktopAuthorizationURL(creation.Issuer, handle, proof, command.State)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}
	return &DesktopAuthorizationStart{
		AuthorizationURL: authorizationURL,
		ExpiresAt:        saved.ExpiresAt.UnixMilli(),
		DPoPNonce:        nonce,
	}, nil
}

func desktopCompatibilityQueryFromStart(command StartDesktopAuthorizationCommand) DesktopCompatibilityQuery {
	return desktopCompatibilityQueryFromBuild(desktopAuthorizationBuild{
		release: command.DesktopRelease, buildID: command.DesktopBuildID, platform: command.Platform,
		architecture: command.Architecture, realtimeProtocol: command.RealtimeProtocol,
	})
}

func desktopCompatibilityQueryFromAuthorization(command ExchangeDesktopAuthorizationCommand) DesktopCompatibilityQuery {
	return desktopCompatibilityQueryFromBuild(desktopAuthorizationBuild{
		release: command.DesktopRelease, buildID: command.DesktopBuildID, platform: command.Platform,
		architecture: command.Architecture, realtimeProtocol: command.RealtimeProtocol,
	})
}

type desktopAuthorizationBuild struct {
	release          string
	buildID          string
	platform         model.DesktopPlatform
	architecture     model.DesktopArchitecture
	realtimeProtocol int
}

func desktopCompatibilityQueryFromBuild(build desktopAuthorizationBuild) DesktopCompatibilityQuery {
	return DesktopCompatibilityQuery{
		DesktopRelease: build.release, DesktopBuildID: build.buildID, Platform: string(build.platform),
		Architecture: string(build.architecture), RealtimeProtocol: build.realtimeProtocol,
	}
}

func (s *Service) hasAuthenticationChoice(ctx context.Context) (bool, error) {
	allowed, err := s.accessPolicy.AllowsDesktopAuthorization(ctx, "password", "")
	if err != nil || allowed {
		return allowed, err
	}
	for _, provider := range s.capabilities.Snapshot().Providers {
		allowed, err = s.accessPolicy.AllowsDesktopAuthorization(ctx, provider.Descriptor.Type, provider.Descriptor.ID)
		if err != nil || allowed {
			return allowed, err
		}
	}
	return false, nil
}

func desktopAuthorizationURL(issuer, handle, proof, state string) (string, error) {
	return hostedDesktopURL(issuer, "/authorize/desktop", handle, proof, state)
}
