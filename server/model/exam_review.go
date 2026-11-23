// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	IntegrityReviewPrivateRationaleMaximumRunes = 1000
	IntegrityReviewPrivateRationaleMaximumBytes = 4000
	// The private review projection is kept as one bounded idempotency
	// outcome; keep both fields well below that store envelope, including
	// JSON framing and aggregate metadata.
	SubmissionReviewPrivateNotesMaximumRunes   = 3000
	SubmissionReviewPrivateNotesMaximumBytes   = 12000
	SubmissionReviewStudentRemarksMaximumRunes = 8192
	SubmissionReviewStudentRemarksMaximumBytes = 32768
	SubmissionReviewMaximumFlags               = 456
	SubmissionReviewMaximumEvidence            = 30000
	SubmissionReviewMaximumDiscrepancies       = 200
)

// IntegrityDiscrepancyKind is a bounded uncertainty record on a sealed
// submission. A discrepancy is never reinterpreted as evidence and never
// changes the submission.
type IntegrityDiscrepancyKind string

const (
	IntegrityDiscrepancyLateFocusLoss                    IntegrityDiscrepancyKind = "late_focus_loss"
	IntegrityDiscrepancyFocusLossGap                     IntegrityDiscrepancyKind = "focus_loss_gap"
	IntegrityDiscrepancyBrowserActivityGap               IntegrityDiscrepancyKind = "browser_activity_gap"
	IntegrityDiscrepancyCorrectionAcknowledgementMissing IntegrityDiscrepancyKind = "correction_acknowledgement_missing"
)

type IntegrityDiscrepancyFocusLossGapReason string

const (
	IntegrityDiscrepancyFocusLossSequenceGap                      IntegrityDiscrepancyFocusLossGapReason = "sequence_gap"
	IntegrityDiscrepancyFocusLossSourceNotFinalized               IntegrityDiscrepancyFocusLossGapReason = "source_not_finalized"
	IntegrityDiscrepancyFocusLossSequenceGapAndSourceNotFinalized IntegrityDiscrepancyFocusLossGapReason = "sequence_gap_and_source_not_finalized"
)

func (r IntegrityDiscrepancyFocusLossGapReason) IsValid() bool {
	return r == IntegrityDiscrepancyFocusLossSequenceGap ||
		r == IntegrityDiscrepancyFocusLossSourceNotFinalized ||
		r == IntegrityDiscrepancyFocusLossSequenceGapAndSourceNotFinalized
}

type IntegrityDiscrepancyBrowserActivityGapReason string

const IntegrityDiscrepancyBrowserActivityPriorSourceGap IntegrityDiscrepancyBrowserActivityGapReason = "prior_source_gap"

func (r IntegrityDiscrepancyBrowserActivityGapReason) IsValid() bool {
	return r == IntegrityDiscrepancyBrowserActivityPriorSourceGap ||
		BrowserActivitySubmissionGapReason(r).IsValid()
}

// IntegrityDiscrepancySpecification is the whole immutable value of one late
// record. ReceivedAt is the authoritative database receipt time.
type IntegrityDiscrepancySpecification struct {
	ID                     IntegrityDiscrepancyID
	SubmissionID           SubmissionID
	AttemptID              ExamAttemptID
	ParticipationID        AttemptParticipationID
	Generation             int64
	Kind                   IntegrityDiscrepancyKind
	SignalID               FocusLossSignalID
	Sequence               int64
	DurationMilliseconds   int64
	Source                 FocusLossSource
	MissingBefore          int64
	CorrectionRevisionID   ExamRevisionID
	BrowserSourceSessionID BrowserSourceSessionID
	FinalSequence          *int64
	GapReason              string
	UnresolvedCount        int64
	ReceivedAt             time.Time
}

// IntegrityDiscrepancy is an immutable, bounded diagnostic recorded after
// collection. Managers see it only through submission review.
type IntegrityDiscrepancy struct {
	IntegrityDiscrepancySpecification
}

