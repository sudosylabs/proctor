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

// SystemAuditAdapter audits trusted work with no requesting user, such as a
// job or a periodic task, so that work never fabricates a user to record
// what it did.
type SystemAuditAdapter struct{ audit *Auditor }

// Begin records value as given; it is for periodic tasks, which run under no
// job.
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

// BeginJob overwrites job_id and job_attempt_id in value, so the trail always
// leads back to the job that did the work.
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

// BeginOnBehalf names actor as the actor of work done outside any request of
// theirs, so the event has no session or request identity. Its one caller is
// an execution environment writing a candidate's workspace: the change is the
// candidate's own work, and there is no plugin principal to attribute it to.
func (a SystemAuditAdapter) BeginOnBehalf(
	ctx context.Context,
	actor model.UserID,
	action model.Action,
	resource model.Resource,
	scopeType model.RoleScopeType,
	scopeID,
	operation string,
	value map[string]any,
) (model.AuditEventID, error) {
	event, err := a.audit.BeginOnBehalfAtScope(
		ctx,
		actor,
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

// Fail completes an attempt as failed; code is the public error code.
func (a SystemAuditAdapter) Fail(ctx context.Context, id model.AuditEventID, code string) error {
	_, err := a.audit.CompleteCriticalAction(ctx, id, model.AuditStatusFail, code, nil)
	return err
}
