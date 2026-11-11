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

func reviewAuthorizationFixture(submissionID model.SubmissionID) ExamIntegrityReviewAuthorization {
	return ExamIntegrityReviewAuthorization{
		SubmissionID:    submissionID,
		ExamID:          model.NewExamID(),
		SittingID:       model.NewExamSittingID(),
		AttemptID:       model.NewExamAttemptID(),
		CandidateUserID: model.NewUserID(),
		AcademicUnitID:  model.NewAcademicUnitID(),
	}
}

func TestExamIntegrityReviewAuthorizationConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamIntegrityReviewAuthorization]
	submissionID := model.NewSubmissionID()
	consistent := func(*testing.T) *ExamIntegrityReviewAuthorization {
		value := reviewAuthorizationFixture(submissionID)
		return &value
	}
	check := func(a *ExamIntegrityReviewAuthorization) bool { return a.ConsistentWith(submissionID) }

	require.False(t, (*ExamIntegrityReviewAuthorization)(nil).ConsistentWith(submissionID))
	requireEachInconsistencyRejected(t, consistent, check, []bad{
		{"another Submission", func(a *ExamIntegrityReviewAuthorization) { a.SubmissionID = model.NewSubmissionID() }},
		{"no Exam", func(a *ExamIntegrityReviewAuthorization) { a.ExamID = "" }},
		{"no Sitting", func(a *ExamIntegrityReviewAuthorization) { a.SittingID = "" }},
		{"no Attempt", func(a *ExamIntegrityReviewAuthorization) { a.AttemptID = "" }},
		{"no candidate", func(a *ExamIntegrityReviewAuthorization) { a.CandidateUserID = "" }},
		{"no Academic Unit", func(a *ExamIntegrityReviewAuthorization) { a.AcademicUnitID = "" }},
	})
}

func TestExamIntegrityReviewMutationResultConsistentWithRejectsEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamIntegrityReviewMutationResult]
	submissionID := model.NewSubmissionID()
	consistent := func(t *testing.T) *ExamIntegrityReviewMutationResult {
		review, err := model.NewSubmissionReview(model.NewSubmissionReviewID(), submissionID, model.NewUserID(), consistencyAt)
		require.NoError(t, err)
		decision := &model.IntegrityReviewDecision{
			ID:               model.NewIntegrityReviewDecisionID(),
			ReviewID:         review.ID,
			FlagID:           model.NewIntegrityFlagID(),
			Outcome:          model.IntegrityReviewDismissed,
			Revision:         1,
			ActorUserID:      model.NewUserID(),
			PrivateRationale: "Explained by the proctor.",
			DecidedAt:        consistencyAt,
		}
		require.NoError(t, decision.Validate())
		return &ExamIntegrityReviewMutationResult{Authorization: reviewAuthorizationFixture(submissionID), Review: review, Decision: decision}
	}
	check := func(r *ExamIntegrityReviewMutationResult) bool { return r.ConsistentWith(submissionID) }

	require.False(t, (*ExamIntegrityReviewMutationResult)(nil).ConsistentWith(submissionID))
	requireEachInconsistencyRejected(t, consistent, check, []bad{
		{"inconsistent authorization", func(r *ExamIntegrityReviewMutationResult) { r.Authorization.ExamID = "" }},
		{"no Review", func(r *ExamIntegrityReviewMutationResult) { r.Review = nil }},
		{"invalid Review", func(r *ExamIntegrityReviewMutationResult) { r.Review.Revision = 0 }},
		{"Review of another Submission", func(r *ExamIntegrityReviewMutationResult) { r.Review.SubmissionID = model.NewSubmissionID() }},
		{"invalid Decision", func(r *ExamIntegrityReviewMutationResult) { r.Decision.Revision = 0 }},
		{"Decision of another Review", func(r *ExamIntegrityReviewMutationResult) { r.Decision.ReviewID = model.NewSubmissionReviewID() }},
	})

	undecided := consistent(t)
	undecided.Decision = nil
	require.True(t, check(undecided), "a mutation without a Decision is consistent")
}

func fileRenditionFixture(t *testing.T) *model.FileRendition {
	t.Helper()
	rendition := &model.FileRendition{
		ID:         model.NewFileRenditionID(),
		RevisionID: model.NewFileRevisionID(),
		CreatedAt:  consistencyAt,
		Name:       "paper.pdf",
		MediaType:  "application/pdf",
		Size:       10,
		SHA256:     strings.Repeat("a", 64),
	}
	require.NoError(t, rendition.Validate())
	return rendition
}

func TestExamCorrectionResourceStageConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamCorrectionResourceStage]
	examID, sittingID, baseID := model.NewExamID(), model.NewExamSittingID(), model.NewExamRevisionID()
	resourceID, actor := model.NewExamResourceID(), model.NewUserID()
	ready := func(target ExamCorrectionResourceStageTarget) func(*testing.T) *ExamCorrectionResourceStage {
		return func(t *testing.T) *ExamCorrectionResourceStage {
			rendition := fileRenditionFixture(t)
			return &ExamCorrectionResourceStage{
				ID:              model.NewExamCorrectionResourceStageID(),
				ExamID:          examID,
				SittingID:       sittingID,
				BaseRevisionID:  baseID,
				Target:          target,
				ResourceID:      resourceID,
				FileEntryID:     model.NewFileEntryID(),
				FileRevisionID:  rendition.RevisionID,
				UploadLeaseID:   model.NewUploadLeaseID(),
				RenditionID:     rendition.ID,
				CreatedByUserID: actor,
				State:           ExamCorrectionResourceStageReady,
				CreatedAt:       consistencyAt,
				ExpiresAt:       consistencyAt.Add(time.Hour),
				ReadyAt:         consistencyAt,
				Rendition:       rendition,
			}
		}
	}
	replacement := func(s *ExamCorrectionResourceStage) bool {
		return s.ConsistentWith(examID, sittingID, baseID, ExamCorrectionResourceReplacement, resourceID, actor)
	}

	require.False(t, replacement(nil))
	requireEachInconsistencyRejected(t, ready(ExamCorrectionResourceReplacement), replacement, []bad{
		{"no stage ID", func(s *ExamCorrectionResourceStage) { s.ID = "" }},
		{"another Exam", func(s *ExamCorrectionResourceStage) { s.ExamID = model.NewExamID() }},
		{"another Sitting", func(s *ExamCorrectionResourceStage) { s.SittingID = model.NewExamSittingID() }},
		{"another base Revision", func(s *ExamCorrectionResourceStage) { s.BaseRevisionID = model.NewExamRevisionID() }},
		{"another target", func(s *ExamCorrectionResourceStage) { s.Target = ExamCorrectionResourceAddition }},
		{"no Resource", func(s *ExamCorrectionResourceStage) { s.ResourceID = "" }},
		{"no file entry", func(s *ExamCorrectionResourceStage) { s.FileEntryID = "" }},
		{"no File Revision", func(s *ExamCorrectionResourceStage) { s.FileRevisionID = "" }},
		{"no upload lease", func(s *ExamCorrectionResourceStage) { s.UploadLeaseID = "" }},
		{"no rendition ID", func(s *ExamCorrectionResourceStage) { s.RenditionID = "" }},
		{"created by another User", func(s *ExamCorrectionResourceStage) { s.CreatedByUserID = model.NewUserID() }},
		{"no expiry", func(s *ExamCorrectionResourceStage) { s.ExpiresAt = time.Time{} }},
		{"replacement of another Resource", func(s *ExamCorrectionResourceStage) { s.ResourceID = model.NewExamResourceID() }},
	})

	addition := ready(ExamCorrectionResourceAddition)(t)
	require.True(t,
		addition.ConsistentWith(examID, sittingID, baseID, ExamCorrectionResourceAddition, model.NewExamResourceID(), actor),
		"an addition names its own new Resource",
	)

	rendition := func(s *ExamCorrectionResourceStage) bool { return s.RenditionConsistent() }
	require.False(t, rendition(nil))
	requireEachInconsistencyRejected(t, ready(ExamCorrectionResourceReplacement), rendition, []bad{
		{"ready without its rendition", func(s *ExamCorrectionResourceStage) { s.Rendition = nil }},
		{"rendition other than the one named", func(s *ExamCorrectionResourceStage) { s.Rendition.ID = model.NewFileRenditionID() }},
		{"rendition of another File Revision", func(s *ExamCorrectionResourceStage) { s.Rendition.RevisionID = model.NewFileRevisionID() }},
		{"invalid rendition", func(s *ExamCorrectionResourceStage) { s.Rendition.Name = "" }},
	})

	pending := ready(ExamCorrectionResourceReplacement)(t)
	pending.State, pending.Rendition = ExamCorrectionResourceStagePending, nil
	require.True(t, pending.RenditionConsistent(), "a pending stage has no rendition yet")
}