func NewIntegrityDiscrepancy(specification IntegrityDiscrepancySpecification) (*IntegrityDiscrepancy, error) {
	value := &IntegrityDiscrepancy{IntegrityDiscrepancySpecification: specification}
	if value.FinalSequence != nil {
		sequence := *value.FinalSequence
		value.FinalSequence = &sequence
	}
	value.ReceivedAt = TimeUTC(value.ReceivedAt)
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return value, nil
}

func (v *IntegrityDiscrepancy) Validate() error {
	if v == nil ||
		!v.ID.IsValid() ||
		!v.SubmissionID.IsValid() ||
		!v.AttemptID.IsValid() ||
		!v.ParticipationID.IsValid() ||
		v.Generation < 1 ||
		v.ReceivedAt.IsZero() {
		return invalidModelError("IntegrityDiscrepancy.Validate", "integrity_discrepancy", "", "invalid Integrity Discrepancy", "")
	}

	switch v.Kind {
	case IntegrityDiscrepancyLateFocusLoss:
		if !v.SignalID.IsValid() ||
			v.Sequence < 1 ||
			v.DurationMilliseconds < 1 ||
			v.DurationMilliseconds > FocusLossMaximumDurationMilliseconds ||
			!v.Source.IsValid() ||
			v.MissingBefore < 0 ||
			v.CorrectionRevisionID != "" ||
			v.BrowserSourceSessionID != "" ||
			v.FinalSequence != nil ||
			v.GapReason != "" ||
			v.UnresolvedCount != 0 {
			return invalidModelError(
				"IntegrityDiscrepancy.Validate",
				"integrity_discrepancy",
				"",
				"invalid late Focus Loss Integrity Discrepancy",
				"",
			)
		}
	case IntegrityDiscrepancyFocusLossGap:
		if v.SignalID != "" ||
			v.Sequence != 0 ||
			v.DurationMilliseconds != 0 ||
			v.Source != "" ||
			v.MissingBefore != 0 ||
			v.CorrectionRevisionID != "" ||
			v.BrowserSourceSessionID != "" ||
			v.FinalSequence != nil ||
			!IntegrityDiscrepancyFocusLossGapReason(v.GapReason).IsValid() ||
			v.UnresolvedCount < 1 {
			return invalidModelError(
				"IntegrityDiscrepancy.Validate",
				"integrity_discrepancy",
				"",
				"invalid Focus Loss gap Integrity Discrepancy",
				"",
			)
		}
	case IntegrityDiscrepancyBrowserActivityGap:
		if v.SignalID != "" ||
			v.Sequence != 0 ||
			v.DurationMilliseconds != 0 ||
			v.Source != "" ||
			v.MissingBefore != 0 ||
			v.CorrectionRevisionID != "" ||
			!v.BrowserSourceSessionID.IsValid() ||
			v.FinalSequence != nil && *v.FinalSequence < 1 ||
			!IntegrityDiscrepancyBrowserActivityGapReason(v.GapReason).IsValid() ||
			v.UnresolvedCount < 1 {
			return invalidModelError(
				"IntegrityDiscrepancy.Validate",
				"integrity_discrepancy",
				"",
				"invalid Browser Activity gap Integrity Discrepancy",
				"",
			)
		}
	case IntegrityDiscrepancyCorrectionAcknowledgementMissing:
		if v.SignalID != "" ||
			v.Sequence != 0 ||
			v.DurationMilliseconds != 0 ||
			v.Source != "" ||
			v.MissingBefore != 0 ||
			!v.CorrectionRevisionID.IsValid() ||
			v.BrowserSourceSessionID != "" ||
			v.FinalSequence != nil ||
			v.GapReason != "" ||
			v.UnresolvedCount != 1 {
			return invalidModelError(
				"IntegrityDiscrepancy.Validate",
				"integrity_discrepancy",
				"",
				"invalid correction acknowledgement Integrity Discrepancy",
				"",
			)
		}
	default:
		return invalidModelError("IntegrityDiscrepancy.Validate", "integrity_discrepancy", "", "invalid Integrity Discrepancy kind", "")
	}

	return nil
}

