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
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Sentinel errors returned by the composition-owned external provider adapter.
// Application code never imports concrete protocol packages.
var (
	ErrExternalAuthenticationRejected    = errors.New("external authentication rejected")
	ErrExternalAuthenticationInvalid     = errors.New("external authentication response is invalid")
	ErrExternalAuthenticationUnavailable = errors.New("external authentication provider is unavailable")
)

// ExternalProviderBeginRequest is the protocol-neutral start challenge.
type ExternalProviderBeginRequest struct {
	CallbackURL         string
	State               string
	Proof               string
	FreshAuthentication bool
}

// ExternalProviderBeginResponse is the protocol-neutral redirect challenge.
type ExternalProviderBeginResponse struct {
	RedirectURL string
}

// ExternalProviderCompleteRequest is the protocol-neutral callback completion.
type ExternalProviderCompleteRequest struct {
	CallbackURL             string
	State                   string
	Proof                   string
	Callback                model.ExternalAuthenticationCallback
	AuthenticationStartedAt time.Time
}

// ExternalIdentityProvider is the app-owned protocol-neutral provider port.
// Composition adapts concrete CAS/OIDC implementations to this surface.
type ExternalIdentityProvider interface {
	Descriptor() model.ExternalAuthenticationProvider
	AutoProvision() bool
	Begin(context.Context, ExternalProviderBeginRequest) (*ExternalProviderBeginResponse, error)
	State(model.ExternalAuthenticationCallback) (string, error)
	Complete(
		context.Context,
		ExternalProviderCompleteRequest,
	) (*model.ExternalAuthenticationAssertion, error)
}

// ExternalProviderSource is the protocol-neutral registry surface consumed by
// application external-login orchestration. Concrete CAS/OIDC adapters remain
// outside package app.
type ExternalProviderSource interface {
	Descriptors() []model.ExternalAuthenticationProvider
	Provider(id string) (ExternalIdentityProvider, bool)
}

// InvitationAcceptor admits an External Identity through the Invitation its
// login state carries. The parent app package connects it to the onboarding
// area's Invitation acceptance.
type InvitationAcceptor interface {
	AcceptExternalIdentity(
		context.Context,
		*model.ExternalLoginState,
		*model.ExternalAuthenticationAssertion,
		store.AccessDeploymentCapabilities,
		model.RequestMetadata,
		string,
	) (*store.ExternalIdentityInvitationAcceptanceResult, error)
}

type desktopExternalAuthenticationTarget struct {
	transactionID model.BrowserAuthenticationTransactionID
	returnTo      string
}

// ExternalAuthenticationPolicy is the deployment projection for external login.
type ExternalAuthenticationPolicy struct {
	PublicURL      string
	LoginStateTTL  time.Duration
	LoginRateLimit LoginRateLimitPolicy
	NodeID         string
}

// requireExternalAuthentication refuses a Service whose external
// authentication collaborators are incomplete. New calls it once, after
// sign-in and Desktop authorization are set up.
func (s *Service) requireExternalAuthentication() error {
	if s.externalProviders == nil {
		return errors.New("external authentication provider registry is required")
	}
	if s.externalLoginStates == nil || s.institutions == nil || s.externalIdentities == nil || s.sessions == nil {
		return errors.New("external authentication persistence is required")
	}
	if s.accessPolicy == nil {
		return errors.New("external authentication access policy is required")
	}
	if s.attempts == nil {
		return errors.New("external authentication attempt accounting is required")
	}
	if s.invalidator == nil {
		return errors.New("authentication invalidator is required")
	}
	if s.auditor == nil || s.capabilities == nil || s.invitations == nil || s.recentAuthenticationTTL <= 0 {
		return errors.New("external authentication method lifecycle dependencies are required")
	}
	if s.diagnostics == nil {
		return errors.New("external authentication diagnostics are required")
	}
	if s.newCredential == nil {
		return errors.New("external authentication credential generator is required")
	}
	if s.now == nil {
		return errors.New("external authentication clock is required")
	}
	return nil
}

// beginExternalAuthenticationAudit, completeExternalAuthenticationAudit and
// recordExternalCompletionFailure durably record external sign-in: the
// attempt before its Session is issued, that attempt's failed completion, and
// a callback that failed after its login state was consumed.
func (s *Service) beginExternalAuthenticationAudit(
	ctx context.Context,
	attempt externalCompletionAttempt,
	userID model.UserID,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil {
		return nil, access.AuditUnavailable(errors.New("external authentication audit service is unavailable"))
	}
	return s.securityAudit.BeginAuthentication(
		ctx,
		userID,
		attempt.method,
		attempt.providerID,
		attempt.state.ClientType,
		attempt.metadata,
		attempt.institution.ID,
	)
}

func (s *Service) completeExternalAuthenticationAudit(ctx context.Context, auditID model.AuditEventID, errorCode string) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("external authentication audit service is unavailable"))
	}
	_, err := s.securityAudit.CompleteCriticalAction(ctx, auditID, model.AuditStatusFail, errorCode, nil)
	return err
}

func (s *Service) recordExternalCompletionFailure(ctx context.Context, attempt externalCompletionAttempt, errorCode string) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("external authentication audit service is unavailable"))
	}
	return s.securityAudit.RecordExternalAuthenticationFailure(
		ctx,
		attempt.providerID,
		attempt.method,
		attempt.metadata,
		attempt.institution.ID,
		errorCode,
	)
}

