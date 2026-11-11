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
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type desktopExternalAuthenticationTarget struct {
	transactionID model.BrowserAuthenticationTransactionID
	returnTo      string
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
