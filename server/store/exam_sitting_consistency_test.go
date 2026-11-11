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

func TestExamSittingSnapshotConsistentWithOwningClassRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := sittingFixture(t)
	sittingID := template.ID
	consistent := func(*testing.T) *ExamSittingSnapshot {
		sitting := *template
		return &ExamSittingSnapshot{Sitting: &sitting, AcademicUnitID: model.NewAcademicUnitID()}
	}
	check := func(s *ExamSittingSnapshot) bool { return s.ConsistentWithOwningClass(sittingID) }

	require.False(t, (*ExamSittingSnapshot)(nil).ConsistentWithOwningClass(sittingID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamSittingSnapshot]{
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

	t.Run("ConsistentAndValid", func(t *testing.T) {
		t.Parallel()
		require.False(t, absent.ConsistentAndValid())
		requireEachInconsistencyRejected(t, consistent, func(s *ExamSittingSnapshot) bool { return s.ConsistentAndValid() }, []bad{
			{"no Sitting", func(s *ExamSittingSnapshot) { s.Sitting = nil }},
			{"invalid Sitting", func(s *ExamSittingSnapshot) { s.Sitting.Revision = 0 }},
		})
	})
	t.Run("ConsistentAndValidWith", func(t *testing.T) {
		t.Parallel()
		check := func(s *ExamSittingSnapshot) bool { return s.ConsistentAndValidWith(sittingID, examID) }
		require.False(t, absent.ConsistentAndValidWith(sittingID, examID))
		requireEachInconsistencyRejected(t, consistent, check, []bad{
			{"no Sitting", func(s *ExamSittingSnapshot) { s.Sitting = nil }},
			{"invalid Sitting", func(s *ExamSittingSnapshot) { s.Sitting.Revision = 0 }},
			{"another Sitting", func(s *ExamSittingSnapshot) { s.Sitting.ID = model.NewExamSittingID() }},
			{"Sitting of another Exam", func(s *ExamSittingSnapshot) { s.Sitting.ExamID = model.NewExamID() }},
		})
	})
	t.Run("ConsistentWithOwningUnit", func(t *testing.T) {
		t.Parallel()
		check := func(s *ExamSittingSnapshot) bool { return s.ConsistentWithOwningUnit(sittingID) }
		require.False(t, absent.ConsistentWithOwningUnit(sittingID))
		requireEachInconsistencyRejected(t, consistent, check, []bad{
			{"no Sitting", func(s *ExamSittingSnapshot) { s.Sitting = nil }},
			{"another Sitting", func(s *ExamSittingSnapshot) { s.Sitting.ID = model.NewExamSittingID() }},
			{"no owning Academic Unit", func(s *ExamSittingSnapshot) { s.AcademicUnitID = "" }},
		})
	})
}
