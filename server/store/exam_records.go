// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	ExamRecordsCompleteOperation    = string(model.ActionExamRecordsComplete)
	ExamRecordsWaiveReviewOperation = "exam.records.waive_review"
	ExamRecordsCreateHoldOperation  = "exam.records.hold.create"
	ExamRecordsReleaseHoldOperation = "exam.records.hold.release"
)

type ExamRecordsScope struct {
	Scope           model.RetentionHoldScope
	InstitutionID   model.InstitutionID
	AcademicUnitID  model.AcademicUnitID
	CandidateUserID model.UserID
	AttemptID       model.ExamAttemptID
}

// ConsistentWith reports an error unless the resolved records scope is the
// scope that was asked for, names the Institution and Academic Unit that own
// it, and, for a scope narrowed to one Submission, names that Submission's
// candidate and Attempt.
func (s *ExamRecordsScope) ConsistentWith(scope model.RetentionHoldScope) error {
	if s == nil ||
		s.Scope != scope ||
		!s.AcademicUnitID.IsValid() ||
		!s.InstitutionID.IsValid() ||
		scope.SubmissionID.IsValid() && (!s.CandidateUserID.IsValid() || !s.AttemptID.IsValid()) {
		return errors.New("Exam records scope projection is incomplete")
	}

	return nil
}

// ExamRecordsCompletionSnapshot exposes bounded counts, not student work.
// PendingReviews counts Submissions with neither a finalized Review nor a
// current waiver. Counts and state belong to the same database snapshot.
type ExamRecordsCompletionSnapshot struct {
	Completion      model.ExamSittingRecordsCompletion
	SittingState    model.ExamSittingState
	SubmissionCount int64
	PendingReviews  int64
}

// ConsistentWith reports an error unless the snapshot carries a valid records
// completion for the given Sitting and its counts are possible: neither is
// negative and no more Reviews are pending than there are Submissions.
func (s *ExamRecordsCompletionSnapshot) ConsistentWith(sittingID model.ExamSittingID) error {
	if s == nil ||
		s.Completion.Validate() != nil ||
		s.Completion.SittingID != sittingID ||
		s.SubmissionCount < 0 ||
		s.PendingReviews < 0 ||
		s.PendingReviews > s.SubmissionCount {
		return errors.New("Exam records completion projection is incomplete")
	}

	return nil
}