// ExternalAuthenticationProviders lists the configured providers the current
// Access Policy offers for sign-in.
func (s *Service) ExternalAuthenticationProviders(
	ctx context.Context,
	_ invocation.Invocation,
) ([]model.ExternalAuthenticationProvider, error) {
	providers, err := s.accessPolicy.AvailableExternalProviders(ctx, s.externalProviders.Descriptors())
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	return providers, nil
}

// BeginExternalAuthenticationCommand starts a browser external login.
type BeginExternalAuthenticationCommand struct {
	ProviderID      string
	InvitationClaim string
	ReturnTo        string
	ClientType      model.SessionClientType
	DeviceID        string
	DeviceName      string
	Source          string
}

// CompleteExternalAuthenticationCommand finishes a browser external login.
type CompleteExternalAuthenticationCommand struct {
	ProviderID string
	Callback   model.ExternalAuthenticationCallback
	Binding    string
	Source     string
}

type BeginProviderConnectionCommand struct {
	ProviderID string
	ReturnTo   string
	Source     string
}

type BeginDesktopExternalAuthenticationCommand struct {
	ProviderID string
	Binding    string
	State      string
	Source     string
}

// BeginDesktopExternalAuthentication starts provider sign-in for the Desktop
// authorization bound to the browser; the callback hands the proof to that
// authorization.
func (s *Service) BeginDesktopExternalAuthentication(
	ctx context.Context,
	_ invocation.Invocation,
	command BeginDesktopExternalAuthenticationCommand,
) (*model.ExternalAuthenticationStart, error) {
	target, err := s.resolveExternalAuthentication(ctx, command.Binding, command.State)
	if err != nil {
		return nil, err
	}

	return s.beginForPurpose(
		ctx,
		command.ProviderID,
		target.returnTo,
		model.SessionClientWeb,
		"",
		"",
		command.Source,
		model.ExternalAuthenticationPurposeDesktopAuthorization,
		"",
		"",
		"",
		target.transactionID,
		nil,
	)
}

// BeginDesktopReauthenticationExternalAuthenticationCommand starts fresh
// provider proof for the Desktop reauthentication bound to the browser.
type BeginDesktopReauthenticationExternalAuthenticationCommand struct {
	ProviderID string
	Binding    string
	State      string
	Source     string
}

// BeginDesktopReauthenticationExternalAuthentication redirects to the Desktop
// Session's own provider with a fresh-authentication challenge. The provider
// callback returns to the hosted reauthentication page.
func (s *Service) BeginDesktopReauthenticationExternalAuthentication(
	ctx context.Context,
	_ invocation.Invocation,
	command BeginDesktopReauthenticationExternalAuthenticationCommand,
) (*model.ExternalAuthenticationStart, error) {
	target, err := s.resolveExternalReauthentication(ctx, command.Binding, command.State, command.ProviderID)
	if err != nil {
		return nil, err
	}

	return s.beginForPurpose(
		ctx,
		command.ProviderID,
		target.returnTo,
		model.SessionClientWeb,
		"",
		"",
		command.Source,
		model.ExternalAuthenticationPurposeDesktopReauthentication,
		"",
		"",
		"",
		target.transactionID,
		nil,
	)
}

// BeginProviderConnection starts linking a provider to the current User after
// strong recent authentication and audits the attempt it begins.
func (s *Service) BeginProviderConnection(
	ctx context.Context,
	invocation invocation.Invocation,
	command BeginProviderConnectionCommand,
) (*model.ExternalAuthenticationStart, error) {
	if err := access.RequireStrongRecentSession(invocation.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     model.ActionExternalIdentityManage,
		Resource:   model.Resource{Type: model.ResourceUser, ID: invocation.Principal().UserID.String()},
		Operation:  "connect_provider",
		Value:      map[string]any{"provider": strings.ToLower(strings.TrimSpace(command.ProviderID))},
	})
	if err != nil {
		return nil, err
	}

	start, appErr := s.beginForPurpose(
		ctx,
		command.ProviderID,
		command.ReturnTo,
		model.SessionClientWeb,
		"",
		"",
		command.Source,
		model.ExternalAuthenticationPurposeConnect,
		invocation.Principal().UserID,
		auditID,
		"",
		"",
		nil,
	)
	if appErr != nil {
		if failErr := s.auditor.Fail(ctx, auditID, appErrorCode(appErr)); failErr != nil {
			return nil, failErr
		}
	}

	return start, appErr
}

func appErrorCode(err error) string {
	if appErr, ok := apperror.As(err); ok {
		return appErr.Code()
	}
	return "authentication.internal"
}

var authenticationMethodStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrAuthenticationMethodDisabled, Code: "authentication.method.disabled"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrLastUsableAuthenticationMethod, Code: "authentication.method.last_usable"},
		{Kind: apperror.StoreSentinel, Sentinel: store.ErrPasswordReused, Code: "authentication.password.reused"},
	},
	Constraints: map[string]apperror.Code{"external_identities_provider_subject_key": "authentication.method.provider_conflict"},
	NotFound:    "authentication.method.not_found",
	Conflict:    "authentication.method.conflict",
	Unavailable: "authentication.method.unavailable",
}

