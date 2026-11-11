// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func retentionHoldFixture(t *testing.T, scope model.RetentionHoldScope) *model.RetentionHold {
	t.Helper()
	hold := &model.RetentionHold{
		ID:              model.NewRetentionHoldID(),
		Scope:           scope,
		Revision:        1,
		CreatedAt:       consistencyAt,
		CreatedByUserID: model.NewUserID(),
		ReasonCode:      "records_review",
		PrivateReason:   "Preserve for the appeal.",
	}
	require.NoError(t, hold.Validate())
	return hold
}

func TestExamRecordsScopeConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamRecordsScope]
	sittingScope := model.RetentionHoldScope{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID()}
	submissionScope := sittingScope
	submissionScope.SubmissionID = model.NewSubmissionID()
	resolved := func(scope model.RetentionHoldScope) func(*testing.T) *ExamRecordsScope {
		return func(*testing.T) *ExamRecordsScope {
			value := &ExamRecordsScope{Scope: scope, InstitutionID: model.NewInstitutionID(), AcademicUnitID: model.NewAcademicUnitID()}
			if scope.SubmissionID.IsValid() {
				value.CandidateUserID, value.AttemptID = model.NewUserID(), model.NewExamAttemptID()
			}
			return value
		}
	}

	require.False(t, (*ExamRecordsScope)(nil).ConsistentWith(sittingScope))
	requireEachInconsistencyRejected(t, resolved(sittingScope), func(s *ExamRecordsScope) bool { return s.ConsistentWith(sittingScope) }, []bad{
		{"another scope", func(s *ExamRecordsScope) { s.Scope.SittingID = model.NewExamSittingID() }},
		{"no Academic Unit", func(s *ExamRecordsScope) { s.AcademicUnitID = "" }},
		{"no Institution", func(s *ExamRecordsScope) { s.InstitutionID = "" }},
	})
	requireEachInconsistencyRejected(t, resolved(submissionScope), func(s *ExamRecordsScope) bool { return s.ConsistentWith(submissionScope) }, []bad{
		{"Submission scope without its candidate", func(s *ExamRecordsScope) { s.CandidateUserID = "" }},
		{"Submission scope without its Attempt", func(s *ExamRecordsScope) { s.AttemptID = "" }},
	})
}

func TestExamRecordsCompletionSnapshotConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	sittingID := model.NewExamSittingID()
	consistent := func(*testing.T) *ExamRecordsCompletionSnapshot {
		return &ExamRecordsCompletionSnapshot{
			Completion:      *model.NewExamSittingRecordsCompletion(sittingID),
			SittingState:    model.ExamSittingClosed,
			SubmissionCount: 3,
			PendingReviews:  1,
		}
	}
	check := func(s *ExamRecordsCompletionSnapshot) bool { return s.ConsistentWith(sittingID) }

	require.False(t, (*ExamRecordsCompletionSnapshot)(nil).ConsistentWith(sittingID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamRecordsCompletionSnapshot]{
		{"invalid completion", func(s *ExamRecordsCompletionSnapshot) { s.Completion.Revision = 0 }},
		{"completion of another Sitting", func(s *ExamRecordsCompletionSnapshot) { s.Completion.SittingID = model.NewExamSittingID() }},
		{"negative Submission count", func(s *ExamRecordsCompletionSnapshot) { s.SubmissionCount, s.PendingReviews = -1, 0 }},
		{"negative pending Reviews", func(s *ExamRecordsCompletionSnapshot) { s.PendingReviews = -1 }},
		{"more pending Reviews than Submissions", func(s *ExamRecordsCompletionSnapshot) { s.PendingReviews = 4 }},
	})
}

