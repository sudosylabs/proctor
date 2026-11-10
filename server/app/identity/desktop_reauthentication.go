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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// desktopReauthenticationRejection names who proved what against which
// Desktop Session's account.
type desktopReauthenticationRejection struct {
	actor         model.UserID
	target        model.UserID
	institutionID model.InstitutionID
	operation     string
	errorCode     string
}

// requireDesktopReauthentication refuses a Service whose Desktop
// reauthentication collaborators or policy are incomplete. Desktop
// reauthentication lets a Desktop Session refresh its primary proof in the
// system browser. It never creates a Session, never accepts a browser Web
// Session as proof, and accepts only the Desktop Session's own authentication
// method for that Session's own User. New calls it once, after Desktop
// authorization is set up.
func (s *Service) requireDesktopReauthentication() error {
	if s.browserAuthentications == nil || s.institutions == nil || s.users == nil || s.capabilities == nil ||
		s.newCredential == nil || s.now == nil {
		return errors.New("desktop reauthentication dependencies are required")
	}
	if s.recentAuthenticationTTL <= 0 {
		return errors.New("desktop reauthentication recency policy is invalid")
	}
	if s.desktopPolicy.TransactionLifetime == 0 {
		s.desktopPolicy.TransactionLifetime = model.BrowserAuthenticationTransactionLifetime
	}
	if s.desktopPolicy.CodeLifetime == 0 {
		s.desktopPolicy.CodeLifetime = model.DesktopAuthorizationCodeLifetime
	}
	if s.desktopPolicy.TransactionLifetime <= 0 || s.desktopPolicy.TransactionLifetime > model.BrowserAuthenticationTransactionLifetime ||
		s.desktopPolicy.CodeLifetime <= 0 || s.desktopPolicy.CodeLifetime > model.DesktopAuthorizationCodeLifetime {
		return errors.New("desktop reauthentication lifetime policy is invalid")
	}
	if err := model.ValidateBrowserAuthenticationIssuer(s.desktopPolicy.Issuer, s.desktopPolicy.AllowLoopbackHTTPDevelopment); err != nil {
		return errors.New("desktop reauthentication issuer policy is invalid")
	}
	return nil
}

// StartDesktopReauthenticationCommand carries the Desktop's loopback callback,
// state and S256 PKCE challenge, validated like Desktop Authorization start.
type StartDesktopReauthenticationCommand struct {
	CallbackURL   string
	State         string
	CodeChallenge string
	Source        string
}

// DesktopReauthenticationStart is the hosted page the Desktop opens in the
// system browser, valid until ExpiresAt.
type DesktopReauthenticationStart struct {
	AuthorizationURL string
	ExpiresAt        time.Time
}

// DesktopReauthenticationBrowserContext is the safe projection the hosted
// page renders: whose Desktop Session it is and the one proof it accepts.
type DesktopReauthenticationBrowserContext struct {
	State       model.BrowserAuthenticationState
	DisplayName string
	// AuthenticationMethod is "password" or the provider protocol type of the
	// Desktop Session's own authentication.
	AuthenticationMethod string
	// Provider is the Desktop Session's identity provider; nil for password.
	Provider  *model.ExternalAuthenticationProvider
	ExpiresAt time.Time
}

// AuthenticateDesktopReauthenticationPasswordCommand proves the password of
// the Desktop Session's User. It has no selectable login identifier.
type AuthenticateDesktopReauthenticationPasswordCommand struct {
	Binding  string
	Password string
	Source   string
}

// CompleteDesktopReauthenticationCommand redeems a reauthentication code from
// the Desktop Session that started it.
type CompleteDesktopReauthenticationCommand struct {
	Code         string
	CodeVerifier string
	State        string
	Source       string
}

// DesktopReauthenticationResult reports how long the refreshed Desktop
// Session counts as recently authenticated.
type DesktopReauthenticationResult struct {
	RecentlyAuthenticatedUntil time.Time
}

// requireDesktopDPoPSession accepts only a Desktop Session whose current
// request passed DPoP verification with its registered key.
func requireDesktopDPoPSession(principal model.Principal) (model.SessionCredentialID, error) {
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess ||
		principal.ClientType != model.SessionClientDesktop || !principal.HasRegisteredDesktopKey() || principal.MFARecoveryRequired {
		return "", apperror.New("authentication.desktop_reauthentication.invalid")
	}
	credentialID, err := model.ParseSessionCredentialID(principal.CredentialID.String())
	if err != nil {
		return "", apperror.New("authentication.desktop_reauthentication.invalid")
	}
	return credentialID, nil
}