// BeginExternalAuthentication starts a browser sign-in with a provider,
// admitting through an Invitation when the command carries its claim.
func (s *Service) BeginExternalAuthentication(
	ctx context.Context,
	_ invocation.Invocation,
	command BeginExternalAuthenticationCommand,
) (*model.ExternalAuthenticationStart, error) {
	result, appErr := s.beginWithInvitationClaim(
		ctx,
		command.ProviderID,
		command.ReturnTo,
		command.ClientType,
		command.DeviceID,
		command.DeviceName,
		command.Source,
		command.InvitationClaim,
	)
	return result, appErr
}

func (s *Service) beginWithInvitationClaim(
	ctx context.Context,
	providerID, returnTo string,
	clientType model.SessionClientType,
	deviceID, deviceName, source, invitationClaim string,
) (*model.ExternalAuthenticationStart, error) {
	return s.beginForPurpose(
		ctx,
		providerID,
		returnTo,
		clientType,
		deviceID,
		deviceName,
		source,
		model.ExternalAuthenticationPurposeLogin,
		"",
		"",
		invitationClaim,
		"",
		nil,
	)
}

func (s *Service) beginForPurpose(
	ctx context.Context,
	providerID, returnTo string,
	clientType model.SessionClientType,
	deviceID, deviceName, source string,
	purpose model.ExternalAuthenticationPurpose,
	targetUserID model.UserID,
	auditEventID model.AuditEventID,
	invitationClaim string,
	browserTransactionID model.BrowserAuthenticationTransactionID,
	reauthentication *model.Principal,
) (*model.ExternalAuthenticationStart, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	provider, exists := s.externalProviders.Provider(providerID)
	if !exists {
		return nil, externalProviderNotFoundError()
	}

	admission, err := s.accessPolicy.ExternalProviderAdmission(ctx, providerID)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	if admission.Mode == "" {
		return nil, externalProviderNotFoundError()
	}
	if returnTo == "" {
		returnTo = "/"
	}

	if !model.IsSafeRelativeURL(returnTo) ||
		clientType != model.SessionClientWeb ||
		len(deviceID) > model.SessionDeviceIDMaxLength ||
		utf8.RuneCountInString(deviceName) > model.SessionDeviceNameMaxRunes {
		return nil, apperror.New("authentication.external.request.invalid")
	}

	if purpose == model.ExternalAuthenticationPurposeConnect && !targetUserID.IsValid() {
		return nil, access.InvalidTokenAppError()
	}
	if purpose == model.ExternalAuthenticationPurposeDesktopAuthorization && !browserTransactionID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	invitationClaimHash := ""
	if invitationClaim != "" {
		if purpose != model.ExternalAuthenticationPurposeLogin ||
			admission.Mode != model.ProviderAdmissionInvitationRequired ||
			!admission.InvitationAdmissionEnabled ||
			!model.IsValidCredentialToken(invitationClaim) {
			return nil, apperror.New("authentication.external.account_not_linked")
		}

		purpose = model.ExternalAuthenticationPurposeInvitationAdmission
		invitationClaimHash = model.HashInvitationClaim(invitationClaim)
	}

	if appErr := s.checkInitiationRateLimit(ctx, providerID, source); appErr != nil {
		return nil, appErr
	}

	stateToken := s.newCredential()
	bindingToken := s.newCredential()
	challenge, err := s.beginProviderChallenge(ctx, provider, providerID, purpose, stateToken, bindingToken)
	if err != nil {
		return nil, err
	}

	state := &model.ExternalLoginState{
		Provider:                           providerID,
		StateHash:                          model.HashToken(stateToken),
		Purpose:                            purpose,
		TargetUserID:                       targetUserID,
		AuditEventID:                       auditEventID.String(),
		BrowserAuthenticationTransactionID: browserTransactionID,
		BindingHash:                        model.HashToken(bindingToken),
		ReturnTo:                           returnTo,
		ClientType:                         clientType,
		DeviceID:                           deviceID,
		DeviceName:                         deviceName,
	}
	saved, err := s.saveExternalLoginState(ctx, state, reauthentication, invitationClaimHash)
	if err != nil {
		return nil, err
	}

	return &model.ExternalAuthenticationStart{
		RedirectURL: challenge.RedirectURL,
		Binding:     bindingToken,
		ExpiresAt:   saved.ExpiresAt.UnixMilli(),
	}, nil
}

// beginProviderChallenge asks the provider for the redirect challenge bound to the fresh state and binding tokens.
func (s *Service) beginProviderChallenge(
	ctx context.Context,
	provider ExternalIdentityProvider,
	providerID string,
	purpose model.ExternalAuthenticationPurpose,
	stateToken, bindingToken string,
) (*ExternalProviderBeginResponse, error) {
	callbackURL, err := externalAuthenticationCallbackURL(
		s.externalAuthenticationPolicy.PublicURL,
		providerID,
	)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	challenge, err := provider.Begin(
		ctx,
		ExternalProviderBeginRequest{
			CallbackURL: callbackURL,
			State:       stateToken,
			Proof:       bindingToken,
			FreshAuthentication: purpose == model.ExternalAuthenticationPurposeReauthenticate ||
				purpose == model.ExternalAuthenticationPurposeMFARecovery ||
				purpose == model.ExternalAuthenticationPurposeDesktopReauthentication,
		},
	)
	if err != nil {
		return nil, externalProviderOperationError(err)
	}
	if challenge == nil || challenge.RedirectURL == "" {
		return nil, authenticationUnavailable(errors.New("external provider returned an empty login challenge"))
	}

	return challenge, nil
}

