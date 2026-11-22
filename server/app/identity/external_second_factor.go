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
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const externalSecondFactorExpired apperror.Code = "authentication.external.second_factor_expired"

type ExternalSecondFactorContext struct {
	Purpose             model.ExternalAuthenticationPurpose
	UserDisplayName     string
	ProviderDisplayName string
	ExpiresAt           time.Time
}

type CompleteExternalSecondFactorCommand struct {
	Binding string
	Code    string
	Source  string
}

type ExternalSecondFactorCompletion struct {
	ReturnTo             string
	Session              *model.Session
	Tokens               *model.AuthenticationTokens
	DesktopAuthorization *DesktopAuthorizationBrowserContext
}

func (s *Service) requiresLocalSecondFactor(ctx context.Context, userID model.UserID) (bool, error) {
	credential, err := s.mfaCredentials.GetByUser(ctx, userID)
	if store.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, authenticationUnavailable(err)
	}
	if !credential.IsActive() {
		return false, nil
	}
	if !s.mfa.deps.Settings.Enabled {
		return false, apperror.New("authentication.mfa.unavailable")
	}
	return true, nil
}

func (s *Service) pauseExternalSignIn(
	ctx context.Context, attempt externalCompletionAttempt, resolution *store.ExternalIdentityResolution,
	assertion *model.ExternalAuthenticationAssertion, generation int64,
) (*model.ExternalAuthenticationCompletion, error) {
	binding := s.newCredential()
	if !model.IsValidCredentialToken(binding) {
		return nil, authenticationUnavailable(errors.New("invalid second factor binding generated"))
	}

	login := attempt.state
	state := &model.ExternalSecondFactorState{
		Purpose: model.ExternalAuthenticationPurposeLogin, UserID: resolution.User.ID,
		ExternalIdentityID: resolution.Identity.ID, Provider: attempt.providerID, AuthenticationMethod: attempt.method,
		BindingHash: model.HashToken(binding), AuthenticationGeneration: generation,
		PrimaryAuthenticatedAt: model.TimeFromMillis(assertion.AuthenticatedAt),
		ReturnTo:               login.ReturnTo, ClientType: model.SessionClientWeb,
		DeviceID: login.DeviceID, DeviceName: login.DeviceName, ExternalLoginStateID: login.ID,
	}
	redirect := "/login/verify"
	if login.Purpose == model.ExternalAuthenticationPurposeDesktopAuthorization {
		query, ok := strings.CutPrefix(login.ReturnTo, "/authorize/desktop?state=")
		if !ok || !model.IsValidCredentialToken(query) {
			return nil, invalidExternalAuthenticationError()
		}
		redirect = "/authorize/desktop/verify?state=" + query
		state.Purpose = login.Purpose
		state.ClientType = model.SessionClientDesktop
		state.BrowserAuthenticationTransactionID = login.BrowserAuthenticationTransactionID
		state.ExternalLoginStateID = ""
	}

	audit, err := s.beginExternalAuthenticationAudit(ctx, attempt, state.UserID)
	if err != nil {
		return nil, err
	}
	state.AuditEventID = audit.ID
	saved, err := s.externalLoginStates.CreateExternalSecondFactorState(ctx, &store.ExternalSecondFactorStateCreation{
		State: state, Lifetime: s.externalAuthenticationPolicy.LoginStateTTL,
	})
	if err != nil || saved == nil {
		return nil, s.failExternalSecondFactorSignIn(ctx, audit.ID, externalSecondFactorStoreErrors.Translate(err))
	}

	return &model.ExternalAuthenticationCompletion{ReturnTo: redirect, SecondFactor: &model.ExternalSecondFactorBinding{
		Binding: binding, ExpiresAt: saved.ExpiresAt.UnixMilli(),
	}}, nil
}

func (s *Service) ExternalSecondFactorContext(
	ctx context.Context, _ invocation.Invocation, binding string,
) (*ExternalSecondFactorContext, error) {
	state, err := s.boundExternalSecondFactor(ctx, binding, false)
	if err != nil {
		return nil, err
	}
	user, err := s.users.Get(ctx, state.UserID)
	if err != nil {
		return nil, externalSecondFactorStoreErrors.Translate(err)
	}
	if !user.IsActive() {
		return nil, invalidExternalAuthenticationError()
	}
	provider, ok := s.externalProviders.Provider(state.Provider)
	if !ok {
		return nil, invalidExternalAuthenticationError()
	}
	return &ExternalSecondFactorContext{Purpose: state.Purpose, UserDisplayName: user.DisplayName,
		ProviderDisplayName: provider.Descriptor().DisplayName, ExpiresAt: state.ExpiresAt}, nil
}

