// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func testAssistantDraftPolicy(t *testing.T, ctx context.Context, ss store.Store, profile *model.AssistantProfile, institutionID model.InstitutionID, actor model.UserID) {
	t.Helper()
	unit := saveAcademicUnit(t, ctx, ss, institutionID.String(), "", "assistant-unit")
	at := model.NowUTC()
	created := createCatalogExam(t, ctx, ss, unit.ID, actor, at, "assistant-exam")
	policy := model.DefaultAssistantPolicy()
	policy.Enabled = true
	policy.ProfileID = profile.ID
	policy.Level = model.AssistantLevelTutor
	policy.Budgets = profile.Budgets
	update := func(revision int64, p model.AssistantPolicy, key string) (*store.ExamAuthoringCommandResult, error) {
		audit := saveExamSittingAudit(t, ctx, ss, actor, created.Value.Exam.ID, unit.ID)
		return ss.ExamAuthoring().UpdateDraftAssistant(ctx, &store.ExamDraftAssistantUpdate{
			ExamID: created.Value.Exam.ID, ActorUserID: actor, ExpectedRevision: revision, Assistant: p, UpdatedAt: at.Add(time.Second), AuditEventID: audit.ID, AuditAt: audit.CreatedAt,
		}, examCommand(actor, "exam.draft.assistant.configure", key, key))
	}
	updated, err := update(1, policy, "assistant-policy")
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Value.Draft.Revision)
	require.True(t, policy.Equal(updated.Value.Draft.Policy.Assistant))
	replayed, err := update(1, policy, "assistant-policy")
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	_, err = update(1, model.DefaultAssistantPolicy(), "stale-assistant")
	requireConflictConstraint(t, err, "exam_draft_revision")
	over := policy.Clone()
	over.Budgets.OutputTokens++
	_, err = update(2, over, "over-ceiling")
	requireConflictConstraint(t, err, "assistant_profile_unusable")
	publish := func(enabled bool, providers []string, key string) (*store.ExamRevisionPublicationResult, error) {
		input := examRevisionPublication(t, ctx, ss, created.Value.Exam.ID, actor, unit.ID, 2, at.Add(2*time.Second))
		input.AssistantEnabled = enabled
		input.AssistantProviders = providers
		return ss.ExamRevision().Publish(ctx, input, examCommand(actor, "exam.revision.publish", key, key))
	}
	_, err = publish(false, []string{profile.Provider}, "assistant-disabled")
	requireConflictConstraint(t, err, "assistant_disabled")
	_, err = publish(true, []string{"missing"}, "assistant-provider-missing")
	requireConflictConstraint(t, err, "assistant_profile_unusable")
	published, err := publish(true, []string{profile.Provider}, "assistant-publish")
	require.NoError(t, err)
	snapshot, err := ss.ExamRevision().GetSnapshot(ctx, created.Value.Exam.ID, published.Revision.ID)
	require.NoError(t, err)
	frozen, err := model.DecodeExamPolicySet(snapshot.Policy.Bytes)
	require.NoError(t, err)
	require.True(t, policy.Equal(frozen.Assistant))
}