// IntegrityReviewOutcome is a manager's non-academic disposition of one
// integrity flag. It is not a grade, a score or a verdict of guilt.
type IntegrityReviewOutcome string

const (
	IntegrityReviewConfirmed    IntegrityReviewOutcome = "confirmed"
	IntegrityReviewDismissed    IntegrityReviewOutcome = "dismissed"
	IntegrityReviewInconclusive IntegrityReviewOutcome = "inconclusive"
)

func (o IntegrityReviewOutcome) IsValid() bool {
	return o == IntegrityReviewConfirmed || o == IntegrityReviewDismissed || o == IntegrityReviewInconclusive
}

// IntegrityReviewDecision is the one current disposition of a flag within a
// draft review, fenced by revision. PrivateRationale is manager-only.
type IntegrityReviewDecision struct {
	InventoryStale   bool
	ID               IntegrityReviewDecisionID
	ReviewID         SubmissionReviewID
	FlagID           IntegrityFlagID
	Outcome          IntegrityReviewOutcome
	Revision         int64
	ActorUserID      UserID
	PrivateRationale string
	DecidedAt        time.Time
}

func NewIntegrityReviewDecision(
	id IntegrityReviewDecisionID,
	reviewID SubmissionReviewID,
	flagID IntegrityFlagID,
	outcome IntegrityReviewOutcome,
	actorID UserID,
	rationale string,
	at time.Time,
) (*IntegrityReviewDecision, error) {
	decision := &IntegrityReviewDecision{
		ID:               id,
		ReviewID:         reviewID,
		FlagID:           flagID,
		Outcome:          outcome,
		Revision:         1,
		ActorUserID:      actorID,
		PrivateRationale: rationale,
		DecidedAt:        TimeUTC(at),
	}
	if err := decision.Validate(); err != nil {
		return nil, err
	}

	return decision, nil
}

func (d *IntegrityReviewDecision) Validate() error {
	if d == nil ||
		!d.ID.IsValid() ||
		!d.ReviewID.IsValid() ||
		!d.FlagID.IsValid() ||
		!d.Outcome.IsValid() ||
		d.Revision < 1 ||
		!d.ActorUserID.IsValid() ||
		d.DecidedAt.IsZero() ||
		!validBoundedReviewText(
			d.PrivateRationale,
			1,
			IntegrityReviewPrivateRationaleMaximumRunes,
			IntegrityReviewPrivateRationaleMaximumBytes,
		) {
		return invalidModelError("IntegrityReviewDecision.Validate", "integrity_review_decision", "", "invalid Integrity Review decision", "")
	}

	return nil
}

func (d *IntegrityReviewDecision) Revise(
	expectedRevision int64,
	outcome IntegrityReviewOutcome,
	actorID UserID,
	rationale string,
	at time.Time,
) error {
	if d == nil || d.Validate() != nil || d.Revision != expectedRevision {
		return invalidModelError(
			"IntegrityReviewDecision.Revise",
			"integrity_review_decision",
			"",
			"Integrity Review decision revision conflict",
			"",
		)
	}

	candidate := *d
	candidate.InventoryStale = false
	candidate.Outcome = outcome
	candidate.Revision++
	candidate.ActorUserID = actorID
	candidate.PrivateRationale = rationale
	candidate.DecidedAt = TimeUTC(at)
	if err := candidate.Validate(); err != nil {
		return err
	}

	*d = candidate
	return nil
}

type SubmissionReviewState string

const (
	SubmissionReviewDraft     SubmissionReviewState = "draft"
	SubmissionReviewFinalized SubmissionReviewState = "finalized"
)

type SubmissionReviewReleaseState string

