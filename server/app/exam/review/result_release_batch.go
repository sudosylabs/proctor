// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ResultReleaseBatchOperation names a bulk result release in the audit log.
const ResultReleaseBatchOperation = store.ExamResultReleaseBatchOperation

// ResultReleaseOutcome is what a bulk release did with one Submission.
type ResultReleaseOutcome = store.ExamResultReleaseOutcome

const (
	// ResultReleaseReleased records a result released by this batch.
	ResultReleaseReleased = store.ExamResultReleased
	// ResultReleaseAlready records a result released before this batch.
	ResultReleaseAlready = store.ExamResultAlreadyReleased
	// ResultReleaseNotFinalized indicates the review is missing or still a draft.
	ResultReleaseNotFinalized = store.ExamResultNotFinalized
	// ResultReleaseDenied covers a submission outside the batch's sitting
	// or the caller's authority.
	ResultReleaseDenied = store.ExamResultReleaseDenied
	// ResultReleaseFailed carries the ErrorCode for a release that can be retried.
	ResultReleaseFailed = store.ExamResultReleaseFailed
)

// ResultReleaseBatchCommand releases the results of SubmissionIDs, or of
// every finalized, withheld Submission of the sitting with AllFinalized.
type ResultReleaseBatchCommand struct {
	ExamID         model.ExamID
	SittingID      model.ExamSittingID
	SubmissionIDs  []model.SubmissionID
	AllFinalized   bool
	IdempotencyKey string
}

// ResultReleaseBatchItem is one Submission's outcome.
type ResultReleaseBatchItem = store.ExamResultReleaseBatchItem

// ResultReleaseBatchResult lists the outcomes in the order the Submissions
// were named, or by submission time for AllFinalized.
type ResultReleaseBatchResult struct {
	Items []ResultReleaseBatchItem
}

