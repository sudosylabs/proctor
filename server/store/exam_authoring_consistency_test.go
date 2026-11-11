// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func examFixture(t *testing.T) *model.Exam {
	t.Helper()
	exam, err := model.NewExam(model.NewExamID(), model.NewAcademicUnitID(), model.NewUserID(), consistencyAt)
	require.NoError(t, err)
	return exam
}

func TestExamAuthoringSnapshotConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamAuthoringSnapshot]
	consistent := func(t *testing.T) *ExamAuthoringSnapshot {
		return &ExamAuthoringSnapshot{Exam: examFixture(t), Draft: &model.ExamDraft{}}
	}
	check := func(s *ExamAuthoringSnapshot) error { return s.Consistent() }
	const message = "exam store returned an incomplete snapshot"

	require.EqualError(t, (*ExamAuthoringSnapshot)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []bad{
		{"no Exam", func(s *ExamAuthoringSnapshot) { s.Exam = nil }},
		{"no Draft", func(s *ExamAuthoringSnapshot) { s.Draft = nil }},
	})
}

func TestExamAccessSnapshotConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamAccessSnapshot]
	template := examFixture(t)
	examID, unitID := template.ID, template.AcademicUnitID
	consistent := func(*testing.T) *ExamAccessSnapshot {
		exam := *template
		return &ExamAccessSnapshot{Exam: &exam, ActorIsManager: true}
	}
	var absent *ExamAccessSnapshot

	t.Run("Consistent", func(t *testing.T) {
		t.Parallel()
		const message = "exam store returned no access projection"
		require.EqualError(t, absent.Consistent(), message)
		requireEachInconsistencyRejected(t, consistent, func(s *ExamAccessSnapshot) error { return s.Consistent() }, message, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
		})
	})
	t.Run("ConsistentWithExam", func(t *testing.T) {
		t.Parallel()
		const message = "Exam access projection names no owned Exam"
		check := func(s *ExamAccessSnapshot) error { return s.ConsistentWithExam(examID) }
		require.EqualError(t, absent.ConsistentWithExam(examID), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
			{"another Exam", func(s *ExamAccessSnapshot) { s.Exam.ID = model.NewExamID() }},
			{"Exam without its Academic Unit", func(s *ExamAccessSnapshot) { s.Exam.AcademicUnitID = "" }},
		})
	})
	t.Run("ConsistentWith", func(t *testing.T) {
		t.Parallel()
		const message = "Exam records access projection is incomplete"
		check := func(s *ExamAccessSnapshot) error { return s.ConsistentWith(examID, unitID) }
		require.EqualError(t, absent.ConsistentWith(examID, unitID), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
			{"another Exam", func(s *ExamAccessSnapshot) { s.Exam.ID = model.NewExamID() }},
			{"Exam of another Academic Unit", func(s *ExamAccessSnapshot) { s.Exam.AcademicUnitID = model.NewAcademicUnitID() }},
		})
	})
	t.Run("ConsistentWithValidExam", func(t *testing.T) {
		t.Parallel()
		const message = "Exam access projection is incomplete"
		check := func(s *ExamAccessSnapshot) error { return s.ConsistentWithValidExam(examID) }
		require.EqualError(t, absent.ConsistentWithValidExam(examID), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
			{"invalid Exam", func(s *ExamAccessSnapshot) { s.Exam.Revision = 0 }},
			{"another Exam", func(s *ExamAccessSnapshot) { s.Exam.ID = model.NewExamID() }},
		})
	})
}

func TestExamAuthoringCommandResultsRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamAuthoringCommandResult]
	consistent := func(*testing.T) *ExamAuthoringCommandResult {
		return &ExamAuthoringCommandResult{Value: &ExamAuthoringSnapshot{Draft: &model.ExamDraft{}}}
	}
	var absent *ExamAuthoringCommandResult

	t.Run("Consistent", func(t *testing.T) {
		t.Parallel()
		const message = "exam store returned no creation result"
		require.EqualError(t, absent.Consistent(), message)
		requireEachInconsistencyRejected(t, consistent, func(r *ExamAuthoringCommandResult) error { return r.Consistent() }, message, []bad{
			{"no snapshot", func(r *ExamAuthoringCommandResult) { r.Value = nil }},
		})
	})
	t.Run("DraftConsistent", func(t *testing.T) {
		t.Parallel()
		const message = "exam store returned no Draft update result"
		require.EqualError(t, absent.DraftConsistent(), message)
		requireEachInconsistencyRejected(t, consistent, func(r *ExamAuthoringCommandResult) error { return r.DraftConsistent() }, message, []bad{
			{"no snapshot", func(r *ExamAuthoringCommandResult) { r.Value = nil }},
			{"no Draft", func(r *ExamAuthoringCommandResult) { r.Value.Draft = nil }},
		})
	})
}

func TestExamManagerCommandResultConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamManagerCommandResult {
		return &ExamManagerCommandResult{Exam: &model.Exam{}, Manager: &model.ExamManager{}}
	}
	const message = "exam store returned no manager result"

	require.EqualError(t, (*ExamManagerCommandResult)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(r *ExamManagerCommandResult) error { return r.Consistent() }, message,
		[]inconsistency[ExamManagerCommandResult]{
			{"no Exam", func(r *ExamManagerCommandResult) { r.Exam = nil }},
			{"no Exam Manager", func(r *ExamManagerCommandResult) { r.Manager = nil }},
		})
}

func TestExamArchiveCommandResultConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamArchiveCommandResult { return &ExamArchiveCommandResult{Value: &model.Exam{}} }
	const message = "exam store returned no archive result"

	require.EqualError(t, (*ExamArchiveCommandResult)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(r *ExamArchiveCommandResult) error { return r.Consistent() }, message,
		[]inconsistency[ExamArchiveCommandResult]{
			{"no Exam", func(r *ExamArchiveCommandResult) { r.Value = nil }},
		})
}

func TestExamRevisionPublicationResultConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamRevisionPublicationResult {
		return &ExamRevisionPublicationResult{Revision: &ExamRevisionSummary{}}
	}
	const message = "Exam Revision store returned no result"

	require.EqualError(t, (*ExamRevisionPublicationResult)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(r *ExamRevisionPublicationResult) error { return r.Consistent() }, message,
		[]inconsistency[ExamRevisionPublicationResult]{
			{"no Revision summary", func(r *ExamRevisionPublicationResult) { r.Revision = nil }},
		})
}

func TestExamRevisionSummaryConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	revisionID, examID := model.NewExamRevisionID(), model.NewExamID()
	consistent := func(*testing.T) *ExamRevisionSummary {
		return &ExamRevisionSummary{ID: revisionID, ExamID: examID, Title: "Midterm"}
	}
	check := func(s *ExamRevisionSummary) error { return s.ConsistentWith(revisionID, examID) }
	const message = "result release Exam revision projection is inconsistent"

	require.EqualError(t, (*ExamRevisionSummary)(nil).ConsistentWith(revisionID, examID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[ExamRevisionSummary]{
		{"another Revision", func(s *ExamRevisionSummary) { s.ID = model.NewExamRevisionID() }},
		{"Revision of another Exam", func(s *ExamRevisionSummary) { s.ExamID = model.NewExamID() }},
		{"no title", func(s *ExamRevisionSummary) { s.Title = "" }},
	})
}
