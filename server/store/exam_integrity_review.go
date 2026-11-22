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
	ExamIntegrityReviewDecisionOperation      = "exam.integrity_review.decision"
	ExamIntegrityReviewDraftOperation         = "exam.integrity_review.draft"
	ExamIntegrityReviewFinalizeOperation      = "exam.integrity_review.finalize"
	ExamIntegrityReviewReleaseOperation       = "exam.integrity_review.release"
	ExamIntegrityReviewEvidenceReadMaximum    = 100
	ExamIntegrityReviewDiscrepancyReadMaximum = 100
	ExamIntegrityReviewFlagReadMaximum        = 200
)

// ExamIntegrityReviewAuthorization is a submission's ownership, used to
// authorize review and release and to conceal nested routes.
type ExamIntegrityReviewAuthorization struct {
	SubmissionID    model.SubmissionID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	AttemptID       model.ExamAttemptID
	CandidateUserID model.UserID
	AcademicUnitID  model.AcademicUnitID
}

// ConsistentWith reports whether the projection is for the submission asked
// for and names every owner.
func (a *ExamIntegrityReviewAuthorization) ConsistentWith(submissionID model.SubmissionID) bool {
	return !(a == nil ||
		a.SubmissionID != submissionID ||
		!a.ExamID.IsValid() ||
		!a.SittingID.IsValid() ||
		!a.AttemptID.IsValid() ||
		!a.CandidateUserID.IsValid() ||
		!a.AcademicUnitID.IsValid())
}

// ExamIntegrityFlagSummary is a manager's view of one flag. It holds no
// evidence detail or private candidate-authored material.
type ExamIntegrityFlagSummary struct {
	Browser                *model.BrowserIntegrityGroup
	Flag                   model.IntegrityFlag
	EvidenceCount          int
	OverflowCount          int64
	UnresolvedMissingCount int64
}

type ExamIntegrityFlagListOptions struct {
	SubmissionID model.SubmissionID
	AfterFlagID  model.IntegrityFlagID
	Limit        int
}

type ExamIntegrityFlagPage struct {
	Items   []ExamIntegrityFlagSummary
	HasMore bool
}

type ExamIntegrityEvidenceListOptions struct {
	SubmissionID    model.SubmissionID
	FlagID          model.IntegrityFlagID
	AfterEvidenceID model.IntegrityEvidenceID
	Limit           int
}

type ExamIntegrityEvidencePage struct {
	Items   []model.IntegrityEvidence
	HasMore bool
}

type ExamIntegrityDiscrepancyListOptions struct {
	SubmissionID       model.SubmissionID
	AfterDiscrepancyID model.IntegrityDiscrepancyID
	Limit              int
}

type ExamIntegrityDiscrepancyPage struct {
	Items   []model.IntegrityDiscrepancy
	HasMore bool
}

// ExamSubmissionReviewSnapshot is the manager's view of a review. It is the
// only place decision rationale and manager notes appear; candidate reads use
// a separate projection.
type ExamSubmissionReviewSnapshot struct {
	NativeConditions model.NativeConditionInventory
	// AttemptEvidence is the inventory the submission seal fixed.
	AttemptEvidence           model.AttemptEvidenceInventory
	DeliveryInventoryRevision int64
	BrowserEvidenceOverflow   *model.BrowserIntegrityOverflow
	Authorization             ExamIntegrityReviewAuthorization
	Submission                *model.ExamSubmission
	Review                    *model.SubmissionReview
	Decisions                 []model.IntegrityReviewDecision
}

type ExamIntegrityReviewDecisionMutation struct {
	SubmissionID           model.SubmissionID
	ReviewID               model.SubmissionReviewID
	DecisionID             model.IntegrityReviewDecisionID
	FlagID                 model.IntegrityFlagID
	ActorUserID            model.UserID
	ManagerOverride        bool
	ExpectedReviewRevision int64
	ExpectedRevision       int64
	Outcome                model.IntegrityReviewOutcome
	PrivateRationale       string
	ChangedAt              time.Time
	AuditEventID           model.AuditEventID
	AuditAt                time.Time
}

type ExamIntegrityReviewDraftMutation struct {
	SubmissionID           model.SubmissionID
	ReviewID               model.SubmissionReviewID
	ActorUserID            model.UserID
	ManagerOverride        bool
	ExpectedRevision       int64
	ManagerNotes           string
	StudentRemarksMarkdown string
	ChangedAt              time.Time
	AuditEventID           model.AuditEventID
	AuditAt                time.Time
}