// ReleaseSittingResults releases many results of one sitting. The sitting is
// authorized once for release; then each Submission goes through the same
// checks, audit, mail and events as a single release, and commits on its own.
// The batch's command key freezes the selection and the completed outcomes.
// An expired execution claim keeps its selection; per-item keys let a new
// claimant recover already executed releases.
func (s *Service) ReleaseSittingResults(
	ctx context.Context,
	inv invocation.Invocation,
	command ResultReleaseBatchCommand,
) (_ ResultReleaseBatchResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.ExamID.IsValid() || !command.SittingID.IsValid() || !validReleaseBatchSelection(command) {
		return ResultReleaseBatchResult{}, apperror.Invalid("exam.integrity_review.invalid", "result_release_batch")
	}
	prepared, err := idempotency.Require(inv, ResultReleaseBatchOperation, command.IdempotencyKey, struct {
		ExamID        model.ExamID         `json:"exam_id"`
		SittingID     model.ExamSittingID  `json:"exam_sitting_id"`
		SubmissionIDs []model.SubmissionID `json:"submission_ids,omitempty"`
		AllFinalized  bool                 `json:"all_finalized"`
	}{command.ExamID, command.SittingID, command.SubmissionIDs, command.AllFinalized})
	if err != nil {
		return ResultReleaseBatchResult{}, err
	}
	if s.authorizer.Sittings == nil {
		return ResultReleaseBatchResult{}, apperror.New("exam.integrity_review.unavailable")
	}
	management, unitID, err := s.authorizer.Sittings.AuthorizeSittingRelease(ctx, inv, command.ExamID, command.SittingID)
	if err != nil {
		return ResultReleaseBatchResult{}, err
	}
	action := model.ActionSubmissionRelease
	if management.Override {
		action = model.ActionSubmissionReleaseOverride
	}

	audit, err := s.auditor.PrepareAtScope(ctx, inv, action,
		model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()},
		model.RoleScopeAcademicUnit, unitID.String(), ResultReleaseBatchOperation, map[string]any{
			"exam_id":         command.ExamID.String(),
			"exam_sitting_id": command.SittingID.String(),
			"all_finalized":   command.AllFinalized,
			"submission_ids":  submissionIDStrings(command.SubmissionIDs),
		})
	if err != nil {
		return ResultReleaseBatchResult{}, err
	}

	// Give the database lease a margin over any one handler's work.
	workCtx, cancel := context.WithTimeout(ctx, store.ExamResultReleaseBatchWorkTime)
	defer cancel()
	claim, err := s.reviews.ClaimResultReleaseBatch(workCtx, &store.ExamResultReleaseBatchStart{
		ExamID: command.ExamID, SittingID: command.SittingID, SubmissionIDs: command.SubmissionIDs,
		AllFinalized: command.AllFinalized, Audit: audit,
	}, prepared)
	if err != nil {
		return ResultReleaseBatchResult{}, storeErrors.Translate(err)
	}
	if claim == nil {
		return ResultReleaseBatchResult{}, storeErrors.Translate(nil)
	}
	if claim.Token == "" {
		return ResultReleaseBatchResult{Items: claim.Items}, nil
	}

	rows, err := s.releaseBatchRows(workCtx, inv, command, claim.SubmissionIDs)
	if err != nil {
		return ResultReleaseBatchResult{}, err
	}
	result := ResultReleaseBatchResult{Items: make([]ResultReleaseBatchItem, 0, len(claim.SubmissionIDs))}
	released := false
	for _, id := range claim.SubmissionIDs {
		if err = workCtx.Err(); err != nil {
			return ResultReleaseBatchResult{}, storeErrors.Translate(err)
		}
		row, found := rows[id]
		item := s.releaseBatchItem(workCtx, inv, command.IdempotencyKey, id, row, found)
		released = released || item.Outcome == ResultReleaseReleased
		result.Items = append(result.Items, item)
	}

	if err = s.reviews.CompleteResultReleaseBatch(workCtx, &store.ExamResultReleaseBatchCompletion{
		Token: claim.Token, Items: result.Items,
	}, prepared); err != nil {
		return ResultReleaseBatchResult{}, storeErrors.Translate(err)
	}
	if released {
		if err = errors.Join(
			realtime.PublishSittingBoardChanged(ctx, s.publisher, command.ExamID, command.SittingID),
			s.publishSittingSubmissionsChanged(ctx, command.ExamID, command.SittingID),
		); err != nil {
			s.publisher.Report(ctx, "submission.result_release_batch", err)
		}
	}
	return result, nil
}

// releaseBatchRows reads the sitting's rows the batch names, keyed by
// Submission. The caller's own Submission is never among them. Only the
// review's state and revision are used; the listing's review visibility is
// irrelevant here because each item is authorized for release itself.
func (s *Service) releaseBatchRows(
	ctx context.Context,
	inv invocation.Invocation,
	command ResultReleaseBatchCommand,
	ids []model.SubmissionID,
) (map[model.SubmissionID]store.SubmissionListItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	page, err := s.reviews.ListSubmissions(ctx, store.SubmissionListOptions{
		ExamID: command.ExamID, SittingID: command.SittingID, ReviewVisible: true,
		ExcludeCandidateUserID: inv.Principal().UserID, SubmissionIDs: ids,
		Sort: store.SubmissionListBySubmitted, Limit: store.SubmissionListMaximum,
	})
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if page == nil || len(page.Items) > len(ids) {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid bulk release rows"))
	}
	rows := make(map[model.SubmissionID]store.SubmissionListItem, len(page.Items))
	for _, item := range page.Items {
		if item.SittingID != command.SittingID || item.ExamID != command.ExamID {
			return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("bulk release row outside its sitting"))
		}
		rows[item.SubmissionID] = item
	}
	return rows, nil
}