// StartDesktopReauthentication begins a browser reauthentication for the
// calling Desktop Session and returns the hosted page to open.
func (s *Service) StartDesktopReauthentication(ctx context.Context, invocation invocation.Invocation, command StartDesktopReauthenticationCommand) (*DesktopReauthenticationStart, error) {
	principal := invocation.Principal()
	credentialID, err := requireDesktopDPoPSession(principal)
	if err != nil {
		return nil, err
	}
	if err = s.checkDesktopAuthorizationAttempt(ctx, desktopAuthorizationAttemptReauthenticationStart, principal.UserID.String(), command.Source); err != nil {
		return nil, err
	}
	if model.ValidateDesktopAuthorizationCallback(command.CallbackURL) != nil ||
		!model.IsValidCredentialToken(command.State) || !model.IsValidCredentialToken(command.CodeChallenge) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, authenticationUnavailable(err)
	}
	handle, proof := s.newCredential(), s.newCredential()
	if !model.IsValidCredentialToken(handle) || !model.IsValidCredentialToken(proof) || handle == proof {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication credentials are invalid"))
	}
	issuer := strings.TrimSuffix(s.desktopPolicy.Issuer, "/")
	created, err := s.browserAuthentications.CreateDesktopReauthentication(ctx, &store.DesktopReauthenticationCreation{
		ID: model.NewBrowserAuthenticationTransactionID(), InstitutionID: institution.ID, Issuer: issuer,
		HandleHash: model.HashToken(handle), BrowserProofHash: model.HashToken(proof),
		StateHash: model.HashToken(command.State), CallbackURL: command.CallbackURL, CodeChallenge: command.CodeChallenge,
		UserID: principal.UserID, SessionID: principal.SessionID, CredentialID: credentialID,
		DesktopRegistrationID: principal.DesktopRegistrationID, KeyThumbprint: principal.DPoPKeyThumbprint,
		Lifetime: s.desktopPolicy.TransactionLifetime,
	})
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if created == nil || created.ExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no deadline"))
	}
	authorizationURL, err := hostedDesktopURL(issuer, "/reauthenticate/desktop", handle, proof, command.State)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	return &DesktopReauthenticationStart{AuthorizationURL: authorizationURL, ExpiresAt: created.ExpiresAt}, nil
}

// BindDesktopReauthentication exchanges the hosted page's one-use handle and
// fragment proof for a browser binding.
func (s *Service) BindDesktopReauthentication(ctx context.Context, _ invocation.Invocation, command BindDesktopAuthorizationCommand) (*DesktopAuthorizationBinding, error) {
	if !model.IsValidCredentialToken(command.Handle) || !model.IsValidCredentialToken(command.BrowserProof) ||
		!model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	binding := s.newCredential()
	if !model.IsValidCredentialToken(binding) || binding == command.Handle || binding == command.BrowserProof {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication binding is invalid"))
	}
	bound, err := s.browserAuthentications.BindDesktopReauthentication(ctx, &store.DesktopAuthorizationBinding{
		HandleHash: model.HashToken(command.Handle), BrowserProofHash: model.HashToken(command.BrowserProof),
		StateHash: model.HashToken(command.State), BindingHash: model.HashToken(binding),
	})
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if bound == nil || bound.ExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no deadline"))
	}
	return &DesktopAuthorizationBinding{Binding: binding, ExpiresAt: bound.ExpiresAt.UnixMilli()}, nil
}

// DesktopReauthenticationContext projects the bound reauthentication: the
// Desktop Session's account and the single proof it accepts.
func (s *Service) DesktopReauthenticationContext(ctx context.Context, _ invocation.Invocation, binding string) (*DesktopReauthenticationBrowserContext, error) {
	current, err := s.boundDesktopReauthentication(ctx, binding)
	if err != nil {
		return nil, err
	}
	user, err := s.users.Get(ctx, current.UserID)
	if err != nil || user == nil || !user.IsActive() {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid").Wrap(err)
	}
	result := &DesktopReauthenticationBrowserContext{State: current.State, DisplayName: user.DisplayName,
		AuthenticationMethod: current.ExpectedAuthenticationMethod, ExpiresAt: current.ExpiresAt}
	if current.ExpectedProviderID != "" {
		provider, configured := s.configuredProvider(current.ExpectedProviderID)
		if !configured {
			return nil, apperror.New("authentication.desktop_reauthentication.invalid")
		}
		result.Provider = &provider
	}
	return result, nil
}

