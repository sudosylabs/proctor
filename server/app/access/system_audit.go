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
)

// SystemAuditAdapter begins and fails the audit attempt of trusted actorless
// work: a durable Job or a periodic runtime task. The attempt names no actor,
// so that work never fabricates a User to record what it did.
type SystemAuditAdapter struct{ audit *Auditor }

// Begin persists the attempt of an actorless mutation and records the value
// exactly as given. Periodic runtime tasks, which run under no Job, begin
// their attempts here.
func (a SystemAuditAdapter) Begin(
	ctx context.Context,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID,
	operation string,
	value map[string]any,
) (model.AuditEventID, error) {
	auditValue := make(map[string]any, len(value))
	for key, item := range value {
		auditValue[key] = item
	}

	return a.begin(ctx, action, resource, scopeType, scopeID, operation, auditValue)
}

// BeginJob persists the attempt of a mutation a Job execution performs and
// names that execution in the recorded value, whatever the caller put under
// those keys, so the audit trail leads back to the Job that did the work.
func (a SystemAuditAdapter) BeginJob(
	ctx context.Context,
	system invocation.System,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID,
	operation string,
	value map[string]any,
) (model.AuditEventID, error) {
	auditValue := make(map[string]any, len(value)+2)
	for key, item := range value {
		auditValue[key] = item
	}

	auditValue["job_id"] = system.JobID.String()
	auditValue["job_attempt_id"] = system.AttemptID.String()
	return a.begin(ctx, action, resource, scopeType, scopeID, operation, auditValue)
}

func (a SystemAuditAdapter) begin(
	ctx context.Context,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID,
	operation string,
	value map[string]any,
) (model.AuditEventID, error) {
	event, err := a.audit.BeginSystemCriticalActionAtScope(
		ctx,
		action,
		resource,
		scopeType,
		scopeID,
		map[string]any{"operation": operation, "value": value},
	)
	if err != nil {
		return "", err
	}

	return event.ID, nil
}

// Fail completes an attempt as failed with the error code.
func (a SystemAuditAdapter) Fail(ctx context.Context, id model.AuditEventID, code string) error {
	_, err := a.audit.CompleteCriticalAction(ctx, id, model.AuditStatusFail, code, nil)
	return err
}