// releaseBatchItem authorizes one Submission as a single release does, so an
// item the caller may not release is denied before its review is looked at.
func (s *Service) releaseBatchItem(
	ctx context.Context,
	inv invocation.Invocation,
	batchKey string,
	submissionID model.SubmissionID,
	row store.SubmissionListItem,
	found bool,
) ResultReleaseBatchItem {
	item := ResultReleaseBatchItem{SubmissionID: submissionID}
	if !found {
		item.Outcome = ResultReleaseDenied
		return item
	}
	override, authorization, err := s.authorizeReleaseMutation(ctx, inv, submissionID)
	if err != nil {
		item.Outcome = ResultReleaseDenied
		if !concealedDenial(err) {
			item.Outcome, item.ErrorCode = ResultReleaseFailed, apperror.CodeOf(publicFailures.Conceal(err), "exam.integrity_review.unavailable")
		}
		return item
	}
	review := row.Review
	if review == nil || review.State != model.SubmissionReviewFinalized {
		item.Outcome = ResultReleaseNotFinalized
		return item
	}

	expected := review.Revision
	if review.ReleaseState == model.SubmissionReviewReleased {
		// A release adds one revision, so this batch's own release was fenced
		// one behind. Replaying it returns the stored outcome; a release by
		// anyone else conflicts and is reported as already released.
		expected--
	}
	key := releaseBatchItemKey(batchKey, submissionID)
	command, err := prepareTerminalIdempotency(inv, store.ExamIntegrityReviewReleaseOperation, key, submissionID, review.ID, expected)
	if err == nil {
		_, err = s.authorizedTerminalMutation(ctx, inv, terminalRun{
			kind: terminalRelease, operation: store.ExamIntegrityReviewReleaseOperation, command: command,
			override: override, authorization: authorization, submissionID: submissionID, reviewID: review.ID,
			expectedRevision: expected, inBatch: true,
		})
	}
	if err == nil {
		item.Outcome = ResultReleaseReleased
		return item
	}
	if review.ReleaseState == model.SubmissionReviewReleased {
		item.Outcome = ResultReleaseAlready
		return item
	}
	code := apperror.CodeOf(publicFailures.Conceal(err), "exam.integrity_review.unavailable")
	if code != "exam.integrity_review.revision_conflict" && code != "exam.integrity_review.state_conflict" {
		item.Outcome, item.ErrorCode = ResultReleaseFailed, code
		return item
	}
	// The review moved since the batch read it: report where it is now.
	item.Outcome = ResultReleaseNotFinalized
	if now, readErr := s.reviews.ListSubmissions(ctx, store.SubmissionListOptions{
		ExamID: row.ExamID, SittingID: row.SittingID, ReviewVisible: true, ExcludeCandidateUserID: inv.Principal().UserID,
		SubmissionIDs: []model.SubmissionID{submissionID}, Sort: store.SubmissionListBySubmitted, Limit: 1,
	}); readErr == nil && now != nil && len(now.Items) == 1 && now.Items[0].Review != nil &&
		now.Items[0].Review.ReleaseState == model.SubmissionReviewReleased {
		item.Outcome = ResultReleaseAlready
	}
	return item
}

// concealedDenial reports a refusal a single release would answer as not
// found.
func concealedDenial(err error) bool {
	concealed := publicFailures.Conceal(err)
	return apperror.Is(concealed, "resource.not_found") || apperror.Is(concealed, "exam.integrity_review.not_found") ||
		apperror.Is(concealed, "authorization.denied")
}

func validReleaseBatchSelection(command ResultReleaseBatchCommand) bool {
	if command.AllFinalized {
		return len(command.SubmissionIDs) == 0
	}
	if len(command.SubmissionIDs) < 1 || len(command.SubmissionIDs) > store.SubmissionListMaximum {
		return false
	}
	for i, id := range command.SubmissionIDs {
		if !id.IsValid() || slices.Contains(command.SubmissionIDs[:i], id) {
			return false
		}
	}
	return true
}

func releaseBatchItemKey(batchKey string, submissionID model.SubmissionID) string {
	return fmt.Sprintf("%d:%s:%s", len(batchKey), batchKey, submissionID)
}

func submissionIDStrings(ids []model.SubmissionID) []string {
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = id.String()
	}
	return values
}