type ExamRecordsMutation struct {
	Scope        model.RetentionHoldScope
	Principal    model.Principal
	Action       model.Action
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type ExamRecordsCompletion struct {
	ExamRecordsMutation
	ExpectedRevision             int64
	AcknowledgedEvidenceRevision int64
}

type ExamRecordsReviewWaiver struct {
	ExpectedDeliveryInventoryRevision int64
	ExamRecordsMutation
	ExpectedRevision         int64 // zero creates the first waiver
	ExpectedReviewRevision   int64
	ExpectedDiscrepancyCount int64
	ReasonCode               string
	PrivateReason            string
}

type ExamRecordsHoldCreation struct {
	ExamRecordsMutation
	HoldID        model.RetentionHoldID
	ReasonCode    string
	PrivateReason string
}

type ExamRecordsHoldRelease struct {
	ExamRecordsMutation
	HoldID                  model.RetentionHoldID
	ExpectedRevision        int64
	ReasonCode              string
	PrivateReason           string
	RecentAuthenticationTTL time.Duration
}

type ExamRecordsHoldListOptions struct {
	Scope           model.RetentionHoldScope
	After           model.RetentionHoldID
	Limit           int
	IncludeReleased bool
}

type ExamRecordsHoldPage struct {
	Holds   []model.RetentionHold
	HasMore bool
}

// ConsistentWith reports an error unless the page exists and holds no more
// Retention Holds than the requested limit.
func (p *ExamRecordsHoldPage) ConsistentWith(limit int) error {
	if p == nil ||
		len(p.Holds) > limit {
		return errors.New("Retention hold page is incomplete")
	}

	return nil
}

// HoldsConsistentWith reports an error unless every Retention Hold on the page
// is valid and scoped to the given Exam.
func (p *ExamRecordsHoldPage) HoldsConsistentWith(examID model.ExamID) error {
	for _, hold := range p.Holds {
		if hold.Validate() != nil ||
			hold.Scope.ExamID != examID {
			return errors.New("Retention hold projection is invalid")
		}
	}

	return nil
}

type ExamRecordsCompletionResult struct {
	Completion *model.ExamSittingRecordsCompletion
	Replayed   bool
}

// ConsistentWith reports an error unless the outcome carries a valid records
// completion for the given Sitting.
func (r *ExamRecordsCompletionResult) ConsistentWith(sittingID model.ExamSittingID) error {
	if r == nil ||
		r.Completion.Validate() != nil ||
		r.Completion.SittingID != sittingID {
		return errors.New("Exam records completion result is incomplete")
	}

	return nil
}

type ExamRecordsWaiverResult struct {
	Waiver   *model.SubmissionReviewWaiver
	Replayed bool
}

// ConsistentWith reports an error unless the outcome carries a valid
// Submission Review waiver for the given Submission.
func (r *ExamRecordsWaiverResult) ConsistentWith(submissionID model.SubmissionID) error {
	if r == nil ||
		r.Waiver.Validate() != nil ||
		r.Waiver.SubmissionID != submissionID {
		return errors.New("Submission Review waiver result is incomplete")
	}

	return nil
}

type ExamRecordsHoldResult struct {
	Hold     *model.RetentionHold
	Replayed bool
}

// ConsistentWith reports an error unless the outcome carries a valid Retention
// Hold placed on the given scope.
func (r *ExamRecordsHoldResult) ConsistentWith(scope model.RetentionHoldScope) error {
	if r == nil ||
		r.Hold.Validate() != nil ||
		r.Hold.Scope != scope {
		return errors.New("Retention hold creation result is incomplete")
	}

	return nil
}

// ConsistentWithRelease reports an error unless the outcome carries the valid
// Retention Hold that was asked to be released, on the given scope, and that
// Hold now records its release.
func (r *ExamRecordsHoldResult) ConsistentWithRelease(holdID model.RetentionHoldID, scope model.RetentionHoldScope) error {
	if r == nil ||
		r.Hold.Validate() != nil ||
		r.Hold.ID != holdID ||
		r.Hold.Scope != scope ||
		!r.Hold.ReleasedAt.Valid {
		return errors.New("Retention hold release result is incomplete")
	}

	return nil
}

// ExamRecordsStore owns records completion and preservation. App preflights
// authorization before reads. Mutations reauthorize the current credential,
// Role, scope, Manager relationship and exact membership in the transaction,
// including idempotent replay. Completion locks the Closed Sitting and checks
// every Submission has a finalized Review or inventory-current waiver. Waivers
// exclude the candidate actor and all undecided Flags. New accepted integrity
// data stales completion atomically; exact retries do not. Holds are durable,
// bounded, manually released and apply to existing and later descendants.
// Hold release requires a current strong recent protected administrator Session.
// The transition, critical audit completion and retry outcome commit together.
// No operation here retires content, schedules cleanup or creates an export.
type ExamRecordsStore interface {
	Resolve(context.Context, model.RetentionHoldScope) (*ExamRecordsScope, error)
	GetCompletion(context.Context, model.ExamID, model.ExamSittingID) (*ExamRecordsCompletionSnapshot, error)
	FindReviewWaiver(context.Context, model.RetentionHoldScope) (*model.SubmissionReviewWaiver, bool, error)
	CompleteRecords(context.Context, *ExamRecordsCompletion, *CommandIdempotency) (*ExamRecordsCompletionResult, error)
	WaiveReview(context.Context, *ExamRecordsReviewWaiver, *CommandIdempotency) (*ExamRecordsWaiverResult, error)
	ListHolds(context.Context, ExamRecordsHoldListOptions) (*ExamRecordsHoldPage, error)
	CreateHold(context.Context, *ExamRecordsHoldCreation, *CommandIdempotency) (*ExamRecordsHoldResult, error)
	ReleaseHold(context.Context, *ExamRecordsHoldRelease, *CommandIdempotency) (*ExamRecordsHoldResult, error)
}