func (s *Service) boundDesktopReauthentication(ctx context.Context, binding string) (*store.DesktopReauthenticationContext, error) {
	if !model.IsValidCredentialToken(binding) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	current, err := s.browserAuthentications.GetDesktopReauthenticationContext(ctx, model.HashToken(binding))
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if current == nil || !current.ID.IsValid() || !current.UserID.IsValid() || current.ExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication context is invalid"))
	}
	return current, nil
}

func (s *Service) configuredProvider(id string) (model.ExternalAuthenticationProvider, bool) {
	for _, configured := range s.capabilities.Snapshot().Providers {
		if configured.Descriptor.ID == id {
			return configured.Descriptor, true
		}
	}
	return model.ExternalAuthenticationProvider{}, false
}

// AuthenticateDesktopReauthenticationPassword checks the password of the
// Desktop Session's own User. It creates no Web Session and asks for no second
// factor: the Desktop Session's MFA history is unchanged and only primary
// proof is refreshed.
func (s *Service) AuthenticateDesktopReauthenticationPassword(ctx context.Context, invocation invocation.Invocation, command AuthenticateDesktopReauthenticationPasswordCommand) (*DesktopReauthenticationBrowserContext, error) {
	current, err := s.boundDesktopReauthentication(ctx, command.Binding)
	if err != nil {
		return nil, err
	}
	if current.State != model.BrowserAuthenticationStateBound {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	if current.ExpectedAuthenticationMethod != "password" || current.ExpectedProviderID != "" {
		return nil, apperror.New("authentication.reauthentication_method_required")
	}
	receipt, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeReauthentication, window: s.loginRateLimit.Window,
		limits: []AttemptLimit{
			{dimension: authenticationAttemptDimensionIdentitySource, maximum: s.loginRateLimit.MaximumAttempts, identity: current.UserID.String(), source: command.Source},
			{dimension: authenticationAttemptDimensionSource, maximum: s.loginRateLimit.MaximumSourceAttempts, source: command.Source},
		},
	})
	if err != nil {
		return nil, RateLimitUnavailableAppError(err)
	}
	if limited {
		return nil, apperror.New("authentication.rate_limited")
	}
	allowed, err := s.accessPolicy.AllowsLocalLogin(ctx)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	if !allowed || command.Password == "" || len(command.Password) > s.hasher.maximumPasswordLength() {
		return nil, invalidCredentialsAppError()
	}
	user, err := s.users.Get(ctx, current.UserID)
	if err != nil || user == nil || !user.IsActive() {
		return nil, invalidCredentialsAppError()
	}
	credential, err := s.passwords.GetByUser(ctx, current.UserID)
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if err = s.hasher.Verify(ctx, credential.PasswordHash, command.Password); err != nil {
		if errors.Is(err, ErrPasswordMismatch) {
			return nil, invalidCredentialsAppError()
		}
		return nil, passwordWorkError(err, "authentication.internal")
	}
	if _, err = s.recordDesktopReauthenticationProof(ctx, invocation, &store.DesktopReauthenticationAuthentication{
		BindingHash: model.HashToken(command.Binding), UserID: current.UserID, AuthenticationMethod: "password",
		PasswordProof:   store.PasswordCredentialProof{ID: credential.ID, Revision: credential.Revision},
		AuthenticatedAt: model.TimeUTC(s.now()),
	}); err != nil {
		return nil, err
	}
	if err = s.attempts.reset(ctx, receipt, authenticationAttemptDimensionIdentitySource); err != nil {
		s.warn(ctx, "desktop reauthentication rate-limit reset failed", err)
	}
	return s.DesktopReauthenticationContext(ctx, invocation, command.Binding)
}

// authenticate records a fresh proof and audits a refusal that proved another
// account than the Desktop Session's.
func (s *Service) recordDesktopReauthenticationProof(ctx context.Context, invocation invocation.Invocation, input *store.DesktopReauthenticationAuthentication) (*store.DesktopReauthenticationAuthenticationResult, error) {
	result, err := s.browserAuthentications.AuthenticateDesktopReauthentication(ctx, input)
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if result == nil {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no result"))
	}
	if !result.Denied {
		return result, nil
	}
	failure := apperror.New("authentication.desktop_reauthentication.denied")
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("audit.unavailable").Wrap(err)
	}
	if auditErr := s.recordRejectedDesktopReauthentication(ctx, invocation, desktopReauthenticationRejection{
		actor: input.UserID, target: result.UserID, institutionID: institution.ID,
		operation: desktopReauthenticationOperation(input.AuthenticationMethod), errorCode: failure.Code(),
	}); auditErr != nil {
		return nil, apperror.New("audit.unavailable").Wrap(auditErr)
	}
	return nil, failure
}

