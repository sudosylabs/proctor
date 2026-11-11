// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"net/url"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type AuthenticateDesktopAuthorizationCommand struct{ Binding string }

type AuthenticateDesktopAuthorizationLocallyCommand struct {
	Binding  string
	LoginID  string
	Password string
	MFACode  string
	Source   string
}

// AuthenticateDesktopAuthorizationSession records the calling browser Web
// Session as the proof of the bound Desktop authorization.
func (s *Service) AuthenticateDesktopAuthorizationSession(
	ctx context.Context,
	invocation invocation.Invocation,
	command AuthenticateDesktopAuthorizationCommand,
) (*DesktopAuthorizationBrowserContext, error) {
	principal := invocation.Principal()
	if principal.MFAEnrollmentRequired {
		return nil, mfaEnrollmentRequiredAppError()
	}

	if principal.Validate() != nil ||
		principal.CredentialType != model.CredentialSessionAccess ||
		principal.ClientType != model.SessionClientWeb ||
		!model.IsValidCredentialToken(command.Binding) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	result, err := s.recordDesktopAuthorizationProof(ctx, command.Binding, store.DesktopAuthorizationAuthentication{
		SourceSessionID:          principal.SessionID,
		SourceCredentialID:       model.SessionCredentialID(principal.CredentialID),
		UserID:                   principal.UserID,
		AuthenticationMethod:     principal.AuthenticationMethod,
		AuthenticationProviderID: principal.AuthenticationProviderID,
		ExternalIdentityID:       principal.ExternalIdentityID,
		AuthenticationStrength:   principal.AuthenticationStrength,
		AuthenticatedAt:          principal.AuthenticatedAt,
		MFACompletedAt:           principal.MFACompletedAt,
	})
	if err != nil || result.Denied {
		return nil, err
	}

	return s.DesktopAuthorizationContext(ctx, invocation, command.Binding)
}

// AuthenticateDesktopAuthorizationLocally proves a local password, and a
// second factor when one is required, for the bound Desktop authorization
// without creating a Web Session.
func (s *Service) AuthenticateDesktopAuthorizationLocally(
	ctx context.Context,
	invocation invocation.Invocation,
	command AuthenticateDesktopAuthorizationLocallyCommand,
) (*DesktopAuthorizationBrowserContext, error) {
	if !model.IsValidCredentialToken(command.Binding) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	proof, err := s.authenticateLocal(ctx, LoginCommand{
		LoginID:    command.LoginID,
		Password:   command.Password,
		MFACode:    command.MFACode,
		ClientType: model.SessionClientWeb,
		Source:     command.Source,
	})
	if err != nil {
		return nil, err
	}
	if proof.MFARecoveryRequired {
		return nil, invalidCredentialsAppError()
	}

	result, err := s.recordDesktopAuthorizationProof(ctx, command.Binding, store.DesktopAuthorizationAuthentication{
		PasswordProof:          proof.PasswordProof,
		UserID:                 proof.User.ID,
		AuthenticationMethod:   "password",
		AuthenticationStrength: proof.AuthenticationStrength,
		AuthenticatedAt:        model.TimeFromMillis(proof.AuthenticatedAt),
		MFACompletedAt:         model.OptionalTimeFromMillis(proof.MFACompletedAt),
	})
	if err != nil {
		return nil, err
	}
	if !result.Denied {
		s.resetLocalAuthenticationAttempts(ctx, proof)
	}

	return s.DesktopAuthorizationContext(ctx, invocation, command.Binding)
}

func (s *Service) recordDesktopAuthorizationProof(
	ctx context.Context,
	binding string,
	input store.DesktopAuthorizationAuthentication,
) (*store.DesktopAuthorizationAuthenticationResult, error) {
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

func (s *Service) authenticateExternal(
	ctx context.Context,
	transactionID model.BrowserAuthenticationTransactionID,
	user *model.User,
	identity *model.ExternalIdentity,
	method string,
	assertion *model.ExternalAuthenticationAssertion,
) error {
	if !transactionID.IsValid() ||
		user == nil ||
		!user.IsActive() ||
		identity == nil ||
		identity.UserID != user.ID ||
		assertion == nil ||
		assertion.ProviderID != identity.Provider {
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
		TransactionID:            transactionID,
		UserID:                   user.ID,
		AuthenticationMethod:     method,
		AuthenticationProviderID: identity.Provider,
		ExternalIdentityID:       identity.ID,
		AuthenticationStrength:   assertion.AuthenticationStrength,
		AuthenticatedAt:          authenticatedAt,
		MFACompletedAt:           mfaCompletedAt,
		Capabilities:             AccessDeploymentCapabilities(s.capabilities.Snapshot()),
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

	if err := s.browserAuthentications.ResetDesktopAuthorizationAccount(
		ctx,
		&store.DesktopAuthorizationAccountReset{BindingHash: model.HashToken(binding)},
	); err != nil {
		return desktopAuthorizationStoreErrors.Translate(err)
	}

	return nil
}
