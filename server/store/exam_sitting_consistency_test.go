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

func TestExamSittingSnapshotConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := sittingFixture(t)
	sittingID := template.ID
	consistent := func(*testing.T) *ExamSittingSnapshot {
		sitting := *template
		return &ExamSittingSnapshot{Sitting: &sitting, AcademicUnitID: model.NewAcademicUnitID()}
	}
	check := func(s *ExamSittingSnapshot) error { return s.ConsistentWith(sittingID) }
	const message = "incomplete Sitting ownership projection"

	require.EqualError(t, (*ExamSittingSnapshot)(nil).ConsistentWith(sittingID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[ExamSittingSnapshot]{
		{"no Sitting", func(s *ExamSittingSnapshot) { s.Sitting = nil }},
		{"another Sitting", func(s *ExamSittingSnapshot) { s.Sitting.ID = model.NewExamSittingID() }},
		{"Sitting without its Class", func(s *ExamSittingSnapshot) { s.Sitting.ClassID = "" }},
	})
}

func TestExamSittingSnapshotConsistencyVariantsRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamSittingSnapshot]
	template := sittingFixture(t)
	sittingID, examID := template.ID, template.ExamID
	consistent := func(*testing.T) *ExamSittingSnapshot {
		sitting := *template
		return &ExamSittingSnapshot{Sitting: &sitting, AcademicUnitID: model.NewAcademicUnitID()}
	}
	var absent *ExamSittingSnapshot

	t.Run("Consistent", func(t *testing.T) {
		t.Parallel()
		const message = "Exam Sitting Store returned an incomplete snapshot"
		require.EqualError(t, absent.Consistent(), message)
		requireEachInconsistencyRejected(t, consistent, func(s *ExamSittingSnapshot) error { return s.Consistent() }, message, []bad{
			{"no Sitting", func(s *ExamSittingSnapshot) { s.Sitting = nil }},
			{"invalid Sitting", func(s *ExamSittingSnapshot) { s.Sitting.Revision = 0 }},
		})
	})
	t.Run("ConsistentWithExam", func(t *testing.T) {
		t.Parallel()
		const message = "result release Sitting projection is inconsistent"
		check := func(s *ExamSittingSnapshot) error { return s.ConsistentWithExam(sittingID, examID) }
		require.EqualError(t, absent.ConsistentWithExam(sittingID, examID), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []bad{
			{"no Sitting", func(s *ExamSittingSnapshot) { s.Sitting = nil }},
			{"invalid Sitting", func(s *ExamSittingSnapshot) { s.Sitting.Revision = 0 }},
			{"another Sitting", func(s *ExamSittingSnapshot) { s.Sitting.ID = model.NewExamSittingID() }},
			{"Sitting of another Exam", func(s *ExamSittingSnapshot) { s.Sitting.ExamID = model.NewExamID() }},
		})
	})
	t.Run("ConsistentWithOwningUnit", func(t *testing.T) {
		t.Parallel()
		const message = "inconsistent managed Attempt Sitting projection"
		check := func(s *ExamSittingSnapshot) error { return s.ConsistentWithOwningUnit(sittingID) }
		require.EqualError(t, absent.ConsistentWithOwningUnit(sittingID), message)
		requireEachInconsistencyRejected(t, consistent, check, message, []bad{
			{"no Sitting", func(s *ExamSittingSnapshot) { s.Sitting = nil }},
			{"another Sitting", func(s *ExamSittingSnapshot) { s.Sitting.ID = model.NewExamSittingID() }},
			{"no owning Academic Unit", func(s *ExamSittingSnapshot) { s.AcademicUnitID = "" }},
		})
	})
}

func TestExamSittingCommandResultConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	consistent := func(*testing.T) *ExamSittingCommandResult {
		return &ExamSittingCommandResult{Value: &ExamSittingSnapshot{}}
	}
	const message = "Exam Sitting Store returned an incomplete result"

	require.EqualError(t, (*ExamSittingCommandResult)(nil).Consistent(), message)
	requireEachInconsistencyRejected(t, consistent, func(r *ExamSittingCommandResult) error { return r.Consistent() }, message,
		[]inconsistency[ExamSittingCommandResult]{
			{"no snapshot", func(r *ExamSittingCommandResult) { r.Value = nil }},
		})
}

func TestExamSittingLifecycleResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := sittingFixture(t)
	sittingID := template.ID
	consistent := func(*testing.T) *ExamSittingLifecycleResult {
		sitting := *template
		return &ExamSittingLifecycleResult{Value: &ExamSittingSnapshot{Sitting: &sitting}}
	}
	check := func(r *ExamSittingLifecycleResult) error { return r.ConsistentWith(sittingID) }
	const message = "Exam Sitting lifecycle reconciliation returned an incomplete result"

	require.EqualError(t, (*ExamSittingLifecycleResult)(nil).ConsistentWith(sittingID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[ExamSittingLifecycleResult]{
		{"no snapshot", func(r *ExamSittingLifecycleResult) { r.Value = nil }},
		{"snapshot without its Sitting", func(r *ExamSittingLifecycleResult) { r.Value.Sitting = nil }},
		{"another Sitting", func(r *ExamSittingLifecycleResult) { r.Value.Sitting.ID = model.NewExamSittingID() }},
	})
}
