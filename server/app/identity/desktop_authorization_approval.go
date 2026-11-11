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

type ApproveDesktopAuthorizationCommand struct{ Binding, State string }

// DesktopAuthorizationApproval is the outcome of approving a Desktop
// authorization or reauthentication. RedirectURL is the Desktop's callback
// URL with the one-use code and the state added as query parameters.
// ExpiresAt is the code's deadline in Unix milliseconds.
type DesktopAuthorizationApproval struct {
	RedirectURL string
	ExpiresAt   int64
}

// ApproveDesktopAuthorization issues the one-use code for the account bound to
// the browser transaction. The binding cookie and pinned state are the
// approval proof; a caller that also presents a Principal must be that same
// account, so one signed-in User can never approve a transaction
// authenticated as another.
func (s *Service) ApproveDesktopAuthorization(
	ctx context.Context,
	invocation invocation.Invocation,
	command ApproveDesktopAuthorizationCommand,
) (*DesktopAuthorizationApproval, error) {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return nil, apperror.New("authentication.desktop_authorization.invalid")
	}

	current, err := s.browserAuthentications.GetDesktopAuthorizationContext(ctx, model.HashToken(command.Binding))
	if err != nil || current == nil || current.State != model.BrowserAuthenticationStateAuthenticated || !current.UserID.IsValid() {
		return nil, desktopAuthorizationStoreErrors.Translate(err)
	}

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}

	if caller := invocation.Principal(); caller.UserID != "" && caller.UserID != current.UserID {
		failure := apperror.New("authentication.desktop_authorization.rejected")
		if auditErr := s.recordRejectedDesktopIssue(ctx, invocation, current.UserID, institution.ID, failure.Code()); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}

		return nil, failure
	}

	audit, err := s.beginDesktopIssueAudit(ctx, invocation, current.UserID, institution.ID)
	if err != nil || audit == nil || !audit.ID.IsValid() {
		return nil, apperror.New("audit.unavailable").Wrap(err)
	}

	code := s.newCredential()
	if !model.IsValidCredentialToken(code) {
		return nil, apperror.New("authentication.desktop_authorization.unavailable")
	}

	at := model.TimeUTC(s.now())
	issued, err := s.browserAuthentications.IssueCode(ctx, &store.DesktopAuthorizationCodeIssue{
		BindingHash:    model.HashToken(command.Binding),
		StateHash:      model.HashToken(command.State),
		CodeHash:       model.HashToken(code),
		ExpectedUserID: current.UserID,
		CodeLifetime:   s.desktopPolicy.CodeLifetime,
		Capabilities:   AccessDeploymentCapabilities(s.capabilities.Snapshot()),
		AuditEventID:   audit.ID,
		AuditAt:        at,
	})
	if err != nil {
		failure := desktopAuthorizationStoreErrors.Translate(err)
		if auditErr := s.failDesktopAudit(
			ctx,
			audit.ID,
			apperror.CodeOf(failure, "authentication.desktop_authorization.unavailable"),
		); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}

		return nil, failure
	}

	if issued == nil || model.ValidateDesktopAuthorizationCallback(issued.CallbackURL) != nil || issued.CodeExpiresAt.IsZero() {
		failure := apperror.New("authentication.desktop_authorization.unavailable")
		if auditErr := s.failDesktopAudit(ctx, audit.ID, failure.Code()); auditErr != nil {
			return nil, apperror.New("audit.unavailable").Wrap(auditErr)
		}

		return nil, failure
	}

	redirect, err := desktopAuthorizationRedirectURL(issued.CallbackURL, code, command.State)
	if err != nil {
		return nil, apperror.New("authentication.desktop_authorization.unavailable").Wrap(err)
	}

	return &DesktopAuthorizationApproval{RedirectURL: redirect, ExpiresAt: issued.CodeExpiresAt.UnixMilli()}, nil
}

// CancelDesktopAuthorization ends the bound Desktop authorization.
func (s *Service) CancelDesktopAuthorization(
	ctx context.Context,
	_ invocation.Invocation,
	command ApproveDesktopAuthorizationCommand,
) error {
	if !model.IsValidCredentialToken(command.Binding) || !model.IsValidCredentialToken(command.State) {
		return apperror.New("authentication.desktop_authorization.invalid")
	}

	err := s.browserAuthentications.Cancel(ctx, &store.DesktopAuthorizationCancellation{
		BindingHash: model.HashToken(command.Binding),
		StateHash:   model.HashToken(command.State),
	})
	if err != nil {
		return desktopAuthorizationStoreErrors.Translate(err)
	}

	return nil
}

func desktopAuthorizationRedirectURL(callback, code, state string) (string, error) {
	if model.ValidateDesktopAuthorizationCallback(callback) != nil {
		return "", model.ErrInvalidDesktopAuthorizationCallback
	}
	parsed, err := url.Parse(callback)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("code", code)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
