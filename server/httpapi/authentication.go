// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/user.go login/logout handlers,
// its session-authenticated boundary, and browser session-cookie behavior.
// Proctor keeps explicit catalog classification and an immutable request
// principal without Mattermost's product-specific handler context.

package httpapi

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type loginRequest struct {
	LoginID    string                  `json:"login_id"`
	Password   string                  `json:"password"`
	ClientType model.SessionClientType `json:"client_type"`
	DeviceID   string                  `json:"device_id,omitempty"`
	DeviceName string                  `json:"device_name,omitempty"`
	MFACode    string                  `json:"mfa_code,omitempty"`
}

type publicRegistrationRequest struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Password  string `json:"password"`
}

type passwordResetRequest struct {
	Email string `json:"email"`
}

type passwordResetCompletion struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type emailVerificationCompletion struct {
	Token string `json:"token"`
}

type emailChangeUndoCompletion struct {
	Token string `json:"token"`
}

// accountTokenInspectionRequest carries a fragment credential in the body so
// it never enters a URL, access log, or Referer.
type accountTokenInspectionRequest struct {
	Token string `json:"token"`
}

type emailVerificationInspectionResponse struct {
	Email string `json:"email"`
}

type emailChangeUndoInspectionResponse struct {
	RestoredEmail      string `json:"restored_email"`
	MaskedCurrentEmail string `json:"masked_current_email"`
	ChangedAt          string `json:"changed_at"`
}

// authenticationResponse is the transport-owned login/refresh success body.
// Field names match the historical v1 envelope so cookie and bearer clients
// keep working while domain models no longer serialize directly.
type authenticationResponse struct {
	User    *userProfileResponse          `json:"user,omitempty"`
	Session *sessionResponse              `json:"session"`
	Tokens  *authenticationTokensResponse `json:"tokens,omitempty"`
}

type sessionResponse struct {
	ID                      string `json:"id"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
	ArchivedAt              string `json:"archived_at,omitempty"`
	UserID                  string `json:"user_id"`
	MFARecoveryRequired     bool   `json:"mfa_recovery_required"`
	MFAEnrollmentRequired   bool   `json:"mfa_enrollment_required"`
	ClientType              string `json:"client_type"`
	DesktopRegistrationID   string `json:"desktop_registration_id,omitempty"`
	DesktopRelease          string `json:"desktop_release,omitempty"`
	DesktopBuildID          string `json:"desktop_build_id,omitempty"`
	DesktopPlatform         string `json:"desktop_platform,omitempty"`
	DesktopArchitecture     string `json:"desktop_architecture,omitempty"`
	DesktopRealtimeProtocol int    `json:"desktop_realtime_protocol,omitempty"`
	DeviceID                string `json:"device_id,omitempty"`
	DeviceName              string `json:"device_name,omitempty"`
	AuthenticationMethod    string `json:"authentication_method"`
	AuthenticationStrength  string `json:"authentication_strength"`
	AuthenticatedAt         string `json:"authenticated_at"`
	ReauthenticatedAt       string `json:"reauthenticated_at,omitempty"`
	MFACompletedAt          string `json:"mfa_completed_at,omitempty"`
	LastActivityAt          string `json:"last_activity_at"`
	IdleExpiresAt           string `json:"idle_expires_at"`
	ExpiresAt               string `json:"expires_at"`
	RevokedAt               string `json:"revoked_at,omitempty"`
	RevocationReasonCode    string `json:"revocation_reason_code,omitempty"`
	RevocationReason        string `json:"revocation_reason,omitempty"`
}

type authenticationTokensResponse struct {
	TokenType        string `json:"token_type"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	AccessExpiresAt  string `json:"access_expires_at"`
	RefreshExpiresAt string `json:"refresh_expires_at"`
}

