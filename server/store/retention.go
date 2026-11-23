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
	RetentionPreviewOperation = "retention.preview"
	RetentionControlOperation = "retention.control"
)

// RetentionMutation is the caller's authority. The store rechecks it on the
// database clock under lock on every write, replays and no-ops included, so a
// retained idempotency key cannot outlive the administrator status or recent
// proof that first used it.
type RetentionMutation struct {
	Principal               model.Principal
	AuditEventID            model.AuditEventID
	AuditAt                 time.Time
	RecentAuthenticationTTL time.Duration
}

type RetentionPreviewCreation struct {
	RetentionMutation
	PreviewID              model.RetentionPreviewID
	ExpectedPolicyRevision int64
	// Proposed, when set, counts these unsaved settings instead of the saved
	// policy; the preview is then marked proposed and cannot approve cleanup.
	Proposed *model.RetentionPolicySettings
}

type RetentionControlChange struct {
	RetentionMutation
	ExpectedRevision       int64
	ExpectedPolicyRevision int64
	PreviewID              model.RetentionPreviewID
	State                  model.RetentionControlState
}

type RetentionControlResult struct {
	Control  *model.RetentionControl
	Replayed bool
}

type RetentionRecordListOptions struct {
	AfterSubmissionID model.SubmissionID
	Limit             int
}

type RetentionRecordItem struct {
	Record      model.RetentionRecord
	Eligibility model.RetentionEligibility
	Retirement  *model.RetentionRetirement
}

type RetentionRecordPage struct {
	PolicyRevision int64
	AsOf           time.Time
	Items          []RetentionRecordItem
	HasMore        bool
	// ExamTitles names each listed record's Exam by its draft title.
	ExamTitles map[model.ExamID]string
}

// RetentionExamSummaryOptions pages the Exams that have sealed records, by
// Exam ID.
type RetentionExamSummaryOptions struct {
	AfterExamID model.ExamID
	Limit       int
}

// RetentionExamSummary counts one Exam's records by category and blocker, as
// a preview counts the whole Institution.
type RetentionExamSummary struct {
	ExamID              model.ExamID
	Title               string
	Work                model.RetentionPreviewCounts
	Integrity           model.RetentionPreviewCounts
	BrowserActivity     model.RetentionPreviewCounts
	SecurityOperational model.RetentionPreviewCounts
}

type RetentionExamSummaryPage struct {
	PolicyRevision int64
	AsOf           time.Time
	Items          []RetentionExamSummary
	HasMore        bool
}

// RetentionHoldListOptions pages the Institution's holds by ID. ExamID and
// ReasonCode narrow it; released holds are left out unless IncludeReleased.
type RetentionHoldListOptions struct {
	ExamID          model.ExamID
	ReasonCode      string
	IncludeReleased bool
	After           model.RetentionHoldID
	Limit           int
}

// RetentionHoldPage is one page of holds with each Exam's draft title.
type RetentionHoldPage struct {
	Holds      []model.RetentionHold
	ExamTitles map[model.ExamID]string
	HasMore    bool
}

// RetentionReconciliation is one idempotent unit of approved retention work.
// The store decides eligibility from its own clock and state; neither the
// worker nor a preview asserts it.
type RetentionReconciliation struct {
	SubmissionID model.SubmissionID
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type RetentionReconciliationResult struct {
	Scheduled int
	Canceled  int
	Retired   int
}

// RetentionPurgeObject is content whose references have all been released:
// either a workspace object or a screen clip, whose ChunkCount keys are
// purged together. A successful delete does not complete it; completion needs
// a later, separate observation that the key is absent.
type RetentionPurgeObject struct {
	RetirementID model.RetentionRetirementID
	ObjectID     model.AttemptWorkspaceObjectID
	ScreenClipID model.ScreenClipID
	ChunkCount   int
}

// Valid reports whether the record names exactly one kind of content.
func (o RetentionPurgeObject) Valid() bool {
	if !o.RetirementID.IsValid() {
		return false
	}
	if o.ScreenClipID == "" {
		return o.ObjectID.IsValid() && o.ChunkCount == 0
	}
	return o.ObjectID == "" && o.ScreenClipID.IsValid() && o.ChunkCount > 0 && o.ChunkCount <= model.ScreenClipMaxChunks
}

type RetentionPurgeCompletion struct {
	RetirementID model.RetentionRetirementID
	ObjectID     model.AttemptWorkspaceObjectID
	ScreenClipID model.ScreenClipID
}

type RetentionNoticeMail struct {
	RetirementID    model.RetentionRetirementID
	RecipientUserID model.UserID
	Mail            *PreparedMail
	FailureCode     string
}

// RetentionStore runs the retention workflow: preview, approval, per-record
// grace, cancellation and irreversible retirement. Saving a policy never
// enables it. Mutations, replays included, reauthorize the protected
// administrator session and complete the audit atomically.
// ReconcileSubmission locks the policy and control, exam, sitting and
// submission in that order, rechecks completion and every hold, and creates
// the recipient notices in the grace transaction.
//
// Only retirement removes sealed content. It leaves a content-free receipt,
// closes late ingestion and queues unreferenced objects for a retryable purge.
// A later hold or policy change cannot bring content back.
type RetentionStore interface {
	GetControl(context.Context) (*model.RetentionControl, error)
	CreatePreview(context.Context, *RetentionPreviewCreation, *CommandIdempotency) (*model.RetentionPreview, error)
	GetPreview(context.Context, model.RetentionPreviewID) (*model.RetentionPreview, error)
	ChangeControl(context.Context, *RetentionControlChange, *CommandIdempotency) (*RetentionControlResult, error)
	ListRecords(context.Context, RetentionRecordListOptions) (*RetentionRecordPage, error)
	// SummarizeRecordsByExam counts a page of Exams' records in one snapshot.
	SummarizeRecordsByExam(context.Context, RetentionExamSummaryOptions) (*RetentionExamSummaryPage, error)
	// ListHolds pages every hold of the Institution.
	ListHolds(context.Context, RetentionHoldListOptions) (*RetentionHoldPage, error)
	ReconcileSubmission(context.Context, *RetentionReconciliation) (*RetentionReconciliationResult, error)
	// BeginPurgeBatch selects up to limit due keys, oldest first, and defers
	// each for one hour before any I/O, so concurrent callers skip them. A lost
	// result may defer keys unattempted but never records absence or releases
	// references. Callers reserve their work budget first.
	BeginPurgeBatch(context.Context, int) ([]RetentionPurgeObject, error)
	CompletePurge(context.Context, *RetentionPurgeCompletion) error
	ListNotices(context.Context, model.UserID, model.RetentionRetirementID, int) ([]model.RetentionNotice, error)
	ListPendingNotices(context.Context, int) ([]model.RetentionNotice, error)
	CompleteNotice(context.Context, *RetentionNoticeMail) error
	// Expiry is idempotent, fenced by policy and control, and completes its
	// audit atomically. Referenced or unfinished records cannot expire. A
	// resumed record gets a fresh grace period, and deleting a receipt never
	// clears a submission's retirement markers.
	ListExpiryRecords(context.Context, RetentionExpiryListOptions) (*RetentionExpiryPage, error)
	ReconcileExpiry(context.Context, *RetentionExpiryReconciliation) (*RetentionExpiryResult, error)
	ReconcileCleanupAuditExpiry(context.Context, *RetentionCleanupAuditReconciliation) (*RetentionCleanupAuditResult, error)
}
