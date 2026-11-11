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
	check := func(s *ExamAuthoringSnapshot) bool { return s.Consistent() }

	require.False(t, (*ExamAuthoringSnapshot)(nil).Consistent())
	requireEachInconsistencyRejected(t, consistent, check, []bad{
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
		require.False(t, absent.Consistent())
		requireEachInconsistencyRejected(t, consistent, func(s *ExamAccessSnapshot) bool { return s.Consistent() }, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
		})
	})
	t.Run("ConsistentWith", func(t *testing.T) {
		t.Parallel()
		check := func(s *ExamAccessSnapshot) bool { return s.ConsistentWith(examID, unitID) }
		require.False(t, absent.ConsistentWith(examID, unitID))
		requireEachInconsistencyRejected(t, consistent, check, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
			{"another Exam", func(s *ExamAccessSnapshot) { s.Exam.ID = model.NewExamID() }},
			{"Exam of another Academic Unit", func(s *ExamAccessSnapshot) { s.Exam.AcademicUnitID = model.NewAcademicUnitID() }},
		})
	})
	t.Run("ConsistentWithOwningUnit", func(t *testing.T) {
		t.Parallel()
		check := func(s *ExamAccessSnapshot) bool { return s.ConsistentWithOwningUnit(examID) }
		require.False(t, absent.ConsistentWithOwningUnit(examID))
		requireEachInconsistencyRejected(t, consistent, check, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
			{"another Exam", func(s *ExamAccessSnapshot) { s.Exam.ID = model.NewExamID() }},
			{"Exam without its Academic Unit", func(s *ExamAccessSnapshot) { s.Exam.AcademicUnitID = "" }},
		})
	})
	t.Run("ConsistentAndValidWith", func(t *testing.T) {
		t.Parallel()
		check := func(s *ExamAccessSnapshot) bool { return s.ConsistentAndValidWith(examID) }
		require.False(t, absent.ConsistentAndValidWith(examID))
		requireEachInconsistencyRejected(t, consistent, check, []bad{
			{"no Exam", func(s *ExamAccessSnapshot) { s.Exam = nil }},
			{"invalid Exam", func(s *ExamAccessSnapshot) { s.Exam.Revision = 0 }},
			{"another Exam", func(s *ExamAccessSnapshot) { s.Exam.ID = model.NewExamID() }},
		})
	})
}

func TestExamAuthoringCommandResultConsistentWithDraftRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamAuthoringCommandResult {
		return &ExamAuthoringCommandResult{Value: &ExamAuthoringSnapshot{Draft: &model.ExamDraft{}}}
	}
	check := func(r *ExamAuthoringCommandResult) bool { return r.ConsistentWithDraft() }

	require.False(t, (*ExamAuthoringCommandResult)(nil).ConsistentWithDraft())
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamAuthoringCommandResult]{
		{"no snapshot", func(r *ExamAuthoringCommandResult) { r.Value = nil }},
		{"no Draft", func(r *ExamAuthoringCommandResult) { r.Value.Draft = nil }},
	})
}

func TestExamRevisionSummaryConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	revisionID, examID := model.NewExamRevisionID(), model.NewExamID()
	consistent := func(*testing.T) *ExamRevisionSummary {
		return &ExamRevisionSummary{ID: revisionID, ExamID: examID, Title: "Midterm"}
	}
	check := func(s *ExamRevisionSummary) bool { return s.ConsistentWith(revisionID, examID) }

	require.False(t, (*ExamRevisionSummary)(nil).ConsistentWith(revisionID, examID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamRevisionSummary]{
		{"another Revision", func(s *ExamRevisionSummary) { s.ID = model.NewExamRevisionID() }},
		{"Revision of another Exam", func(s *ExamRevisionSummary) { s.ExamID = model.NewExamID() }},
		{"no title", func(s *ExamRevisionSummary) { s.Title = "" }},
	})
}
