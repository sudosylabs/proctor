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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ResultReleaseBatchOperation names a bulk result release in the audit log.
const ResultReleaseBatchOperation = "exam.integrity_review.release_batch"

// ResultReleaseOutcome is what a bulk release did with one Submission.
type ResultReleaseOutcome string

const (
	// ResultReleaseReleased: this batch released the result, now or when it
	// first ran under the same key.
	ResultReleaseReleased ResultReleaseOutcome = "released"
	// ResultReleaseAlready: someone had released it before.
	ResultReleaseAlready ResultReleaseOutcome = "already_released"
	// ResultReleaseNotFinalized: its review is missing or still a draft.
	ResultReleaseNotFinalized ResultReleaseOutcome = "not_finalized"
	// ResultReleaseDenied: the caller may not release it, or it is not a
	// Submission of the sitting.
	ResultReleaseDenied ResultReleaseOutcome = "denied"
	// ResultReleaseFailed: the release could not be done now; ErrorCode says
	// why, and the item may be retried.
	ResultReleaseFailed ResultReleaseOutcome = "failed"
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
type ResultReleaseBatchItem struct {
	SubmissionID model.SubmissionID
	Outcome      ResultReleaseOutcome
	ErrorCode    string
}

// ResultReleaseBatchResult lists the outcomes in the order the Submissions
// were named, or by submission time for AllFinalized.
type ResultReleaseBatchResult struct {
	Items []ResultReleaseBatchItem
}

// ReleaseSittingResults releases many results of one sitting. The sitting is
// authorized once for release; then each Submission goes through the same
// checks, audit, mail and events as a single release, and commits on its own.
// One audit entry records the batch. Each item's idempotency key derives from
// the batch key, so a replay releases nothing twice: an item this batch
// released replays as released, and one released by someone else is already
// released.
func (s *Service) ReleaseSittingResults(
	ctx context.Context,
	inv invocation.Invocation,
	command ResultReleaseBatchCommand,
) (_ ResultReleaseBatchResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.ExamID.IsValid() || !command.SittingID.IsValid() || !validReleaseBatchSelection(command) {
		return ResultReleaseBatchResult{}, apperror.Invalid("exam.integrity_review.invalid", "result_release_batch")
	}
	if command.IdempotencyKey == "" {
		return ResultReleaseBatchResult{}, apperror.New("idempotency.key_required")
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

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    unitID.String(),
		Operation:  ResultReleaseBatchOperation,
		Value: map[string]any{
			"exam_id":         command.ExamID.String(),
			"exam_sitting_id": command.SittingID.String(),
			"all_finalized":   command.AllFinalized,
			"submission_ids":  submissionIDStrings(command.SubmissionIDs),
		},
	})
	if err != nil {
		return ResultReleaseBatchResult{}, err
	}

	rows, err := s.releaseBatchRows(ctx, inv, command)
	if err != nil {
		return ResultReleaseBatchResult{}, s.failAudit(ctx, auditID, err)
	}
	ids := command.SubmissionIDs
	if command.AllFinalized {
		ids = make([]model.SubmissionID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.SubmissionID)
		}
	}

	result := ResultReleaseBatchResult{Items: make([]ResultReleaseBatchItem, 0, len(ids))}
	counts := map[ResultReleaseOutcome]int{}
	released := false
	for _, id := range ids {
		row, found := rows[id]
		item := s.releaseBatchItem(ctx, inv, command.IdempotencyKey, id, row, found)
		counts[item.Outcome]++
		released = released || item.Outcome == ResultReleaseReleased
		result.Items = append(result.Items, item)
	}

	if err = s.auditor.Complete(ctx, auditID, map[string]any{
		"released":         counts[ResultReleaseReleased],
		"already_released": counts[ResultReleaseAlready],
		"not_finalized":    counts[ResultReleaseNotFinalized],
		"denied":           counts[ResultReleaseDenied],
		"failed":           counts[ResultReleaseFailed],
	}); err != nil {
		return ResultReleaseBatchResult{}, err
	}
	if released {
		if err = s.publishSittingSubmissionsChanged(ctx, command.ExamID, command.SittingID); err != nil {
			s.publisher.Report(ctx, "exam_sitting.submissions.changed", err)
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
) (map[model.SubmissionID]store.SubmissionListItem, error) {
	options := store.SubmissionListOptions{
		ExamID: command.ExamID, SittingID: command.SittingID, ReviewVisible: true,
		ExcludeCandidateUserID: inv.Principal().UserID, SubmissionIDs: command.SubmissionIDs,
		Sort: store.SubmissionListBySubmitted, Limit: store.SubmissionListMaximum + 1,
	}
	if command.AllFinalized {
		options.Filter = store.SubmissionListFilter{
			ReviewStates:  []store.SubmissionListReviewState{store.SubmissionListReviewFinalized},
			ReleaseStates: []model.SubmissionReviewReleaseState{model.SubmissionReviewWithheld},
		}
	}
	page, err := s.reviews.ListSubmissions(ctx, options)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if page == nil || len(page.Items) > store.SubmissionListMaximum {
		// AllFinalized releases at most one page; the caller sends the rest
		// again once these are out of the finalized, withheld set.
		if page != nil && command.AllFinalized {
			page.Items = page.Items[:store.SubmissionListMaximum]
		} else {
			return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid bulk release rows"))
		}
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
	override, authorization, err := s.authorizeReleaseMutation(ctx, inv, submissionID)
	if err != nil || !found {
		item.Outcome = ResultReleaseDenied
		if err != nil && !concealedDenial(err) {
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
	}); readErr == nil && len(now.Items) == 1 && now.Items[0].Review != nil &&
		now.Items[0].Review.ReleaseState == model.SubmissionReviewReleased {
		item.Outcome = ResultReleaseAlready
	}
	return item
}

// concealedDenial reports a refusal a single release would answer as not
// found.
func concealedDenial(err error) bool {
	code := apperror.CodeOf(publicFailures.Conceal(err), "")
	return code == "resource.not_found" || code == "exam.integrity_review.not_found" || code == "authorization.denied"
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