func sessionResponseFromModel(request *http.Request, session *model.Session) *sessionResponse {
	if session == nil {
		return nil
	}
	return &sessionResponse{
		ID:                      session.ID.String(),
		CreatedAt:               model.FormatRFC3339(session.CreatedAt),
		UpdatedAt:               model.FormatRFC3339(session.UpdatedAt),
		ArchivedAt:              session.ArchivedAt.FormatRFC3339(),
		UserID:                  session.UserID.String(),
		MFARecoveryRequired:     session.MFARecoveryRequired,
		MFAEnrollmentRequired:   session.MFAEnrollmentRequired,
		ClientType:              string(session.ClientType),
		DesktopRegistrationID:   session.DesktopRegistrationID.String(),
		DesktopRelease:          session.DesktopRelease,
		DesktopBuildID:          session.DesktopBuildID,
		DesktopPlatform:         string(session.DesktopPlatform),
		DesktopArchitecture:     string(session.DesktopArchitecture),
		DesktopRealtimeProtocol: session.DesktopRealtimeProtocol,
		DeviceID:                session.DeviceID,
		DeviceName:              session.DeviceName,
		AuthenticationMethod:    session.AuthenticationMethod,
		AuthenticationStrength:  string(session.AuthenticationStrength),
		AuthenticatedAt:         model.FormatRFC3339(session.AuthenticatedAt),
		ReauthenticatedAt:       session.ReauthenticatedAt.FormatRFC3339(),
		MFACompletedAt:          session.MFACompletedAt.FormatRFC3339(),
		LastActivityAt:          model.FormatRFC3339(session.LastActivityAt),
		IdleExpiresAt:           model.FormatRFC3339(session.IdleExpiresAt),
		ExpiresAt:               model.FormatRFC3339(session.ExpiresAt),
		RevokedAt:               session.RevokedAt.FormatRFC3339(),
		RevocationReasonCode:    string(session.RevocationReason),
		RevocationReason:        localizedSessionRevocationReason(request, session.RevocationReason),
	}
}

func localizedSessionRevocationReason(request *http.Request, reason model.SessionRevocationReason) string {
	if !reason.IsValid() {
		return ""
	}
	fallback := sessionRevocationReasonPresentation(reason)
	return route.Translate(request, sessionRevocationLocalizationID(reason), fallback)
}

func sessionRevocationReasonPresentation(reason model.SessionRevocationReason) string {
	switch reason {
	case model.SessionRevocationUserLogout:
		return "Signed out by the user."
	case model.SessionRevocationUserSession:
		return "Revoked by the user."
	case model.SessionRevocationUserAllSessions:
		return "All sessions were revoked by the user."
	case model.SessionRevocationUserOtherSessions:
		return "Signed out from another session by the user."
	case model.SessionRevocationAdministratorSession:
		return "Revoked by an administrator."
	case model.SessionRevocationAdministratorAllSessions:
		return "All sessions were revoked by an administrator."
	case model.SessionRevocationAccountDisabled:
		return "The account was disabled."
	case model.SessionRevocationPasswordRemoved:
		return "Password authentication was removed."
	case model.SessionRevocationExternalIdentityUnlinked:
		return "The external identity was unlinked."
	case model.SessionRevocationMFAReset:
		return "Multi-factor authentication was reset."
	case model.SessionRevocationPasswordReset:
		return "The password was reset."
	case model.SessionRevocationPasswordChanged:
		return "The password was changed from another session."
	case model.SessionRevocationEmailChangeUndone:
		return "An email change was undone from the previous address."
	case model.SessionRevocationRefreshReplay:
		return "Refresh credential replay was detected."
	case model.SessionRevocationInactiveUser:
		return "The account is inactive."
	case model.SessionRevocationAuthenticationAuditFailed:
		return "Authentication audit completion failed."
	case model.SessionRevocationAccessPolicyChanged:
		return "The authentication access policy changed."
	case model.SessionRevocationDesktopRegistration:
		return "The Desktop Registration was revoked."
	case model.SessionRevocationAttemptSessionLock:
		return "Signed out when an exam attempt became active."
	default:
		return ""
	}
}

func authenticationTokensResponseFromModel(tokens *model.AuthenticationTokens) *authenticationTokensResponse {
	if tokens == nil {
		return nil
	}
	return &authenticationTokensResponse{
		TokenType:        tokens.TokenType,
		AccessToken:      tokens.AccessToken,
		RefreshToken:     tokens.RefreshToken,
		AccessExpiresAt:  model.FormatRFC3339(tokens.AccessExpiresAt),
		RefreshExpiresAt: model.FormatRFC3339(tokens.RefreshExpiresAt),
	}
}