type ExamIntegrityReviewFinalize struct {
	SubmissionID     model.SubmissionID
	ReviewID         model.SubmissionReviewID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	ChangedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

type ExamIntegrityReviewRelease struct {
	SubmissionID     model.SubmissionID
	ReviewID         model.SubmissionReviewID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	ChangedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	CandidateUserID  model.UserID
	Notice           *PreparedMail
	// Recipient is the User the Notice was written for; the write refuses it
	// if anything the mail was written from has changed since.
	Recipient *model.User
}

// ExamIntegrityReviewReleasePreparation is the reserved release time.
// Replayed is true when the release already happened, so the application must
// not prepare another candidate notice.
type ExamIntegrityReviewReleasePreparation struct {
	Replayed  bool
	ReleaseAt time.Time
}

type ExamIntegrityReviewMutationResult struct {
	Authorization ExamIntegrityReviewAuthorization
	Review        *model.SubmissionReview
	Decision      *model.IntegrityReviewDecision
	Replayed      bool
}

// ConsistentWith reports whether the result has a consistent authorization
// and a valid review for the submission, and a nil or valid decision of that
// review.
func (r *ExamIntegrityReviewMutationResult) ConsistentWith(submissionID model.SubmissionID) bool {
	return !(r == nil ||
		!r.Authorization.ConsistentWith(submissionID) ||
		r.Review == nil ||
		r.Review.Validate() != nil ||
		r.Review.SubmissionID != submissionID ||
		(r.Decision != nil && (r.Decision.Validate() != nil || r.Decision.ReviewID != r.Review.ID)))
}

// ExamIntegrityReviewStore persists the one review of a sealed submission.
// Each mutation rechecks, in one transaction, the submission and attempt
// lineage, the current manager or an explicit override, revisions, the audit
// attempt and command replay.
//
// SaveDecision and UpdateDraft create the draft when the expected review
// revision is zero and otherwise change only the current draft. Finalize locks
// the submission, the full flag and evidence inventory, the review and its
// decisions; requires one decision per flag; rejects a missing, changed or
// over-limit inventory; snapshots the inventory with a digest that also binds
// the native-condition and attempt evidence inventories; and freezes the
// review. ListAttemptEvidence pages in ID order, at most
// AttemptEvidenceReviewMaximum per page, and conceals evidence once integrity
// retention has ended.
//
// PrepareRelease reserves a millisecond release time on the database clock
// without changing the review. Release is one-way and consumes that
// preparation. A fresh release locks the candidate and recipient revision
// first, then the hierarchy, submission, attempt and review, and commits the
// released review, audit, candidate notice and its delivery Job atomically;
// encrypted mail holds the primary-key fence through commit. A replay
// rechecks authorization and returns the retained result without a notice.
// GetReleasedStudentResult conceals everything before release.
type ExamIntegrityReviewStore interface {
	ListNativeConditions(context.Context, NativeConditionListOptions) (*NativeConditionPage, error)
	ListAttemptEvidence(context.Context, AttemptEvidenceListOptions) (*AttemptEvidencePage, error)
	Resolve(context.Context, model.SubmissionID) (*ExamIntegrityReviewAuthorization, error)
	Get(context.Context, model.SubmissionID) (*ExamSubmissionReviewSnapshot, error)
	ListFlags(context.Context, ExamIntegrityFlagListOptions) (*ExamIntegrityFlagPage, error)
	ListEvidence(context.Context, ExamIntegrityEvidenceListOptions) (*ExamIntegrityEvidencePage, error)
	ListDiscrepancies(context.Context, ExamIntegrityDiscrepancyListOptions) (*ExamIntegrityDiscrepancyPage, error)
	SaveDecision(context.Context, *ExamIntegrityReviewDecisionMutation, *CommandIdempotency) (*ExamIntegrityReviewMutationResult, error)
	UpdateDraft(context.Context, *ExamIntegrityReviewDraftMutation, *CommandIdempotency) (*ExamIntegrityReviewMutationResult, error)
	Finalize(context.Context, *ExamIntegrityReviewFinalize, *CommandIdempotency) (*ExamIntegrityReviewMutationResult, error)
	PrepareRelease(context.Context, *ExamIntegrityReviewReleasePrepare) (*ExamIntegrityReviewReleasePreparation, error)
	Release(context.Context, *ExamIntegrityReviewRelease, *CommandIdempotency) (*ExamIntegrityReviewMutationResult, error)
	GetReleasedStudentResult(context.Context, model.ExamAttemptID, model.UserID) (*model.StudentResult, error)
}

// NativeConditionListOptions pages native condition records, which are
// reviewable without a flag or a decision.
type NativeConditionListOptions struct {
	SubmissionID model.SubmissionID
	AfterID      model.IntegrityEvidenceID
	Limit        int
}
type NativeConditionPage struct {
	Items   []model.NativeConditionEvidence
	HasMore bool
}

// ExamIntegrityReviewReleasePrepare names the finalized review to prepare for
// release.
type ExamIntegrityReviewReleasePrepare struct {
	SubmissionID     model.SubmissionID
	ReviewID         model.SubmissionReviewID
	ExpectedRevision int64
}