func (s *Service) boundExternalSecondFactor(
	ctx context.Context, binding string, reauthentication bool,
) (*model.ExternalSecondFactorState, error) {
	if !model.IsValidCredentialToken(binding) {
		return nil, invalidExternalAuthenticationError()
	}
	bindingHash := model.HashToken(binding)
	state, err := s.externalLoginStates.GetExternalSecondFactorStateByBinding(ctx, bindingHash, s.now())
	if store.IsNotFound(err) {
		expired, expireErr := s.externalLoginStates.ExpireExternalSecondFactorState(ctx, bindingHash)
		if expireErr != nil {
			return nil, externalSecondFactorStoreErrors.Translate(expireErr)
		}
		if expired {
			return nil, apperror.New(externalSecondFactorExpired)
		}
	}
	if err != nil || state == nil {
		return nil, externalSecondFactorStoreErrors.Translate(err)
	}
	if (state.Purpose == model.ExternalAuthenticationPurposeDesktopReauthentication) != reauthentication {
		return nil, invalidExternalAuthenticationError()
	}
	return state, nil
}

func (s *Service) CompleteExternalSecondFactor(
	ctx context.Context, inv invocation.Invocation, command CompleteExternalSecondFactorCommand,
) (*ExternalSecondFactorCompletion, error) {
	state, err := s.boundExternalSecondFactor(ctx, command.Binding, false)
	if err != nil {
		return nil, err
	}
	consumed, err := s.verifyAndConsumeExternalSecondFactor(ctx, inv, command, state)
	if err != nil {
		return nil, err
	}
	user, err := s.users.Get(ctx, consumed.UserID)
	if err != nil {
		return nil, s.failExternalSecondFactorSignIn(ctx, consumed.AuditEventID, externalSecondFactorStoreErrors.Translate(err))
	}
	if !user.IsActive() {
		return nil, s.failExternalSecondFactorSignIn(ctx, consumed.AuditEventID, invalidExternalAuthenticationError())
	}
	if consumed.Purpose == model.ExternalAuthenticationPurposeDesktopAuthorization {
		return s.completeDesktopAuthorizationSecondFactor(ctx, user, consumed)
	}
	session, tokens, err := s.createSession(ctx, sessionIssuance{
		User: user, ClientType: model.SessionClientWeb, DeviceID: consumed.DeviceID, DeviceName: consumed.DeviceName,
		AuthenticationGeneration: consumed.AuthenticationGeneration, ExternalLoginStateID: consumed.ExternalLoginStateID,
		AuthenticationMethod: consumed.AuthenticationMethod, AuthenticationProviderID: consumed.Provider,
		ExternalIdentityID: consumed.ExternalIdentityID, AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: consumed.PrimaryAuthenticatedAt.UnixMilli(), MFACompletedAt: consumed.ConsumedAt.Time.UnixMilli(),
		AuditEventID: consumed.AuditEventID,
	})
	if err != nil {
		return nil, s.failExternalSecondFactorSignIn(ctx, consumed.AuditEventID, err)
	}
	return &ExternalSecondFactorCompletion{ReturnTo: consumed.ReturnTo, Session: session, Tokens: tokens}, nil
}

func (s *Service) completeDesktopAuthorizationSecondFactor(
	ctx context.Context, user *model.User, state *model.ExternalSecondFactorState,
) (*ExternalSecondFactorCompletion, error) {
	proof, err := s.recordDesktopAuthorizationProof(ctx, "", store.DesktopAuthorizationAuthentication{
		TransactionID: state.BrowserAuthenticationTransactionID, UserID: state.UserID,
		AuthenticationMethod: state.AuthenticationMethod, AuthenticationProviderID: state.Provider,
		ExternalIdentityID: state.ExternalIdentityID, AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: state.PrimaryAuthenticatedAt, MFACompletedAt: state.ConsumedAt, AuditEventID: state.AuditEventID,
	})
	if err != nil {
		return nil, s.failExternalSecondFactorSignIn(ctx, state.AuditEventID, err)
	}
	if proof.Context == nil {
		return nil, s.failExternalSecondFactorSignIn(ctx, state.AuditEventID, authenticationUnavailable(nil))
	}
	current := proof.Context
	return &ExternalSecondFactorCompletion{DesktopAuthorization: &DesktopAuthorizationBrowserContext{
		State: current.State, DeviceName: current.DeviceName, RequestedAt: current.CreatedAt.UnixMilli(),
		ExpiresAt: current.ExpiresAt.UnixMilli(),
		Account:   &DesktopAuthorizationAccount{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email},
	}}, nil
}