func authenticationResponseFromLogin(request *http.Request, result *identity.LoginResult) authenticationResponse {
	if result == nil {
		return authenticationResponse{}
	}
	var user *userProfileResponse
	if result.User != nil {
		mapped := userProfileResponseFromModel(result.User)
		user = &mapped
	}
	return authenticationResponse{
		User:    user,
		Session: sessionResponseFromModel(request, result.Session),
		Tokens:  authenticationTokensResponseFromModel(result.Tokens),
	}
}

func authenticationResponseFromRefresh(
	request *http.Request,
	session *model.Session,
	tokens *model.AuthenticationTokens,
) authenticationResponse {
	return authenticationResponse{
		Session: sessionResponseFromModel(request, session),
		Tokens:  authenticationTokensResponseFromModel(tokens),
	}
}

// RegistrationApplication is public self-registration, which the onboarding
// area owns beside Invitations.
type RegistrationApplication interface {
	RegisterLocalUser(context.Context, invocation.Invocation, onboarding.RegisterLocalUserCommand) error
}

type authenticationEntryApplication interface {
	Login(context.Context, invocation.Invocation, identity.LoginCommand) (*identity.LoginResult, error)
	RefreshSession(context.Context, invocation.Invocation, identity.RefreshSessionCommand) (*model.Session, *model.AuthenticationTokens, error)
	Logout(context.Context, invocation.Invocation) error
	RequestEmailVerification(context.Context, invocation.Invocation, identity.RequestEmailVerificationCommand) error
	CompleteEmailVerification(context.Context, invocation.Invocation, identity.CompleteEmailVerificationCommand) (*model.User, error)
	CompleteEmailChangeUndo(context.Context, invocation.Invocation, identity.CompleteEmailChangeUndoCommand) error
	InspectEmailVerification(
		context.Context,
		invocation.Invocation,
		identity.InspectAccountTokenCommand,
	) (*identity.EmailVerificationInspection, error)
	InspectEmailChangeUndo(
		context.Context,
		invocation.Invocation,
		identity.InspectAccountTokenCommand,
	) (*identity.EmailChangeUndoInspection, error)
	RequestPasswordReset(context.Context, invocation.Invocation, identity.RequestPasswordResetCommand) error
	CompletePasswordReset(context.Context, invocation.Invocation, identity.CompletePasswordResetCommand) (*model.User, error)
}

type authenticationHandlers struct {
	registration   RegistrationApplication
	authentication authenticationEntryApplication
	cookies        route.Cookies
}

func authenticationResource(
	registration RegistrationApplication,
	authentication authenticationEntryApplication,
	cookies route.Cookies,
) route.Resource {
	h := authenticationHandlers{registration: registration, authentication: authentication, cookies: cookies}
	return route.NewResource(
		"authentication",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/register",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.password.invalid",
				"authentication.password.pattern_invalid",
				"authentication.password.common_invalid",
				"authentication.password.identity_invalid",
				"service.busy",
				"authentication.registration.invalid",
				"authentication.registration.invitation_required",
				"authentication.registration.unavailable",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
			},
			Handle: h.register,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/login",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.mfa.enrollment_required",
				"authentication.client_type.invalid",
				"authentication.password.invalid",
				"service.busy",
				"authentication.credentials_invalid",
				"authentication.mfa.required",
				"authentication.mfa.code_invalid",
				"authentication.mfa.unavailable",
				"authentication.sessions.maximum_reached",
				"authentication.desktop_authorization.account_session_locked",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.internal",
				"audit.unavailable",
				"authentication.external.invalid",
				"authentication.session.invalid",
			},
			Handle: h.login,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/refresh",
			Auth:   route.AuthRefreshCredentialRequired,
			Errors: []string{
				"authentication.token_invalid",
				"authentication.mfa.enrollment_required",
				"authentication.internal",
				"authentication.dpop.invalid",
				"authentication.dpop.replayed",
				"authentication.dpop.use_nonce",
				"authentication.dpop.unavailable",
			},
			Handle: h.refresh,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/logout",
			Auth:   route.AuthMFARecoverySessionRequired,
			Errors: []string{"authentication.internal", "audit.unavailable"},
			Handle: h.logout,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/email-verification/request",
			Auth:   route.AuthSessionRequired,
			Errors: sessionAuthenticationMutationErrorCodes(
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.account_recovery.unavailable",
			),
			Handle: h.requestEmailVerification,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/email-verification/complete",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.account_token.invalid",
				"authentication.account_recovery.unavailable",
			},
			Handle: h.completeEmailVerification,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/email-verification/inspect",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.account_token.invalid",
				"authentication.account_recovery.unavailable",
			},
			Handle: h.inspectEmailVerification,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/email-change/undo",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.account_token.invalid",
				"authentication.account_recovery.unavailable",
				"authentication.email_change_undo.conflict",
			},
			Handle: h.completeEmailChangeUndo,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/email-change/undo/inspect",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.account_token.invalid",
				"authentication.account_recovery.unavailable",
			},
			Handle: h.inspectEmailChangeUndo,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/password-reset/request",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.account_recovery.unavailable",
			},
			Handle: h.requestPasswordReset,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/auth/password-reset/complete",
			Auth:   route.AuthPublic,
			Errors: []string{
				"request.invalid",
				"authentication.password.invalid",
				"authentication.password.pattern_invalid",
				"authentication.password.common_invalid",
				"authentication.password.identity_invalid",
				"authentication.password.reused",
				"service.busy",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.account_token.invalid",
				"authentication.account_recovery.unavailable",
			},
			Handle: h.completePasswordReset,
		},
	)
}

