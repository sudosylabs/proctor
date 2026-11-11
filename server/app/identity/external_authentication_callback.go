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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CompleteExternalAuthenticationCommand finishes a browser external login.
type CompleteExternalAuthenticationCommand struct {
	ProviderID string
	Callback   model.ExternalAuthenticationCallback
	Binding    string
	Source     string
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