// saveExternalLoginState binds any reauthenticated Session to the login state and persists it with its deadline.
func (s *Service) saveExternalLoginState(
	ctx context.Context, state *model.ExternalLoginState, reauthentication *model.Principal, invitationClaimHash string,
) (*model.ExternalLoginState, error) {
	var err error
	if reauthentication != nil {
		if state.Purpose != model.ExternalAuthenticationPurposeReauthenticate {
			return nil, access.InvalidTokenAppError()
		}

		state.SessionID = reauthentication.SessionID
		state.SessionCredentialID, err = model.ParseSessionCredentialID(reauthentication.CredentialID.String())
		if err != nil {
			return nil, access.InvalidTokenAppError()
		}

		state.ExternalIdentityID = reauthentication.ExternalIdentityID
	}

	var saved *model.ExternalLoginState
	if invitationClaimHash != "" {
		saved, err = s.externalLoginStates.SaveInvitationAdmission(ctx, &store.ExternalLoginStateInvitationAdmission{
			State:     state,
			Lifetime:  s.externalAuthenticationPolicy.LoginStateTTL,
			ClaimHash: invitationClaimHash,
		})
	} else {
		saved, err = s.externalLoginStates.Save(
			ctx,
			&store.ExternalLoginStateCreation{State: state, Lifetime: s.externalAuthenticationPolicy.LoginStateTTL},
		)
	}

	if err != nil {
		if invitationClaimHash != "" && store.IsNotFound(err) {
			return nil, apperror.New("authentication.external.account_not_linked")
		}
		return nil, authenticationUnavailable(err)
	}

	if saved == nil || saved.ExpiresAt.IsZero() {
		return nil, authenticationUnavailable(errors.New("external login state persistence returned an invalid deadline"))
	}

	return saved, nil
}

// CompleteExternalAuthentication finishes a provider callback for the purpose
// its login state was begun with.
func (s *Service) CompleteExternalAuthentication(
	ctx context.Context,
	invocation invocation.Invocation,
	command CompleteExternalAuthenticationCommand,
) (*model.ExternalAuthenticationCompletion, error) {
	result, appErr := s.completeExternalCallback(
		ctx,
		command.ProviderID,
		command.Binding,
		command.Callback,
		invocation.RequestMetadata(),
	)
	return result, appErr
}

// externalCompletionAttempt carries the consumed-state facts shared by the completion steps.
type externalCompletionAttempt struct {
	providerID  string
	method      string
	metadata    model.RequestMetadata
	institution *model.Institution
	state       *model.ExternalLoginState
}

func (s *Service) completeExternalCallback(
	ctx context.Context,
	providerID string,
	bindingToken string,
	callback model.ExternalAuthenticationCallback,
	metadata model.RequestMetadata,
) (completion *model.ExternalAuthenticationCompletion, completionErr error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	provider, exists := s.externalProviders.Provider(providerID)
	if !exists {
		return nil, externalProviderNotFoundError()
	}

	admission, err := s.accessPolicy.ExternalProviderAdmission(ctx, providerID)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	if admission.Mode == "" {
		return nil, externalProviderNotFoundError()
	}

	method := provider.Descriptor().Type
	state, stateToken, callbackURL, err := s.consumeCallbackLoginState(ctx, provider, providerID, bindingToken, callback)
	if err != nil {
		return nil, err
	}

	// Only a consumed, browser-bound state may select a hosted failure
	// continuation. No provider error or query value becomes a redirect target.
	defer func() {
		if completionErr != nil && state.Purpose == model.ExternalAuthenticationPurposeReauthenticate {
			task := "security"
			if state.ReturnTo == "/account/connect-provider" {
				task = "connect-provider"
			}

			completion = &model.ExternalAuthenticationCompletion{ReturnTo: "/account/reauthenticate?task=" + task + "#external_login=failed"}
			completionErr = nil
		}

		// The hosted Desktop reauthentication page reloads its own state;
		// the closed fragment only says this provider attempt did not count.
		if completionErr != nil &&
			state.Purpose == model.ExternalAuthenticationPurposeDesktopReauthentication &&
			model.IsSafeRelativeURL(state.ReturnTo) {
			completion = &model.ExternalAuthenticationCompletion{ReturnTo: state.ReturnTo + "#external_login=failed"}
			completionErr = nil
		}
	}()
	nowTime := s.now()

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, s.failConsumedProviderConnection(ctx, state, authenticationUnavailable(err))
	}

	attempt := externalCompletionAttempt{
		providerID:  providerID,
		method:      method,
		metadata:    metadata,
		institution: institution,
		state:       state,
	}
	assertion, err := s.completeProviderAssertion(ctx, provider, attempt, ExternalProviderCompleteRequest{
		CallbackURL: callbackURL,
		State:       stateToken,
		Proof:       bindingToken,
		Callback:    callback,
	})
	if err != nil {
		return nil, err
	}
	if state.Purpose == model.ExternalAuthenticationPurposeReauthenticate {
		return s.completeExternalReauthentication(ctx, state, assertion)
	}
	if state.Purpose == model.ExternalAuthenticationPurposeDesktopReauthentication {
		return s.completeDesktopReauthentication(ctx, attempt, assertion)
	}
	if state.Purpose == model.ExternalAuthenticationPurposeConnect {
		return s.completeProviderConnection(ctx, state, assertion, metadata, institution)
	}
	if state.Purpose == model.ExternalAuthenticationPurposeInvitationAdmission {
		return s.completeInvitationAdmission(ctx, attempt, admission, assertion)
	}

	resolution, err := s.resolveExternalIdentity(ctx, attempt, admission, provider, assertion, nowTime)
	if err != nil {
		return nil, err
	}

	recovery, recoveryErr := s.mfaRecoveryState(ctx, resolution.User.ID)
	if recoveryErr != nil {
		return nil, recoveryErr
	}
	if recovery.ResetAt.Valid && state.CreatedAt.Before(recovery.ResetAt.Time) {
		return nil, invalidExternalAuthenticationError()
	}

	if state.Purpose == model.ExternalAuthenticationPurposeMFARecovery &&
		(state.TargetUserID != resolution.User.ID ||
			assertion.AuthenticatedAt < state.CreatedAt.Truncate(time.Second).UnixMilli() ||
			assertion.AuthenticatedAt > s.now().UnixMilli()) {
		return nil, invalidExternalAuthenticationError()
	}

	if recovery.ReenrollmentRequired && state.Purpose != model.ExternalAuthenticationPurposeMFARecovery {
		start, startErr := s.beginForPurpose(
			ctx,
			providerID,
			"/account/security",
			model.SessionClientWeb,
			"",
			"",
			metadata.IPAddress,
			model.ExternalAuthenticationPurposeMFARecovery,
			resolution.User.ID,
			"",
			"",
			"",
			nil,
		)
		if startErr != nil {
			return nil, startErr
		}

		return &model.ExternalAuthenticationCompletion{Restart: start}, nil
	}

	if state.Purpose == model.ExternalAuthenticationPurposeDesktopAuthorization {
		if !state.BrowserAuthenticationTransactionID.IsValid() {
			return nil, authenticationUnavailable(errors.New("desktop authorization completion is unavailable"))
		}

		if appErr := s.authenticateExternal(
			ctx,
			state.BrowserAuthenticationTransactionID,
			resolution.User,
			resolution.Identity,
			method,
			assertion,
		); appErr != nil {
			return nil, appErr
		}

		return &model.ExternalAuthenticationCompletion{User: resolution.User, ReturnTo: state.ReturnTo}, nil
	}

	return s.issueExternalSession(ctx, attempt, assertion, resolution, recovery)
}

