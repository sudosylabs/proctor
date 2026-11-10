// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type completionContextAuditStore struct {
	store.AuditStore
	completionErr error
}

func (f *completionContextAuditStore) Complete(ctx context.Context, input *store.AuditCompletion) (*model.AuditEvent, error) {
	id := input.ID
	status := input.Status
	errorCode := input.ErrorCode
	f.completionErr = ctx.Err()
	return &model.AuditEvent{ID: id, Status: status, ErrorCode: errorCode}, nil
}

func TestCompleteCriticalActionSurvivesACanceledRequest(t *testing.T) {
	t.Parallel()

	audits := &completionContextAuditStore{}
	service := &Auditor{deps: AuditorDependencies{Audits: audits}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	auditID := model.NewAuditEventID()
	for _, status := range []model.AuditStatus{model.AuditStatusFail, model.AuditStatusSuccess} {
		_, err := service.CompleteCriticalAction(ctx, auditID, status, "", nil)
		require.NoError(t, err, "status %s", status)
		require.NoError(t, audits.completionErr, "status %s: the completion write saw the request's cancellation", status)
	}
}
