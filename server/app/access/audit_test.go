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

type savingAuditStore struct {
	store.AuditStore
	saved *model.AuditEvent
}

func (f *savingAuditStore) Save(_ context.Context, event *model.AuditEvent) (*model.AuditEvent, error) {
	f.saved = event
	saved := *event
	saved.ID = model.NewAuditEventID()
	return &saved, nil
}

func TestBeginOnBehalfNamesTheUserWithoutAPrincipal(t *testing.T) {
	t.Parallel()
	audits := &savingAuditStore{}
	adapter := SystemAuditAdapter{audit: &Auditor{deps: AuditorDependencies{Audits: audits, NodeID: "node-1"}, now: time.Now}}
	candidateID := model.NewUserID()
	resource := model.Resource{Type: model.ResourceExamSitting, ID: model.NewExamSittingID().String()}
	classID := model.NewClassID().String()

	id, err := adapter.BeginOnBehalf(context.Background(), candidateID, model.ActionExamSittingParticipate, resource,
		model.RoleScopeClass, classID, "exam.attempt.workspace.batch", map[string]any{"origin": "environment"})
	require.NoError(t, err)
	require.True(t, id.IsValid())
	require.Equal(t, candidateID, audits.saved.ActorID, "the candidate is the actor")
	require.True(t, audits.saved.SessionID.IsZero(), "no Session is fabricated")
	require.Empty(t, audits.saved.ClientType)
	require.Empty(t, audits.saved.RequestID)
	require.Equal(t, model.AuditStatusAttempt, audits.saved.Status)
	require.Equal(t, string(model.ActionExamSittingParticipate), audits.saved.Action)
	require.Equal(t, resource, audits.saved.Resource)
	require.Equal(t, model.RoleScopeClass, audits.saved.ScopeType)
	require.Equal(t, classID, audits.saved.ScopeID)
	require.Equal(t, "node-1", audits.saved.NodeID)
	require.JSONEq(t, `{"operation":"exam.attempt.workspace.batch","value":{"origin":"environment"}}`, string(audits.saved.Parameters))

	_, err = adapter.BeginOnBehalf(context.Background(), "", model.ActionExamSittingParticipate, resource,
		model.RoleScopeClass, classID, "exam.attempt.workspace.batch", nil)
	require.Error(t, err, "an on-behalf attempt needs a User")
}