// consumeCallbackLoginState validates the callback credentials and consumes the matching browser-bound login state.
func (s *Service) consumeCallbackLoginState(
	ctx context.Context,
	provider ExternalIdentityProvider,
	providerID, bindingToken string,
	callback model.ExternalAuthenticationCallback,
) (*model.ExternalLoginState, string, string, error) {
	stateToken, providerErr := provider.State(callback)
	if providerErr != nil ||
		!validRawCredential(stateToken) ||
		!validRawCredential(bindingToken) {
		return nil, "", "", invalidExternalAuthenticationError()
	}

	stateHash := model.HashToken(stateToken)
	state, err := s.externalLoginStates.GetByStateHash(ctx, stateHash)
	if err != nil ||
		state == nil ||
		state.Provider != providerID ||
		state.ClientType != model.SessionClientWeb ||
		state.ConsumedAt.Valid {
		if err != nil && !store.IsNotFound(err) {
			return nil, "", "", authenticationUnavailable(err)
		}
		return nil, "", "", invalidExternalAuthenticationError()
	}

	callbackURL, err := externalAuthenticationCallbackURL(
		s.externalAuthenticationPolicy.PublicURL,
		providerID,
	)
	if err != nil {
		return nil, "", "", authenticationUnavailable(err)
	}

	state, err = s.externalLoginStates.Consume(
		ctx,
		&store.ExternalLoginStateConsumption{Provider: providerID, StateHash: stateHash, BindingHash: model.HashToken(bindingToken)},
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, "", "", invalidExternalAuthenticationError()
		}
		return nil, "", "", authenticationUnavailable(err)
	}

	return state, stateToken, callbackURL, nil
}

// completeProviderAssertion exchanges the callback with the provider and audits a rejected or mismatched assertion.
func (s *Service) completeProviderAssertion(
	ctx context.Context,
	provider ExternalIdentityProvider,
	attempt externalCompletionAttempt,
	request ExternalProviderCompleteRequest,
) (*model.ExternalAuthenticationAssertion, error) {
	state := attempt.state
	if state.Purpose == model.ExternalAuthenticationPurposeReauthenticate ||
		state.Purpose == model.ExternalAuthenticationPurposeMFARecovery ||
		state.Purpose == model.ExternalAuthenticationPurposeDesktopReauthentication {
		request.AuthenticationStartedAt = state.CreatedAt
	}

	assertion, providerErr := provider.Complete(ctx, request)
	if providerErr != nil {
		errorCode := apperror.Code("authentication.external.rejected")
		if !errors.Is(providerErr, ErrExternalAuthenticationRejected) {
			errorCode = "authentication.external.unavailable"
		}
		if appErr := s.recordExternalCompletionFailure(ctx, attempt, string(errorCode)); appErr != nil {
			return nil, s.failConsumedProviderConnection(ctx, state, appErr)
		}

		return nil, s.failConsumedProviderConnection(ctx, state, apperror.New(errorCode).Wrap(providerErr))
	}

	if assertion == nil || assertion.ProviderID != attempt.providerID {
		if auditErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.invalid_assertion"); auditErr != nil {
			return nil, s.failConsumedProviderConnection(ctx, state, auditErr)
		}
		return nil, s.failConsumedProviderConnection(
			ctx,
			state,
			authenticationUnavailable(errors.New("provider returned a mismatched assertion")),
		)
	}

	return assertion, nil
}

