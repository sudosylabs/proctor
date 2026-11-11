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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ListFlagsQuery struct {
	SubmissionID model.SubmissionID
	AfterFlagID  model.IntegrityFlagID
	Limit        int
}

type ListEvidenceQuery struct {
	SubmissionID    model.SubmissionID
	FlagID          model.IntegrityFlagID
	AfterEvidenceID model.IntegrityEvidenceID
	Limit           int
}

type ListDiscrepanciesQuery struct {
	SubmissionID       model.SubmissionID
	AfterDiscrepancyID model.IntegrityDiscrepancyID
	Limit              int
}

// GetExamIntegrityReview returns the Submission Review snapshot of a
// Submission to a caller authorized to view it.
func (s *Service) GetExamIntegrityReview(
	ctx context.Context,
	inv invocation.Invocation,
	submissionID model.SubmissionID,
) (_ *store.ExamSubmissionReviewSnapshot, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !submissionID.IsValid() {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "submission_id")
	}
	if err := s.authorizer.AuthorizeSubmissionView(ctx, inv, submissionID); err != nil {
		return nil, err
	}

	snapshot, err := s.reviews.Get(ctx, submissionID)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if !validSnapshot(snapshot, submissionID) {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent Review snapshot"))
	}

	return cloneSnapshot(snapshot), nil
}

// ListExamIntegrityFlags returns one page of a Submission's Integrity Flags
// to a caller authorized to view its Submission Review.
func (s *Service) ListExamIntegrityFlags(
	ctx context.Context,
	inv invocation.Invocation,
	query ListFlagsQuery,
) (_ *store.ExamIntegrityFlagPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.SubmissionID.IsValid() || query.Limit < 1 || query.Limit > store.ExamIntegrityReviewFlagReadMaximum {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "flag_list")
	}
	if err := s.authorizer.AuthorizeSubmissionView(ctx, inv, query.SubmissionID); err != nil {
		return nil, err
	}

	page, err := s.reviews.ListFlags(ctx, store.ExamIntegrityFlagListOptions(query))
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if page == nil || len(page.Items) > query.Limit || (page.HasMore && len(page.Items) != query.Limit) {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent Flag page"))
	}

	clone := &store.ExamIntegrityFlagPage{Items: append([]store.ExamIntegrityFlagSummary(nil), page.Items...), HasMore: page.HasMore}
	for i := range clone.Items {
		if clone.Items[i].Browser != nil {
			if clone.Items[i].Browser.Validate() != nil {
				return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid Browser evidence group"))
			}

			v := clone.Items[i].Browser.Clone()
			clone.Items[i].Browser = &v
		}
	}

	return clone, nil
}

// ListExamIntegrityEvidence returns one page of Integrity Evidence for one
// Integrity Flag of a Submission to a caller authorized to view its
// Submission Review.
func (s *Service) ListExamIntegrityEvidence(
	ctx context.Context,
	inv invocation.Invocation,
	query ListEvidenceQuery,
) (_ *store.ExamIntegrityEvidencePage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.SubmissionID.IsValid() ||
		!query.FlagID.IsValid() ||
		query.Limit < 1 ||
		query.Limit > store.ExamIntegrityReviewEvidenceReadMaximum {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "evidence_list")
	}

	if err := s.authorizer.AuthorizeSubmissionView(ctx, inv, query.SubmissionID); err != nil {
		return nil, err
	}

	page, err := s.reviews.ListEvidence(ctx, store.ExamIntegrityEvidenceListOptions(query))
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if page == nil || len(page.Items) > query.Limit || (page.HasMore && len(page.Items) != query.Limit) {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent Evidence page"))
	}

	clone := &store.ExamIntegrityEvidencePage{Items: append([]model.IntegrityEvidence(nil), page.Items...), HasMore: page.HasMore}
	for i := range clone.Items {
		if clone.Items[i].Browser != nil {
			if clone.Items[i].Browser.Validate() != nil {
				return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid Browser evidence copy"))
			}

			v := clone.Items[i].Browser.Clone()
			clone.Items[i].Browser = &v
		}
	}

	return clone, nil
}

// ListExamIntegrityDiscrepancies returns one page of a Submission's integrity
// discrepancies to a caller authorized to view its Submission Review.
func (s *Service) ListExamIntegrityDiscrepancies(
	ctx context.Context,
	inv invocation.Invocation,
	query ListDiscrepanciesQuery,
) (_ *store.ExamIntegrityDiscrepancyPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.SubmissionID.IsValid() || query.Limit < 1 || query.Limit > store.ExamIntegrityReviewDiscrepancyReadMaximum {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "discrepancy_list")
	}
	if err := s.authorizer.AuthorizeSubmissionView(ctx, inv, query.SubmissionID); err != nil {
		return nil, err
	}

	page, err := s.reviews.ListDiscrepancies(ctx, store.ExamIntegrityDiscrepancyListOptions(query))
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if page == nil || len(page.Items) > query.Limit || (page.HasMore && len(page.Items) != query.Limit) {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent Discrepancy page"))
	}

	clone := &store.ExamIntegrityDiscrepancyPage{Items: append([]model.IntegrityDiscrepancy(nil), page.Items...),
		HasMore: page.HasMore}
	return clone, nil
}

func validSnapshot(snapshot *store.ExamSubmissionReviewSnapshot, submissionID model.SubmissionID) bool {
	if snapshot == nil ||
		snapshot.NativeConditions.Validate() != nil ||
		snapshot.DeliveryInventoryRevision < 0 ||
		snapshot.Submission == nil ||
		snapshot.Submission.Validate() != nil ||
		snapshot.Submission.ID != submissionID ||
		len(snapshot.Decisions) > model.SubmissionReviewMaximumFlags {
		return false
	}

	if snapshot.BrowserEvidenceOverflow != nil && snapshot.BrowserEvidenceOverflow.Validate() != nil {
		return false
	}
	if snapshot.Review == nil {
		return len(snapshot.Decisions) == 0
	}
	if snapshot.Review.Validate() != nil || snapshot.Review.SubmissionID != submissionID {
		return false
	}

	for _, decision := range snapshot.Decisions {
		if decision.Validate() != nil || decision.ReviewID != snapshot.Review.ID {
			return false
		}
	}

	return true
}

func cloneSnapshot(snapshot *store.ExamSubmissionReviewSnapshot) *store.ExamSubmissionReviewSnapshot {
	clone := *snapshot
	if snapshot.BrowserEvidenceOverflow != nil {
		v := *snapshot.BrowserEvidenceOverflow
		clone.BrowserEvidenceOverflow = &v
	}
	submission := *snapshot.Submission
	clone.Submission = &submission
	if snapshot.Review != nil {
		review := *snapshot.Review
		clone.Review = &review
	}
	clone.Decisions = append([]model.IntegrityReviewDecision(nil), snapshot.Decisions...)
	return &clone
}