func (s *Service) verifyAndConsumeExternalSecondFactor(
	ctx context.Context, inv invocation.Invocation, command CompleteExternalSecondFactorCommand,
	state *model.ExternalSecondFactorState,
) (*model.ExternalSecondFactorState, error) {
	if !s.mfa.deps.Settings.Enabled {
		return nil, apperror.New("authentication.mfa.unavailable")
	}
	if err := s.limitMFAFactorForUser(ctx, state.UserID, command.Source); err != nil {
		return nil, err
	}
	if s.securityAudit == nil {
		return nil, apperror.New("audit.unavailable")
	}
	audit, err := s.securityAudit.BeginBoundMFAChallenge(ctx, state.UserID, state.ClientType, state.AuthenticationMethod, inv.RequestMetadata())
	if err != nil {
		return nil, err
	}

	now := model.TimeUTC(s.now())
	proof, err := s.mfa.verifySecondFactor(ctx, s.mfaCredentials, state.UserID.String(), command.Code, now)
	if err != nil {
		return nil, s.failExternalSecondFactorChallenge(ctx, audit.ID, err)
	}
	consumed, err := s.externalLoginStates.ConsumeExternalSecondFactorState(ctx, &store.ExternalSecondFactorStateConsumption{
		ID: state.ID, BindingHash: state.BindingHash, Purpose: state.Purpose,
		CredentialID: proof.credentialID, TimeStep: proof.step, RecoveryCodeHash: proof.recoveryHash,
		VerifiedAt: now, AuditEventID: audit.ID,
	})
	if err != nil || consumed == nil {
		failure := externalSecondFactorStoreErrors.Translate(err)
		if appErr, ok := apperror.As(failure); ok && apperror.Code(appErr.Code()) == externalSecondFactorExpired {
			if _, expireErr := s.externalLoginStates.ExpireExternalSecondFactorState(ctx, state.BindingHash); expireErr != nil {
				failure = apperror.New("audit.unavailable").Wrap(expireErr)
			}
		}
		return nil, s.failExternalSecondFactorChallenge(ctx, audit.ID, failure)
	}
	return consumed, nil
}

func (s *Service) failExternalSecondFactorChallenge(ctx context.Context, auditID model.AuditEventID, failure error) error {
	code := "authentication.mfa.unavailable"
	if appErr, ok := apperror.As(failure); ok {
		code = appErr.Code()
	}
	if err := s.completeMFAAudit(ctx, auditID, model.AuditStatusFail, code, nil); err != nil {
		return err
	}
	return failure
}

func (s *Service) failExternalSecondFactorSignIn(ctx context.Context, auditID model.AuditEventID, failure error) error {
	code := "authentication.internal"
	if appErr, ok := apperror.As(failure); ok {
		code = appErr.Code()
	}
	if err := s.completeExternalAuthenticationAudit(ctx, auditID, code); err != nil {
		return err
	}
	return failure
}

var externalSecondFactorStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreNotFound, Entity: "mfa_credential", Code: "authentication.mfa.code_invalid"},
		{Kind: apperror.StoreNotFound, Entity: "mfa_recovery_code", Code: "authentication.mfa.code_invalid"},
		{Kind: apperror.StoreNotFound, Entity: "audit_event", Code: "audit.unavailable"},
		{Kind: apperror.StoreConflict, Entity: "mfa_credential", Constraint: "verification_expired", Code: "authentication.mfa.code_invalid"},
		{Kind: apperror.StoreConflict, Entity: "external_second_factor_state", Constraint: "expired", Code: externalSecondFactorExpired},
	},
	NotFound:    "authentication.external.invalid",
	Conflict:    "authentication.external.invalid",
	Unavailable: "authentication.external.unavailable",
}