// completeInvitationAdmission accepts the Invitation bound to the login state with the verified external identity.
func (s *Service) completeInvitationAdmission(
	ctx context.Context,
	attempt externalCompletionAttempt,
	admission externalProviderAdmissionPolicy,
	assertion *model.ExternalAuthenticationAssertion,
) (*model.ExternalAuthenticationCompletion, error) {
	state := attempt.state
	if admission.Mode != model.ProviderAdmissionInvitationRequired || !admission.InvitationAdmissionEnabled {
		if auditErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.invalid"); auditErr != nil {
			return nil, auditErr
		}
		return nil, invalidExternalAuthenticationError()
	}

	if !state.InvitationID.IsValid() || s.invitations == nil {
		return nil, invalidExternalAuthenticationError()
	}

	providerEmail := strings.ToLower(strings.TrimSpace(model.SanitizeUnicode(assertion.Email)))
	if !assertion.EmailVerified || !model.IsValidEmail(providerEmail) {
		if auditErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.account_not_linked"); auditErr != nil {
			return nil, auditErr
		}
		return nil, apperror.New("authentication.external.account_not_linked")
	}

	result, acceptErr := s.invitations.AcceptExternalIdentity(
		ctx,
		state,
		assertion,
		AccessDeploymentCapabilities(s.capabilities.Snapshot()),
		attempt.metadata,
		attempt.method,
	)
	if acceptErr != nil {
		if errors.Is(acceptErr, store.ErrAuthenticationMethodDisabled) {
			if auditErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.invalid"); auditErr != nil {
				return nil, auditErr
			}
			return nil, invalidExternalAuthenticationError()
		}

		var conflict *store.ErrConflict
		if errors.As(acceptErr, &conflict) {
			return nil, apperror.New("authentication.external.account_conflict").Wrap(acceptErr)
		}
		if store.IsNotFound(acceptErr) {
			return nil, apperror.New("authentication.external.account_not_linked")
		}

		return nil, authenticationUnavailable(acceptErr)
	}

	if result == nil || result.User == nil || result.Identity == nil {
		return nil, authenticationUnavailable(errors.New("external Invitation acceptance returned invalid provenance"))
	}

	return &model.ExternalAuthenticationCompletion{User: result.User, ReturnTo: state.ReturnTo}, nil
}

// externalProvisioning is the prepared auto-provisioning payload for an unlinked external identity.
type externalProvisioning struct {
	user              *model.User
	defaultPictureJob *model.Job
	settings          *model.UserSettingsDocument
	audit             *model.AuditEvent
}

// prepareExternalProvisioning builds the User, settings, picture Job and audit event used when the provider auto-provisions.
func (s *Service) prepareExternalProvisioning(
	attempt externalCompletionAttempt, assertion *model.ExternalAuthenticationAssertion, nowTime time.Time,
) (externalProvisioning, error) {
	var provisioning externalProvisioning
	var err error
	provisioning.user = ExternalUserCandidate(assertion)
	provisioning.user, provisioning.defaultPictureJob, err = jobs.PrepareUserDefaultProfilePictureJob(provisioning.user, nowTime)
	if err != nil {
		return externalProvisioning{}, authenticationUnavailable(err)
	}

	provisioning.settings, err = user.PrepareInitialSettingsDocument(provisioning.user)
	if err != nil {
		return externalProvisioning{}, authenticationUnavailable(err)
	}

	provisionParameters, appErr := model.EncodeAuditData(map[string]string{
		"provider": attempt.providerID,
	})
	if appErr != nil {
		return externalProvisioning{}, appErr
	}

	provisioning.audit = &model.AuditEvent{
		Action:     string(model.ActionAuthenticationExternalProvision),
		ScopeType:  model.RoleScopeInstitution,
		ScopeID:    attempt.institution.ID.String(),
		Status:     model.AuditStatusSuccess,
		RequestID:  attempt.metadata.RequestID,
		NodeID:     s.externalAuthenticationPolicy.NodeID,
		ClientType: string(attempt.state.ClientType),
		AuthMethod: attempt.method,
		IPAddress:  attempt.metadata.IPAddress,
		UserAgent:  attempt.metadata.UserAgent,
		Parameters: provisionParameters,
	}
	return provisioning, nil
}