// resolveExternalReauthentication returns the bound reauthentication a
// provider login may continue, only for the Desktop Session's own provider.
func (s *Service) resolveExternalReauthentication(ctx context.Context, binding, state, providerID string) (desktopExternalAuthenticationTarget, error) {
	if !model.IsValidCredentialToken(state) {
		return desktopExternalAuthenticationTarget{}, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	current, err := s.boundDesktopReauthentication(ctx, binding)
	if err != nil {
		return desktopExternalAuthenticationTarget{}, err
	}
	if current.State != model.BrowserAuthenticationStateBound {
		return desktopExternalAuthenticationTarget{}, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	if current.ExpectedProviderID == "" || current.ExpectedProviderID != strings.ToLower(strings.TrimSpace(providerID)) {
		return desktopExternalAuthenticationTarget{}, apperror.New("authentication.reauthentication_method_required")
	}
	return desktopExternalAuthenticationTarget{transactionID: current.ID,
		returnTo: "/reauthenticate/desktop?state=" + url.QueryEscape(state)}, nil
}

// authenticateExternalReauthentication records fresh provider proof of the
// exact External Identity on the reauthentication bound to the login state.
func (s *Service) authenticateExternalReauthentication(ctx context.Context, invocation invocation.Invocation,
	transactionID model.BrowserAuthenticationTransactionID, identity *model.ExternalIdentity, method string,
	assertion *model.ExternalAuthenticationAssertion,
) error {
	if !transactionID.IsValid() || identity == nil || !identity.UserID.IsValid() || !identity.ID.IsValid() ||
		assertion == nil || assertion.ProviderID != identity.Provider || method == "" || method == "password" {
		return apperror.New("authentication.desktop_reauthentication.invalid")
	}
	_, err := s.recordDesktopReauthenticationProof(ctx, invocation, &store.DesktopReauthenticationAuthentication{
		TransactionID: transactionID, UserID: identity.UserID, AuthenticationMethod: method,
		ProviderID: identity.Provider, ExternalIdentityID: identity.ID,
		AuthenticatedAt: model.TimeFromMillis(assertion.AuthenticatedAt),
	})
	return err
}

// ApproveDesktopReauthentication issues the one-use code and the loopback
// redirect that hands it to the Desktop.
func (s *Service) ApproveDesktopReauthentication(ctx context.Context, _ invocation.Invocation, command ApproveDesktopAuthorizationCommand) (*DesktopAuthorizationApproval, error) {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	code := s.newCredential()
	if !model.IsValidCredentialToken(code) {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication code is invalid"))
	}
	issued, err := s.browserAuthentications.IssueDesktopReauthenticationCode(ctx, &store.DesktopReauthenticationCodeIssue{
		BindingHash: model.HashToken(command.Binding), StateHash: model.HashToken(command.State),
		CodeHash: model.HashToken(code), CodeLifetime: s.desktopPolicy.CodeLifetime,
	})
	if err != nil {
		return nil, desktopReauthenticationStoreErrors.Translate(err)
	}
	if issued == nil || issued.CodeExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no code deadline"))
	}
	redirect, err := desktopAuthorizationRedirectURL(issued.CallbackURL, code, command.State)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	return &DesktopAuthorizationApproval{RedirectURL: redirect, ExpiresAt: issued.CodeExpiresAt.UnixMilli()}, nil
}

// CancelDesktopReauthentication ends the reauthentication and destroys every
// live proof.
func (s *Service) CancelDesktopReauthentication(ctx context.Context, _ invocation.Invocation, command ApproveDesktopAuthorizationCommand) error {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return apperror.New("authentication.desktop_reauthentication.invalid")
	}
	if err := s.browserAuthentications.CancelDesktopReauthentication(ctx, &store.DesktopAuthorizationCancellation{
		BindingHash: model.HashToken(command.Binding), StateHash: model.HashToken(command.State),
	}); err != nil {
		return desktopReauthenticationStoreErrors.Translate(err)
	}
	return nil
}

