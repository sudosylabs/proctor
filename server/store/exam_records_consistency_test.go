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
	const message = "Exam records scope projection is incomplete"
	resolved := func(scope model.RetentionHoldScope) func(*testing.T) *ExamRecordsScope {
		return func(*testing.T) *ExamRecordsScope {
			value := &ExamRecordsScope{Scope: scope, InstitutionID: model.NewInstitutionID(), AcademicUnitID: model.NewAcademicUnitID()}
			if scope.SubmissionID.IsValid() {
				value.CandidateUserID, value.AttemptID = model.NewUserID(), model.NewExamAttemptID()
			}
			return value
		}
	}

	require.EqualError(t, (*ExamRecordsScope)(nil).ConsistentWith(sittingScope), message)
	requireEachInconsistencyRejected(t, resolved(sittingScope), func(s *ExamRecordsScope) error { return s.ConsistentWith(sittingScope) }, message, []bad{
		{"another scope", func(s *ExamRecordsScope) { s.Scope.SittingID = model.NewExamSittingID() }},
		{"no Academic Unit", func(s *ExamRecordsScope) { s.AcademicUnitID = "" }},
		{"no Institution", func(s *ExamRecordsScope) { s.InstitutionID = "" }},
	})
	requireEachInconsistencyRejected(t, resolved(submissionScope), func(s *ExamRecordsScope) error { return s.ConsistentWith(submissionScope) }, message,
		[]bad{
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
	check := func(s *ExamRecordsCompletionSnapshot) error { return s.ConsistentWith(sittingID) }
	const message = "Exam records completion projection is incomplete"

	require.EqualError(t, (*ExamRecordsCompletionSnapshot)(nil).ConsistentWith(sittingID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[ExamRecordsCompletionSnapshot]{
		{"invalid completion", func(s *ExamRecordsCompletionSnapshot) { s.Completion.Revision = 0 }},
		{"completion of another Sitting", func(s *ExamRecordsCompletionSnapshot) { s.Completion.SittingID = model.NewExamSittingID() }},
		{"negative Submission count", func(s *ExamRecordsCompletionSnapshot) { s.SubmissionCount, s.PendingReviews = -1, 0 }},
		{"negative pending Reviews", func(s *ExamRecordsCompletionSnapshot) { s.PendingReviews = -1 }},
		{"more pending Reviews than Submissions", func(s *ExamRecordsCompletionSnapshot) { s.PendingReviews = 4 }},
	})
}

func TestExamRecordsHoldPageConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamRecordsHoldPage]
	scope := model.RetentionHoldScope{ExamID: model.NewExamID()}
	consistent := func(t *testing.T) *ExamRecordsHoldPage {
		return &ExamRecordsHoldPage{Holds: []model.RetentionHold{*retentionHoldFixture(t, scope), *retentionHoldFixture(t, scope)}}
	}

	require.EqualError(t, (*ExamRecordsHoldPage)(nil).ConsistentWith(2), "Retention hold page is incomplete")
	requireEachInconsistencyRejected(t, consistent, func(p *ExamRecordsHoldPage) error { return p.ConsistentWith(2) },
		"Retention hold page is incomplete", []bad{
			{"more Holds than the limit", func(p *ExamRecordsHoldPage) { p.Holds = append(p.Holds, p.Holds[0]) }},
		})
	requireEachInconsistencyRejected(t, consistent, func(p *ExamRecordsHoldPage) error { return p.HoldsConsistentWith(scope.ExamID) },
		"Retention hold projection is invalid", []bad{
			{"invalid Hold", func(p *ExamRecordsHoldPage) { p.Holds[1].Revision = 0 }},
			{"Hold of another Exam", func(p *ExamRecordsHoldPage) { p.Holds[1].Scope.ExamID = model.NewExamID() }},
		})
}

func TestExamRecordsCompletionResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	sittingID := model.NewExamSittingID()
	consistent := func(*testing.T) *ExamRecordsCompletionResult {
		return &ExamRecordsCompletionResult{Completion: model.NewExamSittingRecordsCompletion(sittingID)}
	}
	check := func(r *ExamRecordsCompletionResult) error { return r.ConsistentWith(sittingID) }
	const message = "Exam records completion result is incomplete"

	require.EqualError(t, (*ExamRecordsCompletionResult)(nil).ConsistentWith(sittingID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[ExamRecordsCompletionResult]{
		{"no completion", func(r *ExamRecordsCompletionResult) { r.Completion = nil }},
		{"invalid completion", func(r *ExamRecordsCompletionResult) { r.Completion.Revision = 0 }},
		{"completion of another Sitting", func(r *ExamRecordsCompletionResult) { r.Completion.SittingID = model.NewExamSittingID() }},
	})
}

func TestExamRecordsWaiverResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	submissionID := model.NewSubmissionID()
	consistent := func(t *testing.T) *ExamRecordsWaiverResult {
		waiver := &model.SubmissionReviewWaiver{
			SubmissionID:  submissionID,
			Revision:      1,
			ActorUserID:   model.NewUserID(),
			RecordedAt:    consistencyAt,
			ReasonCode:    "review_not_required",
			PrivateReason: "No Flags were raised.",
		}
		require.NoError(t, waiver.Validate())
		return &ExamRecordsWaiverResult{Waiver: waiver}
	}
	check := func(r *ExamRecordsWaiverResult) error { return r.ConsistentWith(submissionID) }
	const message = "Submission Review waiver result is incomplete"

	require.EqualError(t, (*ExamRecordsWaiverResult)(nil).ConsistentWith(submissionID), message)
	requireEachInconsistencyRejected(t, consistent, check, message, []inconsistency[ExamRecordsWaiverResult]{
		{"no waiver", func(r *ExamRecordsWaiverResult) { r.Waiver = nil }},
		{"invalid waiver", func(r *ExamRecordsWaiverResult) { r.Waiver.Revision = 0 }},
		{"waiver of another Submission", func(r *ExamRecordsWaiverResult) { r.Waiver.SubmissionID = model.NewSubmissionID() }},
	})
}

func TestExamRecordsHoldResultConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamRecordsHoldResult]
	scope := model.RetentionHoldScope{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID()}
	var absent *ExamRecordsHoldResult

	t.Run("ConsistentWith", func(t *testing.T) {
		t.Parallel()
		consistent := func(t *testing.T) *ExamRecordsHoldResult {
			return &ExamRecordsHoldResult{Hold: retentionHoldFixture(t, scope)}
		}
		const message = "Retention hold creation result is incomplete"
		require.EqualError(t, absent.ConsistentWith(scope), message)
		requireEachInconsistencyRejected(t, consistent, func(r *ExamRecordsHoldResult) error { return r.ConsistentWith(scope) }, message, []bad{
			{"no Hold", func(r *ExamRecordsHoldResult) { r.Hold = nil }},
			{"invalid Hold", func(r *ExamRecordsHoldResult) { r.Hold.Revision = 0 }},
			{"Hold on another scope", func(r *ExamRecordsHoldResult) { r.Hold.Scope.SittingID = model.NewExamSittingID() }},
		})
	})
	t.Run("ConsistentWithRelease", func(t *testing.T) {
		t.Parallel()
		template := retentionHoldFixture(t, scope)
		holdID := template.ID
		consistent := func(t *testing.T) *ExamRecordsHoldResult {
			hold := *template
			require.NoError(t, hold.Release(1, model.NewUserID(), "case_closed", "The appeal closed.", consistencyAt.Add(time.Hour)))
			return &ExamRecordsHoldResult{Hold: &hold}
		}
		const message = "Retention hold release result is incomplete"
		require.EqualError(t, absent.ConsistentWithRelease(holdID, scope), message)
		requireEachInconsistencyRejected(t, consistent, func(r *ExamRecordsHoldResult) error { return r.ConsistentWithRelease(holdID, scope) }, message,
			[]bad{
				{"no Hold", func(r *ExamRecordsHoldResult) { r.Hold = nil }},
				{"invalid Hold", func(r *ExamRecordsHoldResult) { r.Hold.Revision = 0 }},
				{"another Hold", func(r *ExamRecordsHoldResult) { r.Hold.ID = model.NewRetentionHoldID() }},
				{"Hold on another scope", func(r *ExamRecordsHoldResult) { r.Hold.Scope.SittingID = model.NewExamSittingID() }},
				{"Hold not released", func(r *ExamRecordsHoldResult) {
					r.Hold.ReleasedAt, r.Hold.ReleasedByUserID = model.OptionalTime{}, ""
					r.Hold.ReleaseReasonCode, r.Hold.ReleasePrivateReason = "", ""
				}},
			})
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

func TestExamExportSnapshotConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamExportSnapshot]
	template := examExportFixture(t)
	exportID := template.ID
	consistent := func(*testing.T) *ExamExportSnapshot {
		export := *template
		return &ExamExportSnapshot{Export: &export, Records: json.RawMessage(`{}`), Files: []ExamExportFile{{}}}
	}

	require.EqualError(t, (*ExamExportSnapshot)(nil).ConsistentWith(exportID), "invalid export build snapshot")
	requireEachInconsistencyRejected(t, consistent, func(s *ExamExportSnapshot) error { return s.ConsistentWith(exportID) },
		"invalid export build snapshot", []bad{
			{"no Export", func(s *ExamExportSnapshot) { s.Export = nil }},
			{"invalid Export", func(s *ExamExportSnapshot) { s.Export.PolicyRevision = 0 }},
			{"another Export", func(s *ExamExportSnapshot) { s.Export.ID = model.NewExamExportID() }},
		})
	requireEachInconsistencyRejected(t, consistent, func(s *ExamExportSnapshot) error { return s.BuildInputConsistent() },
		"invalid export build input", []bad{
			{"Export no longer queued", func(s *ExamExportSnapshot) { s.Export.State = model.ExamExportFailed }},
			{"no frozen records", func(s *ExamExportSnapshot) { s.Records = nil }},
			{"frozen records over the size bound", func(s *ExamExportSnapshot) {
				s.Records = make(json.RawMessage, model.ExamExportMaximumSnapshotBytes+1)
			}},
			{"file list disagrees with the counted files", func(s *ExamExportSnapshot) { s.Files = nil }},
		})
}

func TestExamExportDownloadConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamExportDownload]
	template := examExportFixture(t)
	exportID, scope, requesterID := template.ID, template.Scope, template.RequesterUserID
	consistent := func(*testing.T) *ExamExportDownload {
		export := *template
		return &ExamExportDownload{Export: &export, Artifact: ExamExportArtifact{ExportID: export.ID, AttemptID: model.NewJobAttemptID()}}
	}

	require.EqualError(t, (*ExamExportDownload)(nil).ConsistentWith(exportID, scope, requesterID), "invalid export read result")
	requireEachInconsistencyRejected(t, consistent, func(d *ExamExportDownload) error { return d.ConsistentWith(exportID, scope, requesterID) },
		"invalid export read result", []bad{
			{"no Export", func(d *ExamExportDownload) { d.Export = nil }},
			{"invalid Export", func(d *ExamExportDownload) { d.Export.PolicyRevision = 0 }},
			{"another Export", func(d *ExamExportDownload) { d.Export.ID = model.NewExamExportID() }},
			{"Export on another scope", func(d *ExamExportDownload) { d.Export.Scope.SittingID = model.NewExamSittingID() }},
			{"Export requested by another User", func(d *ExamExportDownload) { d.Export.RequesterUserID = model.NewUserID() }},
		})
	requireEachInconsistencyRejected(t, consistent, func(d *ExamExportDownload) error { return d.ArtifactConsistent() },
		"invalid export artifact", []bad{
			{"artifact of another Export", func(d *ExamExportDownload) { d.Artifact.ExportID = model.NewExamExportID() }},
			{"artifact without its build attempt", func(d *ExamExportDownload) { d.Artifact.AttemptID = "" }},
		})
}