const (
	SubmissionReviewWithheld SubmissionReviewReleaseState = "withheld"
	SubmissionReviewReleased SubmissionReviewReleaseState = "released"
)

// SubmissionReview is the one integrity review of a sealed submission, fenced
// by revision. Finalization freezes its text, decisions and inventory digest;
// release is a later one-way step.
type SubmissionReview struct {
	ID                      SubmissionReviewID
	SubmissionID            SubmissionID
	State                   SubmissionReviewState
	ReleaseState            SubmissionReviewReleaseState
	Revision                int64
	CreatedByUserID         UserID
	ManagerNotes            string
	StudentRemarksMarkdown  string
	FlagCount               int
	EvidenceCount           int
	DiscrepancyCount        int
	EvidenceInventoryDigest string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	FinalizedAt             OptionalTime
	FinalizedByUserID       UserID
	ReleasedAt              OptionalTime
	ReleasedByUserID        UserID
}

func NewSubmissionReview(id SubmissionReviewID, submissionID SubmissionID, actorID UserID, at time.Time) (*SubmissionReview, error) {
	at = TimeUTC(at)
	review := &SubmissionReview{
		ID:              id,
		SubmissionID:    submissionID,
		State:           SubmissionReviewDraft,
		ReleaseState:    SubmissionReviewWithheld,
		Revision:        1,
		CreatedByUserID: actorID,
		CreatedAt:       at,
		UpdatedAt:       at,
	}
	if err := review.Validate(); err != nil {
		return nil, err
	}

	return review, nil
}

func (r *SubmissionReview) Validate() error {
	if r == nil ||
		!r.ID.IsValid() ||
		!r.SubmissionID.IsValid() ||
		r.Revision < 1 ||
		!r.CreatedByUserID.IsValid() ||
		r.CreatedAt.IsZero() ||
		r.UpdatedAt.IsZero() ||
		r.UpdatedAt.Before(r.CreatedAt) ||
		!validBoundedReviewText(r.ManagerNotes, 0, SubmissionReviewPrivateNotesMaximumRunes, SubmissionReviewPrivateNotesMaximumBytes) ||
		!validBoundedReviewText(
			r.StudentRemarksMarkdown,
			0,
			SubmissionReviewStudentRemarksMaximumRunes,
			SubmissionReviewStudentRemarksMaximumBytes,
		) {
		return invalidModelError("SubmissionReview.Validate", "submission_review", "", "invalid Submission Review", "")
	}

	switch r.State {
	case SubmissionReviewDraft:
		if r.ReleaseState != SubmissionReviewWithheld ||
			r.FlagCount != 0 ||
			r.EvidenceCount != 0 ||
			r.DiscrepancyCount != 0 ||
			r.EvidenceInventoryDigest != "" ||
			r.FinalizedAt.Valid ||
			!r.FinalizedByUserID.IsZero() ||
			r.ReleasedAt.Valid ||
			!r.ReleasedByUserID.IsZero() {
			return invalidModelError("SubmissionReview.Validate", "submission_review", "", "invalid draft Submission Review", "")
		}
	case SubmissionReviewFinalized:
		if r.FlagCount < 0 ||
			r.FlagCount > SubmissionReviewMaximumFlags ||
			r.EvidenceCount < 0 ||
			r.EvidenceCount > SubmissionReviewMaximumEvidence ||
			r.DiscrepancyCount < 0 ||
			r.DiscrepancyCount > SubmissionReviewMaximumDiscrepancies ||
			!validLowerSHA256(r.EvidenceInventoryDigest) ||
			!r.FinalizedAt.Valid ||
			r.FinalizedAt.Time.Before(r.CreatedAt) ||
			!r.FinalizedByUserID.IsValid() {
			return invalidModelError("SubmissionReview.Validate", "submission_review", "", "invalid finalized Submission Review", "")
		}

		switch r.ReleaseState {
		case SubmissionReviewWithheld:
			if r.ReleasedAt.Valid || !r.ReleasedByUserID.IsZero() {
				return invalidModelError("SubmissionReview.Validate", "submission_review", "", "withheld Submission Review has release metadata", "")
			}
		case SubmissionReviewReleased:
			if !r.ReleasedAt.Valid || r.ReleasedAt.Time.Before(r.FinalizedAt.Time) || !r.ReleasedByUserID.IsValid() {
				return invalidModelError("SubmissionReview.Validate", "submission_review", "", "invalid released Submission Review", "")
			}
		default:
			return invalidModelError("SubmissionReview.Validate", "submission_review", "", "invalid Submission Review release state", "")
		}
	default:
		return invalidModelError("SubmissionReview.Validate", "submission_review", "", "invalid Submission Review state", "")
	}

	return nil
}