// CompleteDesktopReauthentication redeems the code from the same Desktop
// Session and key that started the handoff and refreshes that Session's
// primary proof. No Session is created and the Registration is unchanged.
func (s *Service) CompleteDesktopReauthentication(ctx context.Context, invocation invocation.Invocation, command CompleteDesktopReauthenticationCommand) (*DesktopReauthenticationResult, error) {
	principal := invocation.Principal()
	credentialID, err := requireDesktopDPoPSession(principal)
	if err != nil {
		return nil, err
	}
	if !model.IsValidCredentialToken(command.Code) || !model.IsValidCredentialToken(command.State) ||
		!model.IsValidPKCEVerifier(command.CodeVerifier) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	codeHash := model.HashToken(command.Code)
	if err = s.checkDesktopAuthorizationAttempt(ctx, desktopAuthorizationAttemptReauthenticationComplete, codeHash, command.Source); err != nil {
		return nil, err
	}
	result, err := access.RunAudited(ctx, s.auditor, access.Mutation{
		Invocation: invocation, Action: model.ActionAuthenticationReauthenticate,
		Resource:  model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
		Operation: desktopReauthenticationOperation(principal.AuthenticationMethod),
		Value:     map[string]any{"session_id": principal.SessionID.String()},
	}, s.now, func(ctx context.Context, reference access.MutationReference) (*store.SessionReauthenticationResult, error) {
		return s.browserAuthentications.CompleteDesktopReauthentication(ctx, &store.DesktopReauthenticationCompletion{
			CodeHash: codeHash, StateHash: model.HashToken(command.State),
			CodeChallenge: model.PKCES256Challenge(command.CodeVerifier),
			UserID:        principal.UserID, SessionID: principal.SessionID, CredentialID: credentialID,
			DesktopRegistrationID: principal.DesktopRegistrationID, KeyThumbprint: principal.DPoPKeyThumbprint,
			AuditEventID: reference.ID,
		})
	}, desktopReauthenticationStoreErrors.Translate)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Session == nil || !result.Session.ReauthenticatedAt.Valid {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no Session"))
	}
	s.effects.AuthenticationCacheInvalidated(ctx, principal.UserID, result.AccessTokenHashes)
	latest := result.Session.AuthenticatedAt
	if result.Session.ReauthenticatedAt.Time.After(latest) {
		latest = result.Session.ReauthenticatedAt.Time
	}
	return &DesktopReauthenticationResult{RecentlyAuthenticatedUntil: latest.Add(s.recentAuthenticationTTL)}, nil
}

// desktopReauthenticationOperation names the audited proof path.
func desktopReauthenticationOperation(method string) string {
	if method == "password" {
		return "desktop_password"
	}
	return "desktop_external"
}

// hostedDesktopURL is the hosted page at path carrying the one-use handle and
// state in its query and the browser proof only in its fragment.
func hostedDesktopURL(issuer, path, handle, proof, state string) (string, error) {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Host == "" {
		return "", errors.New("desktop hosted page issuer is invalid")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + path
	query := parsed.Query()
	query.Set("request", handle)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	parsed.Fragment = "proof=" + proof
	return parsed.String(), nil
}

var desktopReauthenticationStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrPasswordCredentialChanged, Code: "authentication.credentials_invalid"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationMethodDisabled, Code: "authentication.desktop_reauthentication.invalid"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationGenerationChanged, Code: "authentication.desktop_reauthentication.invalid"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrMFAReenrollmentRequired, Code: "authentication.desktop_reauthentication.invalid"},
	},
	NotFound:    "authentication.desktop_reauthentication.invalid",
	Conflict:    "authentication.desktop_reauthentication.invalid",
	Unavailable: "authentication.internal",
}

// recordRejectedDesktopReauthentication records, as a failed
// reauthentication, a browser proof of another account: the proving account
// is the actor and the Desktop Session's account the resource.
func (s *Service) recordRejectedDesktopReauthentication(ctx context.Context, invocation invocation.Invocation,
	rejection desktopReauthenticationRejection,
) error {
	if s.securityAudit == nil || !rejection.actor.IsValid() || !rejection.target.IsValid() || !rejection.institutionID.IsValid() {
		return apperror.New("audit.unavailable")
	}
	event, err := s.securityAudit.BeginAccountActionAtScope(ctx, access.AccountAuditActor{UserID: rejection.actor, ClientType: model.SessionClientWeb},
		model.ActionAuthenticationReauthenticate,
		model.Resource{Type: model.ResourceUser, ID: rejection.target.String()}, model.RoleScopeInstitution,
		rejection.institutionID.String(), invocation.RequestMetadata(), map[string]any{"operation": rejection.operation})
	if err != nil {
		return err
	}
	_, appErr := s.securityAudit.CompleteCriticalAction(ctx, event.ID, model.AuditStatusFail, rejection.errorCode, nil)
	return appErr
}
