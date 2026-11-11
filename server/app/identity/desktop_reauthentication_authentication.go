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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuthenticateDesktopReauthenticationPasswordCommand proves the password of
// the Desktop Session's User. It has no selectable login identifier.
type AuthenticateDesktopReauthenticationPasswordCommand struct {
	Binding  string
	Password string
	Source   string
}

// AuthenticateDesktopReauthenticationPassword checks the password of the
// Desktop Session's own User. It creates no Web Session and asks for no second
// factor: the Desktop Session's MFA history is unchanged and only primary
// proof is refreshed.
func (s *Service) AuthenticateDesktopReauthenticationPassword(
	ctx context.Context,
	invocation invocation.Invocation,
	command AuthenticateDesktopReauthenticationPasswordCommand,
) (*DesktopReauthenticationBrowserContext, error) {
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
		purpose: authenticationAttemptPurposeReauthentication,
		window:  s.loginRateLimit.Window,
		limits: []AttemptLimit{
			{
				dimension: authenticationAttemptDimensionIdentitySource,
				maximum:   s.loginRateLimit.MaximumAttempts,
				identity:  current.UserID.String(),
				source:    command.Source,
			},
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
		BindingHash:          model.HashToken(command.Binding),
		UserID:               current.UserID,
		AuthenticationMethod: "password",
		PasswordProof:        store.PasswordCredentialProof{ID: credential.ID, Revision: credential.Revision},
		AuthenticatedAt:      model.TimeUTC(s.now()),
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
func (s *Service) recordDesktopReauthenticationProof(
	ctx context.Context,
	invocation invocation.Invocation,
	input *store.DesktopReauthenticationAuthentication,
) (*store.DesktopReauthenticationAuthenticationResult, error) {
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
		actor:         input.UserID,
		target:        result.UserID,
		institutionID: institution.ID,
		operation:     desktopReauthenticationOperation(input.AuthenticationMethod),
		errorCode:     failure.Code(),
	}); auditErr != nil {
		return nil, apperror.New("audit.unavailable").Wrap(auditErr)
	}

	return nil, failure
}

// resolveExternalReauthentication returns the bound reauthentication a
// provider login may continue, only for the Desktop Session's own provider.
func (s *Service) resolveExternalReauthentication(
	ctx context.Context,
	binding, state, providerID string,
) (desktopExternalAuthenticationTarget, error) {
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
func (s *Service) authenticateExternalReauthentication(
	ctx context.Context,
	invocation invocation.Invocation,
	transactionID model.BrowserAuthenticationTransactionID,
	identity *model.ExternalIdentity,
	method string,
	assertion *model.ExternalAuthenticationAssertion,
) error {
	if !transactionID.IsValid() ||
		identity == nil ||
		!identity.UserID.IsValid() ||
		!identity.ID.IsValid() ||
		assertion == nil ||
		assertion.ProviderID != identity.Provider ||
		method == "" ||
		method == "password" {
		return apperror.New("authentication.desktop_reauthentication.invalid")
	}

	_, err := s.recordDesktopReauthenticationProof(ctx, invocation, &store.DesktopReauthenticationAuthentication{
		TransactionID:        transactionID,
		UserID:               identity.UserID,
		AuthenticationMethod: method,
		ProviderID:           identity.Provider,
		ExternalIdentityID:   identity.ID,
		AuthenticatedAt:      model.TimeFromMillis(assertion.AuthenticatedAt),
	})
	return err
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
