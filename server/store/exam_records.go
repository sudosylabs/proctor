// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
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

// ConsistentWith reports whether the scope is the one asked for, names its
// owning institution and academic unit, and, when narrowed to one submission,
// names its candidate and attempt.
func (s *ExamRecordsScope) ConsistentWith(scope model.RetentionHoldScope) bool {
	return !(s == nil ||
		s.Scope != scope ||
		!s.AcademicUnitID.IsValid() ||
		!s.InstitutionID.IsValid() ||
		scope.SubmissionID.IsValid() && (!s.CandidateUserID.IsValid() || !s.AttemptID.IsValid()))
}

// ExamRecordsCompletionSnapshot holds counts, never student work, all read in
// one database snapshot. PendingReviews counts submissions with neither a
// finalized review nor a current waiver.
type ExamRecordsCompletionSnapshot struct {
	Completion      model.ExamSittingRecordsCompletion
	SittingState    model.ExamSittingState
	SubmissionCount int64
	PendingReviews  int64
}

// ConsistentWith reports whether the completion is valid for the sitting and
// the counts are possible: non-negative, with no more pending reviews than
// submissions.
func (s *ExamRecordsCompletionSnapshot) ConsistentWith(sittingID model.ExamSittingID) bool {
	return !(s == nil ||
		s.Completion.Validate() != nil ||
		s.Completion.SittingID != sittingID ||
		s.SubmissionCount < 0 ||
		s.PendingReviews < 0 ||
		s.PendingReviews > s.SubmissionCount)
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
	// Until, when set, must follow the hold's creation on the database clock.
	Until model.OptionalTime
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

type ExamRecordsCompletionResult struct {
	Completion *model.ExamSittingRecordsCompletion
	Replayed   bool
}

type ExamRecordsWaiverResult struct {
	Waiver   *model.SubmissionReviewWaiver
	Replayed bool
}

type ExamRecordsHoldResult struct {
	Hold     *model.RetentionHold
	Replayed bool
}

// ConsistentWithRelease reports whether the result is the valid hold asked
// for, on the given scope, and satisfies Released.
func (r *ExamRecordsHoldResult) ConsistentWithRelease(holdID model.RetentionHoldID, scope model.RetentionHoldScope) bool {
	return !(r == nil ||
		r.Hold.Validate() != nil ||
		r.Hold.ID != holdID ||
		r.Hold.Scope != scope ||
		!r.Released())
}

// Released is the application's postcondition on a hold release.
func (r *ExamRecordsHoldResult) Released() bool {
	return r.Hold.ReleasedAt.Valid
}

// ExamRecordsStore persists records completion, review waivers and retention
// holds. Reads trust the application's authorization; mutations, replays
// included, reauthorize the credential, role, scope, manager relationship and
// membership in the transaction. CompleteRecords locks the closed sitting and
// requires every submission to have a finalized review or a waiver current
// with the inventory. A waiver cannot be granted by the candidate or with
// undecided flags. New integrity data marks completion stale atomically. Holds
// last until released by hand and cover later descendants too; releasing one
// requires a strong, recent protected administrator session. Nothing here
// retires content, schedules cleanup or creates an export.
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