func (r *SubmissionReview) UpdateDraft(expectedRevision int64, managerNotes, studentRemarks string, at time.Time) error {
	if r == nil || r.Validate() != nil || r.State != SubmissionReviewDraft || r.Revision != expectedRevision {
		return invalidModelError("SubmissionReview.UpdateDraft", "submission_review", "", "Submission Review cannot be edited", "")
	}
	candidate := *r
	candidate.ManagerNotes = managerNotes
	candidate.StudentRemarksMarkdown = studentRemarks
	candidate.Revision++
	candidate.UpdatedAt = TimeUTC(at)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

// TouchDraft bumps the review's revision when a flag decision is created or
// revised without changing the review text.
func (r *SubmissionReview) TouchDraft(expectedRevision int64, at time.Time) error {
	if r == nil || r.Validate() != nil || r.State != SubmissionReviewDraft || r.Revision != expectedRevision {
		return invalidModelError("SubmissionReview.TouchDraft", "submission_review", "", "Submission Review cannot advance", "")
	}
	candidate := *r
	candidate.Revision++
	candidate.UpdatedAt = TimeUTC(at)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

func (r *SubmissionReview) Finalize(expectedRevision int64, actorID UserID, flagCount, evidenceCount,
	discrepancyCount int, inventoryDigest string, at time.Time,
) error {
	if r == nil || r.Validate() != nil || r.State != SubmissionReviewDraft || r.Revision != expectedRevision {
		return invalidModelError("SubmissionReview.Finalize", "submission_review", "", "Submission Review cannot be finalized", "")
	}
	candidate := *r
	candidate.State = SubmissionReviewFinalized
	candidate.Revision++
	candidate.FlagCount = flagCount
	candidate.EvidenceCount = evidenceCount
	candidate.DiscrepancyCount = discrepancyCount
	candidate.EvidenceInventoryDigest = inventoryDigest
	candidate.FinalizedAt = OptionalTimeFrom(at)
	candidate.FinalizedByUserID = actorID
	candidate.UpdatedAt = TimeUTC(at)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

func (r *SubmissionReview) Release(expectedRevision int64, actorID UserID, at time.Time) error {
	if r == nil ||
		r.Validate() != nil ||
		r.State != SubmissionReviewFinalized ||
		r.ReleaseState != SubmissionReviewWithheld ||
		r.Revision != expectedRevision {
		return invalidModelError("SubmissionReview.Release", "submission_review", "", "Submission Review cannot be released", "")
	}

	candidate := *r
	candidate.ReleaseState = SubmissionReviewReleased
	candidate.Revision++
	candidate.ReleasedAt = OptionalTimeFrom(at)
	candidate.ReleasedByUserID = actorID
	candidate.UpdatedAt = TimeUTC(at)
	if err := candidate.Validate(); err != nil {
		return err
	}

	*r = candidate
	return nil
}

// SubmissionReviewReopenReasonMaxRunes bounds the private reason a manager
// gives for reopening a finalized review.
const SubmissionReviewReopenReasonMaxRunes = 1000

// Reopen returns a finalized, unreleased review to a draft so its decisions
// can be revised. The decisions stay; the frozen inventory stays as history
// under its finalization revision. A released result is final.
func (r *SubmissionReview) Reopen(expectedRevision int64, at time.Time) error {
	if r == nil ||
		r.Validate() != nil ||
		r.State != SubmissionReviewFinalized ||
		r.ReleaseState != SubmissionReviewWithheld ||
		r.Revision != expectedRevision {
		return invalidModelError("SubmissionReview.Reopen", "submission_review", "", "Submission Review cannot be reopened", "")
	}

	candidate := *r
	candidate.State = SubmissionReviewDraft
	candidate.Revision++
	candidate.FlagCount, candidate.EvidenceCount, candidate.DiscrepancyCount = 0, 0, 0
	candidate.EvidenceInventoryDigest = ""
	candidate.FinalizedAt = OptionalTime{}
	candidate.FinalizedByUserID = ""
	candidate.UpdatedAt = TimeUTC(at)
	if candidate.UpdatedAt.Before(r.UpdatedAt) {
		candidate.UpdatedAt = r.UpdatedAt
	}
	if err := candidate.Validate(); err != nil {
		return err
	}

	*r = candidate
	return nil
}

// StudentResult is everything the candidate sees on release. It leaves out
// evidence, decisions, private rationale, manager notes and sealed content.
type StudentResult struct {
	ReviewID               SubmissionReviewID
	SubmissionID           SubmissionID
	AttemptID              ExamAttemptID
	CandidateUserID        UserID
	StudentRemarksMarkdown string
	ReleasedAt             time.Time
}

func (r *SubmissionReview) StudentResult(attemptID ExamAttemptID, candidateID UserID) (*StudentResult, error) {
	if r == nil ||
		r.Validate() != nil ||
		r.State != SubmissionReviewFinalized ||
		r.ReleaseState != SubmissionReviewReleased ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() {
		return nil, invalidModelError("SubmissionReview.StudentResult", "submission_review", "", "Student Result is unavailable", "")
	}

	return &StudentResult{
		ReviewID:               r.ID,
		SubmissionID:           r.SubmissionID,
		AttemptID:              attemptID,
		CandidateUserID:        candidateID,
		StudentRemarksMarkdown: r.StudentRemarksMarkdown,
		ReleasedAt:             r.ReleasedAt.Time,
	}, nil
}

func validBoundedReviewText(value string, minimumRunes, maximumRunes, maximumBytes int) bool {
	if !utf8.ValidString(value) || value != strings.TrimSpace(value) || len(value) > maximumBytes {
		return false
	}
	runes := utf8.RuneCountInString(value)
	return runes >= minimumRunes && runes <= maximumRunes
}

type IntegrityReviewInventoryFlag struct {
	FlagID           IntegrityFlagID
	DecisionID       IntegrityReviewDecisionID
	DecisionRevision int64
}

type IntegrityReviewInventoryEvidence struct {
	EvidenceID IntegrityEvidenceID
	FlagID     IntegrityFlagID
}

type IntegrityReviewInventoryDiscrepancy struct {
	DiscrepancyID IntegrityDiscrepancyID
}

// IntegrityReviewInventoryDigest returns the version-1 digest that freezes the
// exact flag, decision revision and evidence IDs finalization saw. Input order
// does not matter and the slices are not mutated.
func IntegrityReviewInventoryDigest(
	flags []IntegrityReviewInventoryFlag,
	evidence []IntegrityReviewInventoryEvidence,
	discrepancies []IntegrityReviewInventoryDiscrepancy,
) (string, error) {
	if len(flags) > SubmissionReviewMaximumFlags ||
		len(evidence) > SubmissionReviewMaximumEvidence ||
		len(discrepancies) > SubmissionReviewMaximumDiscrepancies {
		return "", invalidModelError("IntegrityReviewInventoryDigest", "exam_review", "", "Integrity Review inventory exceeds bounds", "")
	}

	flagCopy := append([]IntegrityReviewInventoryFlag(nil), flags...)
	evidenceCopy := append([]IntegrityReviewInventoryEvidence(nil), evidence...)
	discrepancyCopy := append([]IntegrityReviewInventoryDiscrepancy(nil), discrepancies...)
	for _, item := range flagCopy {
		if !item.FlagID.IsValid() || !item.DecisionID.IsValid() || item.DecisionRevision < 1 {
			return "", invalidModelError("IntegrityReviewInventoryDigest", "exam_review", "", "invalid Integrity Review Flag inventory", "")
		}
	}

	for _, item := range evidenceCopy {
		if !item.EvidenceID.IsValid() || !item.FlagID.IsValid() {
			return "", invalidModelError("IntegrityReviewInventoryDigest", "exam_review", "", "invalid Integrity Review Evidence inventory", "")
		}
	}

	for _, item := range discrepancyCopy {
		if !item.DiscrepancyID.IsValid() {
			return "", invalidModelError("IntegrityReviewInventoryDigest", "exam_review", "", "invalid Integrity Review Discrepancy inventory", "")
		}
	}

	sort.Slice(flagCopy, func(i, j int) bool { return flagCopy[i].FlagID.String() < flagCopy[j].FlagID.String() })
	sort.Slice(evidenceCopy, func(i, j int) bool { return evidenceCopy[i].EvidenceID.String() < evidenceCopy[j].EvidenceID.String() })
	sort.Slice(discrepancyCopy, func(i, j int) bool {
		return discrepancyCopy[i].DiscrepancyID.String() < discrepancyCopy[j].DiscrepancyID.String()
	})
	hash := sha256.New()
	hash.Write([]byte("proctor.integrity_review.inventory\x00"))
	writeReviewInventoryCount(hash, len(flagCopy))
	for _, item := range flagCopy {
		hash.Write([]byte(item.FlagID.String()))
		hash.Write([]byte(item.DecisionID.String()))
		var revision [8]byte
		binary.BigEndian.PutUint64(revision[:], uint64(item.DecisionRevision)) //nolint:gosec // G115: DecisionRevision is checked to be at least 1 above.
		hash.Write(revision[:])
	}

	writeReviewInventoryCount(hash, len(evidenceCopy))
	for _, item := range evidenceCopy {
		hash.Write([]byte(item.EvidenceID.String()))
		hash.Write([]byte(item.FlagID.String()))
	}

	writeReviewInventoryCount(hash, len(discrepancyCopy))
	for _, item := range discrepancyCopy {
		hash.Write([]byte(item.DiscrepancyID.String()))
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

type reviewInventoryWriter interface{ Write([]byte) (int, error) }

func writeReviewInventoryCount(writer reviewInventoryWriter, count int) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], uint32(count)) //nolint:gosec // G115: every count is a slice length capped by the SubmissionReviewMaximum* limits.
	_, _ = writer.Write(encoded[:])
}

// IntegrityReviewDeliveryDigest binds the inventory digest to the current
// delivery revision, including count-only overflow and settlement.
func IntegrityReviewDeliveryDigest(base string, browser BrowserSubmissionSettlement, deliveryRevision int64) (string, error) {
	if !validLowerSHA256(base) || browser.Validate() != nil || !securitySafeInt(deliveryRevision) {
		return "", ErrDeliveryInvalid
	}

	h := sha256.New()
	h.Write([]byte("proctor.integrity_review.delivery\x00"))
	h.Write([]byte(base))
	h.Write([]byte(browser.State))
	h.Write([]byte{0})
	for _, v := range []int64{
		browser.InventoryRevision,
		browser.SourceCount,
		browser.PendingSourceCount,
		browser.IncompleteSourceCount,
		deliveryRevision,
	} {
		var raw [8]byte
		binary.BigEndian.PutUint64(raw[:], uint64(v)) //nolint:gosec // G115: Validate and securitySafeInt bound every value to a non-negative safe integer.
		h.Write(raw[:])
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
