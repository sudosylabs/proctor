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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

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

// ApproveDesktopReauthentication issues the one-use code and the loopback
// redirect that hands it to the Desktop.
func (s *Service) ApproveDesktopReauthentication(
	ctx context.Context,
	_ invocation.Invocation,
	command ApproveDesktopAuthorizationCommand,
) (*DesktopAuthorizationApproval, error) {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}

	code := s.newCredential()
	if !model.IsValidCredentialToken(code) {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication code is invalid"))
	}

	issued, err := s.browserAuthentications.IssueDesktopReauthenticationCode(ctx, &store.DesktopReauthenticationCodeIssue{
		BindingHash:  model.HashToken(command.Binding),
		StateHash:    model.HashToken(command.State),
		CodeHash:     model.HashToken(code),
		CodeLifetime: s.desktopPolicy.CodeLifetime,
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
func (s *Service) CancelDesktopReauthentication(
	ctx context.Context,
	_ invocation.Invocation,
	command ApproveDesktopAuthorizationCommand,
) error {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return apperror.New("authentication.desktop_reauthentication.invalid")
	}

	if err := s.browserAuthentications.CancelDesktopReauthentication(ctx, &store.DesktopAuthorizationCancellation{
		BindingHash: model.HashToken(command.Binding),
		StateHash:   model.HashToken(command.State),
	}); err != nil {
		return desktopReauthenticationStoreErrors.Translate(err)
	}

	return nil
}

// CompleteDesktopReauthentication redeems the code from the same Desktop
// Session and key that started the handoff and refreshes that Session's
// primary proof. No Session is created and the Registration is unchanged.
func (s *Service) CompleteDesktopReauthentication(
	ctx context.Context,
	invocation invocation.Invocation,
	command CompleteDesktopReauthenticationCommand,
) (*DesktopReauthenticationResult, error) {
	principal := invocation.Principal()
	credentialID, err := requireDesktopDPoPSession(principal)
	if err != nil {
		return nil, err
	}

	if !model.IsValidCredentialToken(command.Code) ||
		!model.IsValidCredentialToken(command.State) ||
		!model.IsValidPKCEVerifier(command.CodeVerifier) {
		return nil, apperror.New("authentication.desktop_reauthentication.invalid")
	}

	codeHash := model.HashToken(command.Code)
	if err = s.checkDesktopAuthorizationAttempt(
		ctx,
		desktopAuthorizationAttemptReauthenticationComplete,
		codeHash,
		command.Source,
	); err != nil {
		return nil, err
	}

	result, err := access.RunAudited(ctx, s.auditor, access.Mutation{
		Invocation: invocation,
		Action:     model.ActionAuthenticationReauthenticate,
		Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
		Operation:  desktopReauthenticationOperation(principal.AuthenticationMethod),
		Value:      map[string]any{"session_id": principal.SessionID.String()},
	}, s.now, func(ctx context.Context, reference access.MutationReference) (*store.SessionReauthenticationResult, error) {
		return s.browserAuthentications.CompleteDesktopReauthentication(ctx, &store.DesktopReauthenticationCompletion{
			CodeHash:              codeHash,
			StateHash:             model.HashToken(command.State),
			CodeChallenge:         model.PKCES256Challenge(command.CodeVerifier),
			UserID:                principal.UserID,
			SessionID:             principal.SessionID,
			CredentialID:          credentialID,
			DesktopRegistrationID: principal.DesktopRegistrationID,
			KeyThumbprint:         principal.DPoPKeyThumbprint,
			AuditEventID:          reference.ID,
		})
	}, desktopReauthenticationStoreErrors.Translate)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Session == nil || !result.Session.ReauthenticatedAt.Valid {
		return nil, authenticationUnavailable(errors.New("desktop reauthentication returned no Session"))
	}

	s.publisher.AuthenticationCacheInvalidated(ctx, principal.UserID, result.AccessTokenHashes)
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

// desktopReauthenticationRejection names who proved what against which
// Desktop Session's account.
type desktopReauthenticationRejection struct {
	actor         model.UserID
	target        model.UserID
	institutionID model.InstitutionID
	operation     string
	errorCode     string
}

// recordRejectedDesktopReauthentication records, as a failed
// reauthentication, a browser proof of another account: the proving account
// is the actor and the Desktop Session's account the resource.
func (s *Service) recordRejectedDesktopReauthentication(
	ctx context.Context,
	invocation invocation.Invocation,
	rejection desktopReauthenticationRejection,
) error {
	if s.securityAudit == nil || !rejection.actor.IsValid() || !rejection.target.IsValid() || !rejection.institutionID.IsValid() {
		return apperror.New("audit.unavailable")
	}

	event, err := s.securityAudit.BeginAccountActionAtScope(
		ctx,
		access.AccountAuditActor{UserID: rejection.actor, ClientType: model.SessionClientWeb},
		model.ActionAuthenticationReauthenticate,
		model.Resource{Type: model.ResourceUser, ID: rejection.target.String()},
		model.RoleScopeInstitution,
		rejection.institutionID.String(),
		invocation.RequestMetadata(),
		map[string]any{"operation": rejection.operation},
	)
	if err != nil {
		return err
	}

	_, appErr := s.securityAudit.CompleteCriticalAction(ctx, event.ID, model.AuditStatusFail, rejection.errorCode, nil)
	return appErr
}
