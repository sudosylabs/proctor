// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// MutationAuditor begins and fails the audit attempt of one named Store
// mutation. Begin receives the whole Mutation, so every auditor is handed the
// scope and the subject User the attempt must record.
type MutationAuditor interface {
	Begin(context.Context, Mutation) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// MutationAuditAdapter is the one Invocation-shaped view of the audit service.
// Every parent use case and Exam child receives it directly for
// principal-attributed mutation audit; it never wraps another adapter.
type MutationAuditAdapter struct{ audit *Auditor }

// Begin persists the attempt of a mutation. The Mutation's fields choose how
// it is recorded: a Subject names the affected User, a ScopeType or ScopeID
// records the explicit authorization scope, and neither records the
// resource's own scope.
func (a MutationAuditAdapter) Begin(ctx context.Context, attempt Mutation) (model.AuditEventID, error) {
	var event *model.AuditEvent
	var err error
	switch {
	case !attempt.Subject.IsZero():
		event, err = a.beginForSubject(ctx, attempt)
	case attempt.ScopeType != "" || attempt.ScopeID != "":
		event, err = a.audit.BeginCriticalActionAtScope(
			ctx,
			attempt.Invocation.Principal(),
			attempt.Action,
			attempt.Resource,
			attempt.ScopeType,
			attempt.ScopeID,
			attempt.Invocation.RequestMetadata(),
			map[string]any{"operation": attempt.Operation, "value": attempt.Value},
			attempt.Prior,
		)
	default:
		event, err = a.audit.BeginCriticalAction(
			ctx,
			attempt.Invocation.Principal(),
			attempt.Action,
			attempt.Resource,
			attempt.Invocation.RequestMetadata(),
			map[string]any{"operation": attempt.Operation, "value": attempt.Value},
			attempt.Prior,
		)
	}

	if err != nil {
		return "", err
	}

	return event.ID, nil
}

// beginForSubject begins the attempt of a mutation whose resource is not the
// affected User, naming that User as the event's subject.
func (a MutationAuditAdapter) beginForSubject(ctx context.Context, attempt Mutation) (*model.AuditEvent, error) {
	principal := attempt.Invocation.Principal()
	if principal.ValidateMFARecovery() != nil {
		return nil, InvalidTokenAppError()
	}
	if a.audit == nil || a.audit.deps.Audits == nil {
		return nil, AuditUnavailable(store.NewErrNotFound("audit_store", ""))
	}

	scopeType, scopeID := attempt.ScopeType, attempt.ScopeID
	if scopeType == "" && scopeID == "" {
		var err error
		if scopeType, scopeID, err = a.audit.resourceScope(ctx, attempt.Resource); err != nil {
			return nil, err
		}
	}

	return a.audit.beginCriticalActionAtScope(
		ctx,
		principal,
		attempt.Subject,
		attempt.Action,
		attempt.Resource,
		scopeType,
		scopeID,
		attempt.Invocation.RequestMetadata(),
		map[string]any{"operation": attempt.Operation, "value": attempt.Value},
		attempt.Prior,
	)
}

// Fail completes an attempt as failed with the error code.
func (a MutationAuditAdapter) Fail(ctx context.Context, auditID model.AuditEventID, errorCode string) error {
	_, appErr := a.audit.CompleteCriticalAction(
		ctx,
		auditID,
		model.AuditStatusFail,
		errorCode,
		nil,
	)
	return appErr
}

// PrepareAtScope builds the audit draft of a mutation that a Store aggregate inserts itself.
func (a MutationAuditAdapter) PrepareAtScope(
	_ context.Context,
	inv invocation.Invocation,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID,
	operation string,
	value map[string]any,
) (*model.AuditEvent, error) {
	return a.audit.prepareCriticalActionAtScope(
		inv.Principal(),
		"",
		action,
		resource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		map[string]any{"operation": operation, "value": value},
		nil,
	)
}

// RecordAuthorizationDecision persists an authorization decision for the invocation.
func (a MutationAuditAdapter) RecordAuthorizationDecision(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID string,
	allowed bool,
) error {
	return a.audit.RecordAuthorizationDecision(
		ctx,
		inv.Principal(),
		action,
		resource,
		scopeType,
		scopeID,
		inv.RequestMetadata(),
		allowed,
	)
}

// Complete completes an attempt as successful with its result.
func (a MutationAuditAdapter) Complete(ctx context.Context, auditID model.AuditEventID, value map[string]any) error {
	_, err := a.audit.CompleteCriticalAction(ctx, auditID, model.AuditStatusSuccess, "", value)
	return err
}
