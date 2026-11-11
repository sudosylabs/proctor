// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// beginDesktopIssueAudit records the approval attempt with the approving
// account as the actor and the browser request's identity, never as an
// actor-less system event.
func (s *Service) beginDesktopIssueAudit(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
	institutionID model.InstitutionID,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil || !userID.IsValid() || !institutionID.IsValid() {
		return nil, apperror.New("audit.unavailable")
	}
	return s.securityAudit.BeginAccountActionAtScope(
		ctx,
		access.AccountAuditActor{
			UserID:     userID,
			SessionID:  inv.Principal().SessionID,
			ClientType: model.SessionClientWeb,
		},
		model.ActionAuthenticationDesktopAuthorization,
		model.Resource{Type: model.ResourceUser, ID: userID.String()},
		model.RoleScopeInstitution,
		institutionID.String(),
		inv.RequestMetadata(),
		map[string]any{"operation": "issue_code"},
	)
}

// beginDesktopExchangeAudit records the Desktop Session issuance attempt for
// the account that approved the code, with the Desktop request's network
// identity.
func (s *Service) beginDesktopExchangeAudit(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
	institutionID model.InstitutionID,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil || !userID.IsValid() || !institutionID.IsValid() {
		return nil, apperror.New("audit.unavailable")
	}
	return s.securityAudit.BeginAccountActionAtScope(
		ctx,
		access.AccountAuditActor{UserID: userID, ClientType: model.SessionClientDesktop},
		model.ActionAuthenticationDesktopSessionIssue,
		model.Resource{Type: model.ResourceUser, ID: userID.String()},
		model.RoleScopeInstitution,
		institutionID.String(),
		inv.RequestMetadata(),
		map[string]any{"operation": "exchange"},
	)
}

// recordRejectedDesktopIssue durably records an approval refused because the
// approving browser Session belongs to another account than the one the
// handoff authenticated: the approving Session's account is the actor and the
// handoff's account the resource.
func (s *Service) recordRejectedDesktopIssue(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
	institutionID model.InstitutionID,
	errorCode string,
) error {
	caller := inv.Principal()
	if s.securityAudit == nil || !caller.UserID.IsValid() || !userID.IsValid() || !institutionID.IsValid() {
		return apperror.New("audit.unavailable")
	}

	event, err := s.securityAudit.BeginAccountActionAtScope(
		ctx,
		access.AccountAuditActor{
			UserID:     caller.UserID,
			SessionID:  caller.SessionID,
			ClientType: model.SessionClientWeb,
		},
		model.ActionAuthenticationDesktopAuthorization,
		model.Resource{Type: model.ResourceUser, ID: userID.String()},
		model.RoleScopeInstitution,
		institutionID.String(),
		inv.RequestMetadata(),
		map[string]any{"operation": "issue_code"},
	)
	if err != nil {
		return err
	}

	return s.failDesktopAudit(ctx, event.ID, errorCode)
}

// beginDesktopWithdrawalAudit records the attempt to take back a Desktop
// Session whose exchange committed but whose credentials were never
// disclosed.
func (s *Service) beginDesktopWithdrawalAudit(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
	institutionID model.InstitutionID,
	sessionID model.SessionID,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil || !userID.IsValid() || !institutionID.IsValid() || !sessionID.IsValid() {
		return nil, apperror.New("audit.unavailable")
	}
	return s.securityAudit.BeginAccountActionAtScope(
		ctx,
		access.AccountAuditActor{UserID: userID, ClientType: model.SessionClientDesktop},
		model.ActionAuthenticationDesktopSessionIssue,
		model.Resource{Type: model.ResourceUser, ID: userID.String()},
		model.RoleScopeInstitution,
		institutionID.String(),
		inv.RequestMetadata(),
		map[string]any{"operation": "withdraw", "value": map[string]any{"session_id": sessionID}},
	)
}

func (s *Service) completeDesktopAudit(ctx context.Context, auditID model.AuditEventID, result map[string]any) error {
	if s.securityAudit == nil {
		return apperror.New("audit.unavailable")
	}
	_, appErr := s.securityAudit.CompleteCriticalAction(ctx, auditID, model.AuditStatusSuccess, "", result)
	return appErr
}

func (s *Service) failDesktopAudit(ctx context.Context, auditID model.AuditEventID, errorCode string) error {
	if s.securityAudit == nil {
		return apperror.New("audit.unavailable")
	}
	_, appErr := s.securityAudit.CompleteCriticalAction(ctx, auditID, model.AuditStatusFail, errorCode, nil)
	return appErr
}