func TestExamRecordsHoldResultConsistentWithReleaseRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	scope := model.RetentionHoldScope{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID()}
	template := retentionHoldFixture(t, scope)
	holdID := template.ID
	consistent := func(t *testing.T) *ExamRecordsHoldResult {
		hold := *template
		require.NoError(t, hold.Release(1, model.NewUserID(), "case_closed", "The appeal closed.", consistencyAt.Add(time.Hour)))
		return &ExamRecordsHoldResult{Hold: &hold}
	}
	check := func(r *ExamRecordsHoldResult) bool { return r.ConsistentWithRelease(holdID, scope) }

	require.False(t, (*ExamRecordsHoldResult)(nil).ConsistentWithRelease(holdID, scope))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamRecordsHoldResult]{
		{"no Hold", func(r *ExamRecordsHoldResult) { r.Hold = nil }},
		{"invalid Hold", func(r *ExamRecordsHoldResult) { r.Hold.Revision = 0 }},
		{"another Hold", func(r *ExamRecordsHoldResult) { r.Hold.ID = model.NewRetentionHoldID() }},
		{"Hold on another scope", func(r *ExamRecordsHoldResult) { r.Hold.Scope.SittingID = model.NewExamSittingID() }},
		{"Hold not released", func(r *ExamRecordsHoldResult) {
			r.Hold.ReleasedAt, r.Hold.ReleasedByUserID = model.OptionalTime{}, ""
			r.Hold.ReleaseReasonCode, r.Hold.ReleasePrivateReason = "", ""
		}},
	})
}

func examExportFixture(t *testing.T) *model.ExamExport {
	t.Helper()
	export := &model.ExamExport{
		ID:              model.NewExamExportID(),
		Scope:           model.RetentionHoldScope{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID()},
		RequesterUserID: model.NewUserID(),
		Categories:      []model.RetentionCategory{model.RetentionCategoryWork},
		State:           model.ExamExportQueued,
		PolicyRevision:  1,
		CreatedAt:       consistencyAt,
		ExpiresAt:       consistencyAt.Add(24 * time.Hour),
		SourceExpiresAt: consistencyAt.Add(time.Hour),
		SubmissionCount: 1,
		FileCount:       1,
	}
	require.NoError(t, export.Validate())
	return export
}

func TestExamExportSnapshotBuildInputConsistentRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := examExportFixture(t)
	consistent := func(*testing.T) *ExamExportSnapshot {
		export := *template
		return &ExamExportSnapshot{Export: &export, Records: json.RawMessage(`{}`), Files: []ExamExportFile{{}}}
	}
	check := func(s *ExamExportSnapshot) bool { return s.BuildInputConsistent() }

	require.False(t, (*ExamExportSnapshot)(nil).BuildInputConsistent())
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamExportSnapshot]{
		{"Export no longer queued", func(s *ExamExportSnapshot) { s.Export.State = model.ExamExportFailed }},
		{"no frozen records", func(s *ExamExportSnapshot) { s.Records = nil }},
		{"frozen records over the size bound", func(s *ExamExportSnapshot) {
			s.Records = make(json.RawMessage, model.ExamExportMaximumSnapshotBytes+1)
		}},
		{"file list disagrees with the counted files", func(s *ExamExportSnapshot) { s.Files = nil }},
	})
}

func TestExamExportDownloadConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	template := examExportFixture(t)
	exportID, scope, requesterID := template.ID, template.Scope, template.RequesterUserID
	consistent := func(*testing.T) *ExamExportDownload {
		export := *template
		return &ExamExportDownload{Export: &export, Artifact: ExamExportArtifact{ExportID: export.ID, AttemptID: model.NewJobAttemptID()}}
	}

	check := func(d *ExamExportDownload) bool { return d.ConsistentWith(exportID, scope, requesterID) }

	require.False(t, (*ExamExportDownload)(nil).ConsistentWith(exportID, scope, requesterID))
	requireEachInconsistencyRejected(t, consistent, check, []inconsistency[ExamExportDownload]{
		{"no Export", func(d *ExamExportDownload) { d.Export = nil }},
		{"invalid Export", func(d *ExamExportDownload) { d.Export.PolicyRevision = 0 }},
		{"another Export", func(d *ExamExportDownload) { d.Export.ID = model.NewExamExportID() }},
		{"Export on another scope", func(d *ExamExportDownload) { d.Export.Scope.SittingID = model.NewExamSittingID() }},
		{"Export requested by another User", func(d *ExamExportDownload) { d.Export.RequesterUserID = model.NewUserID() }},
	})
}
