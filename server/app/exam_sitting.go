// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type examSittingSystemAuditAdapter struct{ audit *access.Auditor }

func (a examSittingSystemAuditAdapter) Begin(
	ctx context.Context,
	call invocation.System,
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

	auditValue["job_id"] = call.JobID.String()
	auditValue["job_attempt_id"] = call.AttemptID.String()
	event, err := a.audit.BeginSystemCriticalActionAtScope(
		ctx,
		action,
		resource,
		scopeType,
		scopeID,
		map[string]any{"operation": operation, "value": auditValue},
	)
	if err != nil {
		return "", err
	}

	return event.ID, nil
}

func (a examSittingSystemAuditAdapter) Fail(ctx context.Context, id model.AuditEventID, code string) error {
	_, err := a.audit.CompleteCriticalAction(ctx, id, model.AuditStatusFail, code, nil)
	return err
}

var _ sitting.Authorizer = (*access.Authorizer)(nil)
var _ sitting.Auditor = access.MutationAuditAdapter{}
var _ sitting.SystemAuditor = examSittingSystemAuditAdapter{}
