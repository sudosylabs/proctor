// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func automaticSealTargetFixture() ExamSubmissionAutomaticSealTarget {
	return ExamSubmissionAutomaticSealTarget{
		ExamID:            model.NewExamID(),
		SittingID:         model.NewExamSittingID(),
		ClassID:           model.NewClassID(),
		AcademicUnitID:    model.NewAcademicUnitID(),
		CandidateUserID:   model.NewUserID(),
		AttemptID:         model.NewExamAttemptID(),
		WorkspaceID:       model.NewExamAttemptWorkspaceID(),
		CurrentRevisionID: model.NewExamRevisionID(),
		ParticipationID:   model.NewAttemptParticipationID(),
		Generation:        2,
		ConnectionID:      model.NewAttemptConnectionID(),
	}
}

// sealResultFixture is the consistent outcome of sealing target's Attempt with
// the proposed Submission identities.
func sealResultFixture(target ExamSubmissionAutomaticSealTarget, id model.SubmissionID, code model.SubmissionReceiptCode) ExamSubmissionSealResult {
	return ExamSubmissionSealResult{
		Receipt: ExamSubmissionReceipt{
			BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
			SubmissionID:    id,
			ReceiptCode:     code,
			AttemptID:       target.AttemptID,
			ExamRevisionID:  target.CurrentRevisionID,
			State:           model.ExamAttemptSubmitted,
			WorkspaceCursor: 4,
			ManifestDigest:  strings.Repeat("a", 64),
			SubmittedAt:     consistencyAt,
		},
		ExamID:          target.ExamID,
		SittingID:       target.SittingID,
		ClassID:         target.ClassID,
		CandidateUserID: target.CandidateUserID,
		ParticipationID: target.ParticipationID,
		Generation:      target.Generation,
		ConnectionID:    target.ConnectionID,
	}
}

// sealScopeInconsistencies are the clauses the three Submission outcomes share
// in the same form: receipt completeness and the scope the outcome names.
func sealScopeInconsistencies() []inconsistency[ExamSubmissionSealResult] {
	type bad = inconsistency[ExamSubmissionSealResult]
	return []bad{
		{"receipt not submitted", func(r *ExamSubmissionSealResult) { r.Receipt.State = model.ExamAttemptActive }},
		{"receipt of another Attempt", func(r *ExamSubmissionSealResult) { r.Receipt.AttemptID = model.NewExamAttemptID() }},
		{"no Submission ID", func(r *ExamSubmissionSealResult) { r.Receipt.SubmissionID = "" }},
		{"no receipt code", func(r *ExamSubmissionSealResult) { r.Receipt.ReceiptCode = "" }},
		{"malformed manifest digest", func(r *ExamSubmissionSealResult) { r.Receipt.ManifestDigest = strings.Repeat("A", 64) }},
		{"no submission instant", func(r *ExamSubmissionSealResult) { r.Receipt.SubmittedAt = time.Time{} }},
		{"another Exam", func(r *ExamSubmissionSealResult) { r.ExamID = model.NewExamID() }},
		{"another Sitting", func(r *ExamSubmissionSealResult) { r.SittingID = model.NewExamSittingID() }},
		{"another Class", func(r *ExamSubmissionSealResult) { r.ClassID = model.NewClassID() }},
		{"another candidate", func(r *ExamSubmissionSealResult) { r.CandidateUserID = model.NewUserID() }},
		{"another Participation", func(r *ExamSubmissionSealResult) { r.ParticipationID = model.NewAttemptParticipationID() }},
		{"another generation", func(r *ExamSubmissionSealResult) { r.Generation++ }},
		{"another Connection", func(r *ExamSubmissionSealResult) { r.ConnectionID = model.NewAttemptConnectionID() }},
		{"fresh outcome with another Submission ID", func(r *ExamSubmissionSealResult) { r.Receipt.SubmissionID = model.NewSubmissionID() }},
		{"fresh outcome with another receipt code", func(r *ExamSubmissionSealResult) {
			r.Receipt.ReceiptCode = model.NewSubmissionReceiptCode()
		}},
	}
}

// onEmbeddedSeal lifts the shared seal inconsistencies onto an outcome type
// that embeds ExamSubmissionSealResult.
func onEmbeddedSeal[T any](embedded func(*T) *ExamSubmissionSealResult, extra ...inconsistency[T]) []inconsistency[T] {
	cases := make([]inconsistency[T], 0, len(extra)+16)
	for _, shared := range sealScopeInconsistencies() {
		cases = append(cases, inconsistency[T]{shared.name, func(value *T) { shared.apply(embedded(value)) }})
	}
	return append(cases, extra...)
}

func TestExamSubmissionAutomaticSealResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamSubmissionAutomaticSealResult
	target := automaticSealTargetFixture()
	proposed, proposedCode := model.NewSubmissionID(), model.NewSubmissionReceiptCode()
	consistent := func(*testing.T) *result {
		return &result{ExamSubmissionSealResult: sealResultFixture(target, proposed, proposedCode), ConnectionClosed: true}
	}
	check := func(r *result) bool { return r.ConsistentWith(target, proposed, proposedCode) }

	require.False(t, (*result)(nil).ConsistentWith(target, proposed, proposedCode))
	requireEachInconsistencyRejected(t, consistent, check, onEmbeddedSeal(
		func(r *result) *ExamSubmissionSealResult { return &r.ExamSubmissionSealResult },
		inconsistency[result]{"receipt of another Exam Revision", func(r *result) { r.Receipt.ExamRevisionID = model.NewExamRevisionID() }},
		inconsistency[result]{"negative workspace cursor", func(r *result) { r.Receipt.WorkspaceCursor = -1 }},
		inconsistency[result]{"replay that closed a Connection", func(r *result) { r.Replayed = true }},
	))

	replayed := consistent(t)
	replayed.Replayed, replayed.ConnectionClosed = true, false
	replayed.Receipt.SubmissionID, replayed.Receipt.ReceiptCode = model.NewSubmissionID(), model.NewSubmissionReceiptCode()
	require.True(t, check(replayed), "a replay keeps the identities of the earlier seal")
}

func TestExamSubmissionManagerEndResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamSubmissionManagerEndResult
	target := automaticSealTargetFixture()
	proposed, proposedCode := model.NewSubmissionID(), model.NewSubmissionReceiptCode()
	consistent := func(*testing.T) *result {
		return &result{ExamSubmissionSealResult: sealResultFixture(target, proposed, proposedCode), ConnectionClosed: true}
	}
	check := func(r *result) bool {
		return r.ConsistentWith(target.AttemptID, target.ExamID, target.SittingID, target, proposed, proposedCode)
	}

	require.False(t, (*result)(nil).ConsistentWith(target.AttemptID, target.ExamID, target.SittingID, target, proposed, proposedCode))
	requireEachInconsistencyRejected(t, consistent, check, onEmbeddedSeal(
		func(r *result) *ExamSubmissionSealResult { return &r.ExamSubmissionSealResult },
		inconsistency[result]{"receipt without its Exam Revision", func(r *result) { r.Receipt.ExamRevisionID = "" }},
		inconsistency[result]{"negative workspace cursor", func(r *result) { r.Receipt.WorkspaceCursor = -1 }},
		inconsistency[result]{"replay that closed a Connection", func(r *result) { r.Replayed = true }},
	))
}

func TestExamSubmissionSealResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type result = ExamSubmissionSealResult
	prepared := automaticSealTargetFixture()
	proposed, proposedCode := model.NewSubmissionID(), model.NewSubmissionReceiptCode()
	target := &ExamSubmissionSealTarget{
		ExamID:            prepared.ExamID,
		SittingID:         prepared.SittingID,
		ClassID:           prepared.ClassID,
		CandidateUserID:   prepared.CandidateUserID,
		WorkspaceID:       prepared.WorkspaceID,
		CurrentRevisionID: prepared.CurrentRevisionID,
		SealAt:            consistencyAt,
	}
	sealAccess := ExamSubmissionSealAccess{
		AttemptID:                 prepared.AttemptID,
		ParticipationID:           prepared.ParticipationID,
		Generation:                prepared.Generation,
		ConnectionID:              prepared.ConnectionID,
		CandidateUserID:           prepared.CandidateUserID,
		ExpectedCurrentRevisionID: prepared.CurrentRevisionID,
		ExpectedWorkspaceCursor:   4,
	}
	consistent := func(*testing.T) *result {
		value := sealResultFixture(prepared, proposed, proposedCode)
		return &value
	}
	check := func(r *result) bool { return r.ConsistentWith(target, sealAccess, proposed, proposedCode) }

	require.False(t, (*result)(nil).ConsistentWith(target, sealAccess, proposed, proposedCode))
	requireEachInconsistencyRejected(t, consistent, check, append(sealScopeInconsistencies(),
		inconsistency[result]{"receipt at another workspace cursor", func(r *result) { r.Receipt.WorkspaceCursor++ }},
		inconsistency[result]{"receipt of another Exam Revision", func(r *result) { r.Receipt.ExamRevisionID = model.NewExamRevisionID() }},
		inconsistency[result]{"invalid browser settlement", func(r *result) { r.Receipt.BrowserActivity.InventoryRevision = 0 }},
	))
}

func TestSubmissionAuthorizationsConsistentWithRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	submissionID := model.NewSubmissionID()
	submission := func(*testing.T) *ExamSubmissionAuthorization {
		return &ExamSubmissionAuthorization{
			SubmissionID:    submissionID,
			ExamID:          model.NewExamID(),
			SittingID:       model.NewExamSittingID(),
			AttemptID:       model.NewExamAttemptID(),
			CandidateUserID: model.NewUserID(),
			AcademicUnitID:  model.NewAcademicUnitID(),
		}
	}

	t.Run("Submission", func(t *testing.T) {
		t.Parallel()
		check := func(a *ExamSubmissionAuthorization) bool { return a.ConsistentWith(submissionID) }
		require.False(t, (*ExamSubmissionAuthorization)(nil).ConsistentWith(submissionID))
		requireEachInconsistencyRejected(t, submission, check, []inconsistency[ExamSubmissionAuthorization]{
			{"another Submission", func(a *ExamSubmissionAuthorization) { a.SubmissionID = model.NewSubmissionID() }},
			{"no Exam", func(a *ExamSubmissionAuthorization) { a.ExamID = "" }},
			{"no Sitting", func(a *ExamSubmissionAuthorization) { a.SittingID = "" }},
			{"no Attempt", func(a *ExamSubmissionAuthorization) { a.AttemptID = "" }},
			{"no candidate", func(a *ExamSubmissionAuthorization) { a.CandidateUserID = "" }},
			{"no Academic Unit", func(a *ExamSubmissionAuthorization) { a.AcademicUnitID = "" }},
		})
	})
}