func (h authenticationHandlers) register(request route.Request) (route.Result, error) {
	var input publicRegistrationRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	if err := h.registration.RegisterLocalUser(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		onboarding.RegisterLocalUserCommand{
			Username:  input.Username,
			Email:     input.Email,
			FirstName: input.FirstName,
			LastName:  input.LastName,
			Password:  input.Password,
			Source:    request.Metadata.IPAddress,
		},
	); err != nil {
		return route.Result{}, err
	}

	return route.Status(http.StatusAccepted).WithHeaders(http.Header{"Cache-Control": {"no-store"}}), nil
}

func sessionAuthenticationMutationErrorCodes(extra ...string) []string {
	return append([]string(nil), extra...)
}

func (h authenticationHandlers) login(request route.Request) (route.Result, error) {
	var input loginRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	result, err := h.authentication.Login(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.LoginCommand{
			LoginID:    input.LoginID,
			Password:   input.Password,
			ClientType: input.ClientType,
			DeviceID:   input.DeviceID,
			DeviceName: input.DeviceName,
			MFACode:    input.MFACode,
			Source:     request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	response := authenticationResponseFromLogin(request.HTTP, result)
	headers := http.Header{"Cache-Control": {"no-store"}}
	if usesBrowserCookieTransport(input.ClientType) {
		headers = route.CombineHeaders(headers, route.CaptureHeaders(func(writer http.ResponseWriter) {
			if result != nil {
				h.cookies.Attach(writer, result.Tokens)
			}
		}))
		response.Tokens = nil
	}

	return route.JSON(http.StatusOK, response).WithHeaders(headers), nil
}

func (h authenticationHandlers) refresh(request route.Request) (route.Result, error) {
	credential, ok := route.RefreshCredential(request.Context)
	if !ok {
		return route.Result{}, route.AuthenticationRequired()
	}

	command := identity.RefreshSessionCommand{RefreshToken: credential.Token}
	if credential.Source == route.CredentialDPoP {
		proof, proofErr := route.DPoPProof(request.HTTP)
		if proofErr != nil {
			return route.Result{}, route.ErrorWithHeaders(proofErr, dpopErrorHeaders(proofErr))
		}

		command.DPoP = &identity.DPoPRequestProof{
			Proof:  proof,
			Method: request.HTTP.Method,
			Path:   request.HTTP.URL.EscapedPath(),
		}
	}

	session, tokens, err := h.authentication.RefreshSession(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		command,
	)
	if err != nil {
		headers := http.Header{}
		if command.DPoP != nil {
			headers = dpopErrorHeaders(err)
		}
		if credential.Source == route.CredentialCookie {
			headers = route.CombineHeaders(headers, route.CaptureHeaders(h.cookies.Clear))
		}

		return route.Result{}, route.ErrorWithHeaders(err, headers)
	}

	response := authenticationResponseFromRefresh(request.HTTP, session, tokens)
	headers := http.Header{"Cache-Control": {"no-store"}}
	if credential.Source == route.CredentialCookie {
		response.Tokens = nil
		headers = route.CombineHeaders(headers, route.CaptureHeaders(func(writer http.ResponseWriter) {
			h.cookies.Attach(writer, tokens)
		}))
	}

	return route.JSON(http.StatusOK, response).WithHeaders(headers), nil
}

func dpopErrorHeaders(err error) http.Header {
	headers := http.Header{"WWW-Authenticate": {`DPoP error="invalid_dpop_proof"`}}
	if nonce, ok := identity.DPoPChallengeNonce(err); ok {
		headers.Set("DPoP-Nonce", nonce)
		headers.Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
	}
	return headers
}

func (h authenticationHandlers) logout(request route.Request) (route.Result, error) {
	if err := h.authentication.Logout(request.Context, request.Invocation()); err != nil {
		return route.Result{}, err
	}
	result := route.NoContent().WithHeaders(http.Header{"Cache-Control": {"no-store"}})
	if route.CredentialSourceOf(request.Context) == route.CredentialCookie {
		result = result.WithHeaders(route.CombineHeaders(result.Headers(), route.CaptureHeaders(h.cookies.Clear)))
	}
	return result, nil
}

func (h authenticationHandlers) requestEmailVerification(request route.Request) (route.Result, error) {
	err := h.authentication.RequestEmailVerification(
		request.Context,
		request.Invocation(),
		identity.RequestEmailVerificationCommand{Source: request.Metadata.IPAddress},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.Status(http.StatusAccepted), nil
}

func (h authenticationHandlers) completeEmailVerification(request route.Request) (route.Result, error) {
	var input emailVerificationCompletion
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}
	_, err := h.authentication.CompleteEmailVerification(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.CompleteEmailVerificationCommand{Token: input.Token, Source: request.Metadata.IPAddress},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func (h authenticationHandlers) completeEmailChangeUndo(request route.Request) (route.Result, error) {
	var input emailChangeUndoCompletion
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	if err := h.authentication.CompleteEmailChangeUndo(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.CompleteEmailChangeUndoCommand{Token: input.Token, Source: request.Metadata.IPAddress},
	); err != nil {
		return route.Result{}, err
	}

	return route.NoContent(), nil
}

func (h authenticationHandlers) inspectEmailVerification(request route.Request) (route.Result, error) {
	var input accountTokenInspectionRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	inspected, err := h.authentication.InspectEmailVerification(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.InspectAccountTokenCommand{Token: input.Token, Source: request.Metadata.IPAddress},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, emailVerificationInspectionResponse{Email: inspected.Email}).WithHeaders(noStoreHeaders()), nil
}

func (h authenticationHandlers) inspectEmailChangeUndo(request route.Request) (route.Result, error) {
	var input accountTokenInspectionRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	inspected, err := h.authentication.InspectEmailChangeUndo(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.InspectAccountTokenCommand{Token: input.Token, Source: request.Metadata.IPAddress},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, emailChangeUndoInspectionResponse{
		RestoredEmail:      inspected.RestoredEmail,
		MaskedCurrentEmail: inspected.MaskedCurrentEmail,
		ChangedAt:          model.FormatRFC3339(inspected.ChangedAt),
	}).WithHeaders(noStoreHeaders()), nil
}

func (h authenticationHandlers) requestPasswordReset(request route.Request) (route.Result, error) {
	var input passwordResetRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}
	if err := h.authentication.RequestPasswordReset(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.RequestPasswordResetCommand{Email: input.Email, Source: request.Metadata.IPAddress},
	); err != nil {
		return route.Result{}, err
	}
	return route.Status(http.StatusAccepted), nil
}

func (h authenticationHandlers) completePasswordReset(request route.Request) (route.Result, error) {
	var input passwordResetCompletion
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	_, err := h.authentication.CompletePasswordReset(
		request.Context,
		invocation.New(model.Principal{}, request.Metadata),
		identity.CompletePasswordResetCommand{
			Token:    input.Token,
			Password: input.Password,
			Source:   request.Metadata.IPAddress,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.NoContent().WithHeaders(route.CaptureHeaders(h.cookies.Clear)), nil
}

func usesBrowserCookieTransport(clientType model.SessionClientType) bool {
	return clientType == model.SessionClientDesktop ||
		clientType == model.SessionClientWeb
}