// resolveExternalIdentity resolves or provisions the linked User and rejects invalid provenance or inactive accounts.
func (s *Service) resolveExternalIdentity(
	ctx context.Context,
	attempt externalCompletionAttempt,
	admission externalProviderAdmissionPolicy,
	provider ExternalIdentityProvider,
	assertion *model.ExternalAuthenticationAssertion,
	nowTime time.Time,
) (*store.ExternalIdentityResolution, error) {
	now := nowTime.UnixMilli()
	var provisioning externalProvisioning
	autoProvision := attempt.state.Purpose != model.ExternalAuthenticationPurposeMFARecovery &&
		admission.Mode == model.ProviderAdmissionAutoProvision &&
		provider.AutoProvision()
	if autoProvision {
		var err error
		provisioning, err = s.prepareExternalProvisioning(attempt, assertion, nowTime)
		if err != nil {
			return nil, err
		}
	}

	resolution, err := s.externalIdentities.ResolveOrProvision(
		ctx, &store.ExternalIdentityResolutionRequest{
			Identity: &model.ExternalIdentity{
				Provider:   attempt.providerID,
				Subject:    assertion.Subject,
				LastSeenAt: model.OptionalTimeFromMillis(now),
			},
			User:                     provisioning.user,
			Settings:                 provisioning.settings,
			Capabilities:             AccessDeploymentCapabilities(s.capabilities.Snapshot()),
			AuditEvent:               provisioning.audit,
			DefaultProfilePictureJob: provisioning.defaultPictureJob,
		},
	)
	if err != nil {
		return nil, s.identityResolutionFailure(ctx, attempt, err)
	}

	if resolution == nil ||
		resolution.User == nil ||
		resolution.Identity == nil ||
		!resolution.Identity.ID.IsValid() ||
		resolution.Identity.UserID != resolution.User.ID ||
		resolution.Identity.Provider != attempt.providerID {
		if auditErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.internal"); auditErr != nil {
			return nil, auditErr
		}
		return nil, authenticationUnavailable(errors.New("external identity resolution returned invalid provenance"))
	}

	if !resolution.User.IsActive() {
		if appErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.inactive_account"); appErr != nil {
			return nil, appErr
		}
		return nil, invalidExternalAuthenticationError()
	}

	return resolution, nil
}

// identityResolutionFailure audits a failed identity resolution and maps it to the public error.
func (s *Service) identityResolutionFailure(ctx context.Context, attempt externalCompletionAttempt, err error) error {
	var conflict *store.ErrConflict
	errorCode := "authentication.external.internal"
	if errors.Is(err, store.ErrAuthenticationMethodDisabled) {
		errorCode = "authentication.external.invalid"
	} else if errors.As(err, &conflict) {
		errorCode = "authentication.external.account_conflict"
	} else if store.IsNotFound(err) {
		errorCode = "authentication.external.account_not_linked"
	}
	if auditErr := s.recordExternalCompletionFailure(ctx, attempt, errorCode); auditErr != nil {
		return auditErr
	}
	switch {
	case errors.Is(err, store.ErrAuthenticationMethodDisabled):
		return invalidExternalAuthenticationError()
	case errors.As(err, &conflict):
		return apperror.New("authentication.external.account_conflict").Wrap(err)
	case store.IsNotFound(err):
		return apperror.New("authentication.external.account_not_linked")
	default:
		return authenticationUnavailable(err)
	}
}

// issueExternalSession audits the authentication and issues the browser Session for the resolved User.
func (s *Service) issueExternalSession(
	ctx context.Context,
	attempt externalCompletionAttempt,
	assertion *model.ExternalAuthenticationAssertion,
	resolution *store.ExternalIdentityResolution,
	recovery *model.UserMFARecovery,
) (*model.ExternalAuthenticationCompletion, error) {
	state := attempt.state
	auditEvent, appErr := s.beginExternalAuthenticationAudit(ctx, attempt, resolution.User.ID)
	if appErr != nil {
		return nil, appErr
	}

	mfaCompletedAt := int64(0)
	if assertion.AuthenticationStrength == model.AuthenticationMultiFactor {
		mfaCompletedAt = assertion.AuthenticatedAt
	}

	session, tokens, sessionErr := s.createSession(
		ctx,
		sessionIssuance{
			User:                     resolution.User,
			ClientType:               state.ClientType,
			AuthenticationGeneration: recovery.Generation,
			MFARecoveryRequired:      recovery.ReenrollmentRequired,
			ExternalLoginStateID:     state.ID,
			DeviceID:                 state.DeviceID,
			DeviceName:               state.DeviceName,
			AuthenticationMethod:     attempt.method,
			AuthenticationProviderID: attempt.providerID,
			ExternalIdentityID:       resolution.Identity.ID,
			AuthenticationStrength:   assertion.AuthenticationStrength,
			AuthenticatedAt:          assertion.AuthenticatedAt,
			MFACompletedAt:           mfaCompletedAt,
			AuditEventID:             auditEvent.ID,
		},
	)
	if sessionErr != nil {
		legacy := sessionErr
		if completionErr := s.completeExternalAuthenticationAudit(
			ctx,
			auditEvent.ID,
			func() string {
				if f, ok := apperror.As(legacy); ok {
					return f.Code()
				}
				return "authentication.internal"
			}(),
		); completionErr != nil {
			return nil, completionErr
		}

		return nil, legacy
	}

	return &model.ExternalAuthenticationCompletion{
		User:     resolution.User,
		Session:  session,
		Tokens:   tokens,
		ReturnTo: state.ReturnTo,
	}, nil
}

func (s *Service) failConsumedProviderConnection(ctx context.Context, state *model.ExternalLoginState, failure error) error {
	if state == nil ||
		(state.Purpose != model.ExternalAuthenticationPurposeConnect && state.Purpose != model.ExternalAuthenticationPurposeReauthenticate) ||
		state.AuditEventID == "" {
		return failure
	}

	if err := s.auditor.Fail(ctx, model.AuditEventID(state.AuditEventID), appErrorCode(failure)); err != nil {
		return err
	}

	return failure
}

