// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type mutationAuditor interface {
	Begin(
		context.Context, invocation.Invocation, model.Action, model.Resource, string,
		map[string]any, map[string]any,
	) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// mutationAuditAdapter is the one Invocation-shaped view of the audit service.
// Every parent use case and Exam child receives it directly for
// principal-attributed mutation audit; it never wraps another adapter.
type mutationAuditAdapter struct{ audit *auditService }

func (a mutationAuditAdapter) Begin(ctx context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource, operation string, value, prior map[string]any) (model.AuditEventID, error) {
	event, appErr := a.audit.BeginCriticalAction(
		ctx, invocation.Principal(), action, resource,
		invocation.RequestMetadata(),
		map[string]any{"operation": operation, "value": value}, prior,
	)
	if appErr != nil {
		return "", appErr
	}
	return event.ID, nil
}

func (a mutationAuditAdapter) BeginAtScope(ctx context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID, operation string, value, prior map[string]any) (model.AuditEventID, error) {
	event, appErr := a.audit.BeginCriticalActionAtScope(
		ctx, invocation.Principal(), action, resource, scopeType, scopeID,
		invocation.RequestMetadata(),
		map[string]any{"operation": operation, "value": value}, prior,
	)
	if appErr != nil {
		return "", appErr
	}
	return event.ID, nil
}

// BeginForSubject begins the attempt of a mutation whose resource is not the
// affected User, naming that User as the event's subject.
func (a mutationAuditAdapter) BeginForSubject(ctx context.Context, attempt mutationAttempt) (model.AuditEventID, error) {
	principal := attempt.Invocation.Principal()
	if principal.ValidateMFARecovery() != nil {
		return "", invalidTokenAppError()
	}
	if a.audit == nil || a.audit.deps.Audits == nil {
		return "", auditUnavailable(store.NewErrNotFound("audit_store", ""))
	}
	scopeType, scopeID := attempt.ScopeType, attempt.ScopeID
	if scopeType == "" && scopeID == "" {
		var err error
		if scopeType, scopeID, err = a.audit.resourceScope(ctx, attempt.Resource); err != nil {
			return "", err
		}
	}
	event, err := a.audit.beginCriticalActionAtScope(
		ctx, principal, attempt.Subject, attempt.Action, attempt.Resource, scopeType, scopeID,
		attempt.Invocation.RequestMetadata(),
		map[string]any{"operation": attempt.Operation, "value": attempt.Value}, attempt.Prior,
	)
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func (a mutationAuditAdapter) Fail(ctx context.Context, auditID model.AuditEventID, errorCode string) error {
	_, appErr := a.audit.CompleteCriticalAction(
		ctx, auditID, model.AuditStatusFail, errorCode, nil,
	)
	return appErr
}

func (a mutationAuditAdapter) PrepareAtScope(_ context.Context, invocation invocation.Invocation, action model.Action,
	resource model.Resource, scopeType model.RoleScopeType, scopeID, operation string, value map[string]any,
) (*model.AuditEvent, error) {
	return a.audit.prepareCriticalActionAtScope(invocation.Principal(), "", action, resource, scopeType, scopeID,
		invocation.RequestMetadata(), map[string]any{"operation": operation, "value": value}, nil)
}

func (a mutationAuditAdapter) RecordAuthorizationDecision(ctx context.Context, invocation invocation.Invocation,
	action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID string, allowed bool,
) error {
	return a.audit.RecordAuthorizationDecision(ctx, invocation.Principal(), action, resource, scopeType, scopeID,
		invocation.RequestMetadata(), allowed)
}

func (a mutationAuditAdapter) Complete(ctx context.Context, auditID model.AuditEventID, value map[string]any) error {
	_, err := a.audit.CompleteCriticalAction(ctx, auditID, model.AuditStatusSuccess, "", value)
	return err
}
