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

// TestAssistantAdmissionStore admits after profile retirement and probes the frozen recipe.
func TestAssistantAdmissionStore(t *testing.T, ss store.Store, verify func(*testing.T, model.ExamAttemptID, model.AssistantPolicy),
	exercise ...func(*testing.T, *store.AssistantMessageStart, func())) {
	t.Helper()
	testAssistantAdmissionStore(t, ss, model.AssistantLevelTutor, nil, model.AssistantBudgets{}, verify, exercise...)
}
func testAssistantAdmissionStore(t *testing.T, ss store.Store, level model.AssistantLevel, execution *model.ExecutionPolicy, budgets model.AssistantBudgets,
	verify func(*testing.T, model.ExamAttemptID, model.AssistantPolicy), exercise ...func(*testing.T, *store.AssistantMessageStart, func())) {

	t.Helper()
	ctx := context.Background()
	bootstrap, err := ss.Installation().Bootstrap(ctx, testInstallationBootstrap(814))
	require.NoError(t, err)
	actor := bootstrap.Administrator.ID
	spec := model.AssistantProfileSpecification{Name: "admission-tutor", DisplayName: "Campus tutor", Provider: "campus",
		Model: "teaching-model", MaxLevel: level,
		Budgets:      model.AssistantBudgets{Turns: 100, InputTokens: 100000, OutputTokens: 10000, CommandTimeoutSeconds: 60},
		DataHandling: "self_hosted", Pseudonymize: true}
	if budgets.Valid() {
		spec.Budgets = budgets
	}
	profile, err := model.NewAssistantProfile(model.NewAssistantProfileID(), spec, actor, model.NowUTC())
	require.NoError(t, err)
	auditID, auditAt := saveAssistantProfileAudit(t, ctx, ss, bootstrap.Institution.ID, actor)
	_, err = ss.AssistantProfile().Create(ctx, &store.AssistantProfileCreation{Profile: profile, AuditEventID: auditID, AuditAt: auditAt},
		assistantProfileCommand(actor, store.AssistantProfileCreateOperation, "admission-profile"))
	require.NoError(t, err)
	policy := model.DefaultAssistantPolicy()
	policy.Enabled = true
	policy.ProfileID = profile.ID
	policy.Level = level
	policy.Budgets = spec.Budgets
	if execution != nil && execution.Profile.IsZero() {
		withProfile := *execution
		withProfile.Profile = createFixtureEnvironmentProfile(t, ctx, ss, bootstrap.Institution.ID, actor)
		execution = &withProfile
	}
	fixture := newExamAttemptFixtureWithAssistantOptions(t, ctx, ss, nil, nil, model.ExamCapacityPolicy{}, false, execution, &policy)
	auditID, auditAt = saveAssistantProfileAudit(t, ctx, ss, bootstrap.Institution.ID, actor)
	_, err = ss.AssistantProfile().Retire(ctx, &store.AssistantProfileRetirement{ActorID: actor, ProfileID: profile.ID,
		AuditEventID: auditID, AuditAt: auditAt}, assistantProfileCommand(actor, store.AssistantProfileRetireOperation, "admission-retire"))
	require.NoError(t, err)
	connect := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(),
		WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(),
		ContinuityCredentialHash: model.HashToken(model.NewCredentialToken()), AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID,
		AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, connect)
	command := examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "assistant-admission", "assistant-admission")
	result, err := ss.ExamAttempt().Connect(ctx, connect, command)
	require.NoError(t, err)
	require.NotNil(t, result)
	verify(t, connect.AttemptID, policy)
	connect.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	_, err = ss.ExamAttempt().Connect(ctx, connect, command)
	require.NoError(t, err)
	verify(t, connect.AttemptID, policy)
	// The attempt keeps its admitted allowances after the draft switches off.
	draft, err := ss.ExamAuthoring().Get(ctx, fixture.examID, fixture.manager.ID)
	require.NoError(t, err)
	audit := saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID)
	_, err = ss.ExamAuthoring().UpdateDraftAssistant(ctx, &store.ExamDraftAssistantUpdate{ExamID: fixture.examID,
		ActorUserID: fixture.manager.ID, ExpectedRevision: draft.Draft.Revision, Assistant: model.DefaultAssistantPolicy(),
		UpdatedAt: model.NowUTC().Add(time.Second), AuditEventID: audit.ID, AuditAt: audit.CreatedAt}, examCommand(fixture.manager.ID,
		"exam.draft.assistant.configure", "disable-after-admission", "disable-after-admission"))
	require.NoError(t, err)
	verify(t, connect.AttemptID, policy)
	for _, run := range exercise {
		audit := saveExamAttemptAudit(t, ctx, ss, fixture)
		run(t, &store.AssistantMessageStart{Access: store.ExamAttemptWorkspaceEditorAccess{
			AttemptID: connect.AttemptID, ParticipationID: connect.ParticipationID, Generation: result.Participation.Generation,
			CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
			DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, ConnectionID: connect.ConnectionID, ContinuityCredentialHash: connect.ContinuityCredentialHash,
		}, ConversationID: model.NewAssistantConversationID(), TurnID: model.NewAssistantTurnID(), OwnerToken: model.NewCredentialToken(),
			Text: "A hint, please.", AuditEventID: audit.ID, AuditAt: audit.CreatedAt}, func() {
			current, readErr := ss.ExamAttemptWorkspace().List(ctx, store.CandidateWorkspaceListOptions{Access: store.CandidateAttemptAccess{AttemptID: connect.AttemptID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, ConnectionID: connect.ConnectionID, ContinuityCredentialHash: connect.ContinuityCredentialHash}, ExpectedCursor: -1, Limit: 1})
			require.NoError(t, readErr)

			access := store.ExamSubmissionSealAccess{AttemptID: connect.AttemptID, ParticipationID: connect.ParticipationID,
				Generation:   result.Participation.Generation,
				ConnectionID: connect.ConnectionID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
				ContinuityCredentialHash: connect.ContinuityCredentialHash,
				ExpectedWorkspaceCursor:  current.Cursor, ExpectedCurrentRevisionID: fixture.sitting.ExamRevisionID}
			seal := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
				Access: access, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
			attachSubmissionReceipt(t, ctx, ss, fixture.candidate, seal)
			_, sealErr := ss.ExamSubmission().Seal(ctx, seal, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation,
				"assistant-seal", "assistant-seal"))
			require.NoError(t, sealErr)
		})
	}

}
