// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Service) pauseDesktopReauthenticationIfRequired(
	ctx context.Context, current *store.DesktopReauthenticationContext,
	proof *store.DesktopReauthenticationAuthentication, returnTo string,
) (bool, error) {
	if current.AuthenticationStrength != model.AuthenticationSingleFactor {
		return false, nil
	}
	required, err := s.requiresLocalSecondFactor(ctx, proof.UserID)
	if err != nil || !required {
		return false, err
	}

	bindingHash := proof.BindingHash
	if bindingHash == "" {
		bindingHash = current.BindingHash
	}
	_, err = s.externalLoginStates.CreateExternalSecondFactorState(ctx, &store.ExternalSecondFactorStateCreation{
		State: &model.ExternalSecondFactorState{
			Purpose: model.ExternalAuthenticationPurposeDesktopReauthentication, UserID: proof.UserID,
			ExternalIdentityID: proof.ExternalIdentityID, Provider: proof.ProviderID, AuthenticationMethod: proof.AuthenticationMethod,
			BrowserAuthenticationTransactionID: current.ID, BindingHash: bindingHash,
			AuthenticationGeneration: current.AuthenticationGeneration, PrimaryAuthenticatedAt: proof.AuthenticatedAt,
			ReturnTo: returnTo, ClientType: model.SessionClientDesktop,
			PasswordCredentialID: proof.PasswordProof.ID, PasswordCredentialRevision: proof.PasswordProof.Revision,
		},
		Lifetime: s.externalAuthenticationPolicy.LoginStateTTL,
	})
	if err != nil {
		return false, desktopReauthenticationStoreErrors.Translate(err)
	}
	return true, nil
}

// AuthenticateDesktopReauthenticationSecondFactor proves the enrolled factor for a paused Desktop handoff.
func (s *Service) AuthenticateDesktopReauthenticationSecondFactor(
	ctx context.Context, inv invocation.Invocation, command CompleteExternalSecondFactorCommand,
) (*DesktopReauthenticationBrowserContext, error) {
	current, err := s.boundDesktopReauthentication(ctx, command.Binding)
	if err != nil {
		return nil, err
	}
	if current.State != model.BrowserAuthenticationStateSecondFactorRequired {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	state, err := s.boundExternalSecondFactor(ctx, command.Binding, true)
	if err != nil {
		return nil, err
	}
	if state.BrowserAuthenticationTransactionID != current.ID || state.UserID != current.UserID {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}
	consumed, err := s.verifyAndConsumeExternalSecondFactor(ctx, inv, command, state)
	if err != nil {
		return nil, err
	}

	_, err = s.recordDesktopReauthenticationProof(ctx, inv, &store.DesktopReauthenticationAuthentication{
		BindingHash: consumed.BindingHash, UserID: consumed.UserID, AuthenticationMethod: consumed.AuthenticationMethod,
		ProviderID: consumed.Provider, ExternalIdentityID: consumed.ExternalIdentityID,
		PasswordProof:   store.PasswordCredentialProof{ID: consumed.PasswordCredentialID, Revision: consumed.PasswordCredentialRevision},
		AuthenticatedAt: consumed.PrimaryAuthenticatedAt, MFACompletedAt: consumed.ConsumedAt,
	})
	if err != nil {
		return nil, err
	}
	return s.DesktopReauthenticationContext(ctx, inv, command.Binding)
}