func TestExamCorrectionResultConsistencyChecksRejectEachInconsistency(t *testing.T) {
	t.Parallel()
	type bad = inconsistency[ExamCorrectionResult]
	template := sittingFixture(t)
	examID, sittingID, previousID := template.ExamID, template.ID, model.NewExamRevisionID()
	const expectedSittingRevision = 4
	consistent := func(*testing.T) *ExamCorrectionResult {
		sitting := *template
		sitting.State = model.ExamSittingOpen
		sitting.Revision = expectedSittingRevision + 1
		return &ExamCorrectionResult{
			Revision: &ExamRevisionSummary{
				ID:          sitting.ExamRevisionID,
				ExamID:      examID,
				Number:      2,
				PublishedAt: consistencyAt,
				Kind:        model.ExamRevisionPublicationLiveCorrection,
			},
			Sitting:            &ExamSittingSnapshot{Sitting: &sitting},
			PreviousRevisionID: previousID,
			EffectiveAt:        consistencyAt,
		}
	}

	require.False(t, (*ExamCorrectionResult)(nil).Consistent())
	require.False(
		t,
		(&ExamCorrectionResult{}).ConsistentWith(examID, sittingID, previousID, expectedSittingRevision),
		"no Revision or Sitting",
	)
	requireEachInconsistencyRejected(t, consistent, func(r *ExamCorrectionResult) bool { return r.Consistent() }, []bad{
		{"no Revision", func(r *ExamCorrectionResult) { r.Revision = nil }},
		{"no Sitting snapshot", func(r *ExamCorrectionResult) { r.Sitting = nil }},
		{"Sitting snapshot without its Sitting", func(r *ExamCorrectionResult) { r.Sitting.Sitting = nil }},
	})

	check := func(r *ExamCorrectionResult) bool {
		return r.ConsistentWith(examID, sittingID, previousID, expectedSittingRevision)
	}
	require.False(t, check(nil))
	requireEachInconsistencyRejected(t, consistent, check, []bad{
		{"Revision of another Exam", func(r *ExamCorrectionResult) { r.Revision.ExamID = model.NewExamID() }},
		{"Revision is the expected current one", func(r *ExamCorrectionResult) {
			r.Revision.ID, r.Sitting.Sitting.ExamRevisionID = previousID, previousID
		}},
		{"Revision is not a live correction", func(r *ExamCorrectionResult) { r.Revision.Kind = "initial" }},
		{"Revision number below one", func(r *ExamCorrectionResult) { r.Revision.Number = 0 }},
		{"follows another Revision", func(r *ExamCorrectionResult) { r.PreviousRevisionID = model.NewExamRevisionID() }},
		{"another Sitting", func(r *ExamCorrectionResult) { r.Sitting.Sitting.ID = model.NewExamSittingID() }},
		{"Sitting of another Exam", func(r *ExamCorrectionResult) { r.Sitting.Sitting.ExamID = model.NewExamID() }},
		{"Sitting on another Revision", func(r *ExamCorrectionResult) { r.Sitting.Sitting.ExamRevisionID = model.NewExamRevisionID() }},
		{"Sitting revision did not advance by one", func(r *ExamCorrectionResult) { r.Sitting.Sitting.Revision++ }},
		{"Sitting neither open nor paused", func(r *ExamCorrectionResult) { r.Sitting.Sitting.State = model.ExamSittingClosed }},
		{"Sitting without an update instant", func(r *ExamCorrectionResult) { r.Sitting.Sitting.UpdatedAt = time.Time{} }},
		{"no effective instant", func(r *ExamCorrectionResult) { r.EffectiveAt, r.Revision.PublishedAt = time.Time{}, time.Time{} }},
		{"effective at another instant than publication", func(r *ExamCorrectionResult) { r.EffectiveAt = r.EffectiveAt.Add(time.Second) }},
	})

	paused := consistent(t)
	paused.Sitting.Sitting.State = model.ExamSittingPaused
	require.True(t, check(paused), "a paused Sitting can be corrected")
}