func (s *Service) completeProviderConnection(
	ctx context.Context,
	state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion,
	metadata model.RequestMetadata,
	institution *model.Institution,
) (*model.ExternalAuthenticationCompletion, error) {
	if state == nil ||
		state.Purpose != model.ExternalAuthenticationPurposeConnect ||
		!state.TargetUserID.IsValid() ||
		!state.ConsumedAt.Valid ||
		assertion == nil {
		return nil, invalidExternalAuthenticationError()
	}

	connectedAt := model.TimeUTC(state.ConsumedAt.Time)
	result, err := s.externalIdentities.Link(ctx, &store.ExternalIdentityLink{
		ExternalLoginStateID: state.ID,
		Identity: &model.ExternalIdentity{
			UserID:     state.TargetUserID,
			Provider:   state.Provider,
			Subject:    assertion.Subject,
			LastSeenAt: model.OptionalTimeFrom(connectedAt),
		},
		Capabilities: AccessDeploymentCapabilities(s.capabilities.Snapshot()),
		AuditEventID: model.AuditEventID(state.AuditEventID),
		AuditAt:      connectedAt,
	})
	if err != nil {
		mapped := authenticationMethodStoreErrors.Translate(err)
		if failErr := s.auditor.Fail(ctx, model.AuditEventID(state.AuditEventID), appErrorCode(mapped)); failErr != nil {
			return nil, failErr
		}

		return nil, mapped
	}

	_ = metadata
	_ = institution
	_ = result
	return &model.ExternalAuthenticationCompletion{ReturnTo: state.ReturnTo}, nil
}

func (s *Service) checkInitiationRateLimit(
	ctx context.Context,
	providerID string,
	source string,
) error {
	settings := s.externalAuthenticationPolicy.LoginRateLimit
	_, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose:   authenticationAttemptPurposeExternalAuthentication,
		qualifier: providerID,
		window:    settings.Window,
		limits: []AttemptLimit{{
			dimension: authenticationAttemptDimensionSource,
			maximum:   settings.MaximumSourceAttempts,
			source:    source,
		}},
	})
	if err != nil {
		return RateLimitUnavailableAppError(err)
	}
	if limited {
		return apperror.New("authentication.rate_limited")
	}
	return nil
}

// ExternalUserCandidate proposes the User an External Identity assertion would
// provision; Invitation acceptance reuses it.
func ExternalUserCandidate(
	assertion *model.ExternalAuthenticationAssertion,
) *model.User {
	displayName := strings.TrimSpace(assertion.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(
			assertion.FirstName + " " + assertion.LastName,
		)
	}

	return &model.User{
		Username:      assertion.Username,
		Email:         assertion.Email,
		EmailVerified: assertion.EmailVerified,
		DisplayName:   displayName,
		FirstName:     assertion.FirstName,
		LastName:      assertion.LastName,
	}
}

func externalAuthenticationCallbackURL(
	publicURL string,
	providerID string,
) (string, error) {
	callback, err := url.Parse(publicURL)
	if err != nil || callback.Host == "" {
		return "", errors.New("public URL is invalid")
	}
	callback.Path = strings.TrimSuffix(callback.Path, "/") +
		model.APIURLSuffix + "/auth/providers/" +
		url.PathEscape(providerID) + "/callback"
	callback.RawQuery = ""
	callback.Fragment = ""
	return callback.String(), nil
}

func externalProviderNotFoundError() error {
	return apperror.New("authentication.external.provider_not_found")
}

func invalidExternalAuthenticationError() error {
	return apperror.New("authentication.external.invalid")
}

func externalProviderOperationError(err error) error {
	if errors.Is(err, ErrExternalAuthenticationRejected) {
		return apperror.New("authentication.external.rejected").Wrap(err)
	}
	if errors.Is(err, ErrExternalAuthenticationUnavailable) ||
		errors.Is(err, ErrExternalAuthenticationInvalid) {
		return apperror.New("authentication.external.unavailable").Wrap(err)
	}
	return authenticationUnavailable(err)
}

// completeDesktopReauthentication hands verified provider proof of an
// already linked External Identity to its Desktop reauthentication. It never
// provisions a User, links an identity or issues a Session.
func (s *Service) completeDesktopReauthentication(
	ctx context.Context,
	attempt externalCompletionAttempt,
	assertion *model.ExternalAuthenticationAssertion,
) (*model.ExternalAuthenticationCompletion, error) {
	state := attempt.state
	if !state.BrowserAuthenticationTransactionID.IsValid() ||
		assertion.AuthenticatedAt < state.CreatedAt.Truncate(time.Second).UnixMilli() ||
		assertion.AuthenticatedAt > s.now().UnixMilli() {
		return nil, invalidExternalAuthenticationError()
	}

	identity, err := s.externalIdentities.GetByProviderSubject(ctx, attempt.providerID, assertion.Subject)
	if err != nil || identity == nil || identity.UserID.IsZero() {
		if err != nil && !store.IsNotFound(err) {
			return nil, authenticationUnavailable(err)
		}
		if auditErr := s.recordExternalCompletionFailure(ctx, attempt, "authentication.external.account_not_linked"); auditErr != nil {
			return nil, auditErr
		}

		return nil, apperror.New("authentication.external.account_not_linked")
	}

	if appErr := s.authenticateExternalReauthentication(
		ctx,
		invocation.New(model.Principal{}, attempt.metadata),
		state.BrowserAuthenticationTransactionID,
		identity,
		attempt.method,
		assertion,
	); appErr != nil {
		return nil, appErr
	}

	return &model.ExternalAuthenticationCompletion{ReturnTo: state.ReturnTo}, nil
}
