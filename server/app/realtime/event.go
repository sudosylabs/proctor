// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// Event is a transport-neutral, past-tense application fact published
// after durable commit. Sequence is never assigned here: each owning connection
// stamps sequence at the WebSocket boundary.
//
// JSON field names match the existing cluster publication payload so multi-node
// peers remain wire-compatible while ownership of wire DTOs moves outward.
// Cluster fan-out is always best-effort; there is no durable delivery class.
type Event struct {
	ID       string
	Name     string
	UserID   model.UserID
	Action   model.Action
	Resource model.Resource
	Data     json.RawMessage
}

const (
	maxRealtimeEventNameBytes = 128
	maxRealtimeEventDataBytes = 256 << 10
)

type userSettingsChangedData struct {
	Revision      string `json:"revision"`
	FormatVersion int    `json:"format_version"`
	ChangedAt     string `json:"changed_at"`
}

type collectionInvalidationData struct {
	SchemaVersion int `json:"schema_version"`
}

type sittingBoardInvalidationData struct {
	SchemaVersion int    `json:"schema_version"`
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
}

// NewCandidateExamActivityChangedEvent constructs the payload-minimal
// collection invalidation sent only to the affected candidate. The public
// payload deliberately carries no selector because the delivery envelope
// already owns the exact User target.
func NewCandidateExamActivityChangedEvent(candidateID model.UserID) (Event, error) {
	if !candidateID.IsValid() {
		return Event{}, errors.New("candidate Exam activity invalidation requires a valid User identity")
	}
	data, err := json.Marshal(collectionInvalidationData{SchemaVersion: 1})
	if err != nil {
		return Event{}, fmt.Errorf("encode candidate Exam activity invalidation: %w", err)
	}
	return Event{Name: candidateExamActivityChangedMessage.name, UserID: candidateID, Data: data}, nil
}

// NewCurrentUserContextChangedEvent constructs a content-free refetch signal
// for the affected User's bounded navigation projection.
func NewCurrentUserContextChangedEvent(userID model.UserID) (Event, error) {
	if !userID.IsValid() {
		return Event{}, errors.New("current User context invalidation requires a valid User identity")
	}
	data, err := json.Marshal(collectionInvalidationData{SchemaVersion: 1})
	if err != nil {
		return Event{}, fmt.Errorf("encode current User context invalidation: %w", err)
	}
	return Event{Name: currentUserContextChangedMessage.name, UserID: userID, Data: data}, nil
}

// NewCurrentUserSessionsChangedEvent tells the User's connections to refetch
// their own Session list: one of their Sessions ended, was revoked or was
// renamed. It names no Session; the connections of an ended Session close.
func NewCurrentUserSessionsChangedEvent(userID model.UserID) (Event, error) {
	if !userID.IsValid() {
		return Event{}, errors.New("current User Sessions invalidation requires a valid User identity")
	}
	data, err := json.Marshal(collectionInvalidationData{SchemaVersion: 1})
	if err != nil {
		return Event{}, fmt.Errorf("encode current User Sessions invalidation: %w", err)
	}
	return Event{Name: currentUserSessionsChangedMessage.name, UserID: userID, Data: data}, nil
}

// NewManagerSittingBoardChangedEvent constructs a selector-only invalidation
// for currently authorized Exam Sitting viewers. Candidate and Attempt
// identities never enter its public payload.
func NewManagerSittingBoardChangedEvent(examID model.ExamID, sittingID model.ExamSittingID) (Event, error) {
	if !examID.IsValid() || !sittingID.IsValid() {
		return Event{}, errors.New("manager Sitting board invalidation requires valid Exam and Sitting identities")
	}

	data, err := json.Marshal(sittingBoardInvalidationData{
		SchemaVersion: 1,
		ExamID:        examID.String(),
		ExamSittingID: sittingID.String(),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode manager Sitting board invalidation: %w", err)
	}

	return Event{
		Name:     managerSittingBoardChangedMessage.name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewUserSettingsChangedEvent constructs the content-free refetch hint sent to
// the owning User's live sessions after a durable settings replacement. The
// source document and all client/session details deliberately remain absent.
func NewUserSettingsChangedEvent(
	userID model.UserID,
	revision model.UserSettingsRevision,
	formatVersion int,
	changedAt time.Time,
) (Event, error) {
	if !userID.IsValid() || !revision.IsValid() || formatVersion <= 0 || changedAt.IsZero() {
		return Event{}, errors.New("User Settings change event requires valid bounded metadata")
	}

	data, err := json.Marshal(userSettingsChangedData{
		Revision:      revision.String(),
		FormatVersion: formatVersion,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode User Settings change event: %w", err)
	}

	return Event{
		Name:   userSettingsChangedMessage.name,
		UserID: userID,
		Data:   data,
	}, nil
}

type examDraftUpdatedData struct {
	ExamID        string `json:"exam_id"`
	DraftRevision int64  `json:"draft_revision"`
}

type examStarterWorkspaceChangedData struct {
	ExamID        string `json:"exam_id"`
	EntryID       string `json:"entry_id"`
	Operation     string `json:"operation"`
	DraftRevision int64  `json:"draft_revision"`
	ChangedAt     string `json:"changed_at"`
}

type examArchivedData struct {
	ExamID       string `json:"exam_id"`
	ExamRevision int64  `json:"exam_revision"`
	ArchivedAt   string `json:"archived_at"`
}

type examManagerChangedData struct {
	ExamID       string `json:"exam_id"`
	UserID       string `json:"user_id"`
	Present      bool   `json:"present"`
	ExamRevision int64  `json:"exam_revision"`
	ChangedAt    string `json:"changed_at"`
}

type examOwnerTransferredData struct {
	ExamID       string `json:"exam_id"`
	OwnerUserID  string `json:"owner_user_id"`
	ExamRevision int64  `json:"exam_revision"`
	ChangedAt    string `json:"changed_at"`
}

type examRevisionPublishedData struct {
	ExamID       string `json:"exam_id"`
	RevisionID   string `json:"revision_id"`
	Number       int64  `json:"number"`
	PolicyDigest string `json:"policy_digest"`
	Kind         string `json:"kind"`
	PublishedAt  string `json:"published_at"`
}

type examSittingChangedData struct {
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	State         string `json:"state"`
	Revision      int64  `json:"revision"`
	ChangedAt     string `json:"changed_at"`
}

type examSittingLifecycleChangedData struct {
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	State         string `json:"state"`
	Revision      int64  `json:"revision"`
	ReasonCode    string `json:"reason_code"`
	ScheduledEnd  string `json:"scheduled_end_at"`
	ChangedAt     string `json:"changed_at"`
}

type examSittingContentCorrectedData struct {
	ExamID             string `json:"exam_id"`
	ExamSittingID      string `json:"exam_sitting_id"`
	PreviousRevisionID string `json:"previous_revision_id"`
	RevisionID         string `json:"revision_id"`
	SittingRevision    int64  `json:"sitting_revision"`
	EffectiveAt        string `json:"effective_at"`
}

type examAttemptConnectionChangedData struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	CandidateID   string `json:"candidate_user_id"`
	ConnectionID  string `json:"attempt_connection_id"`
	State         string `json:"state"`
	CloseReason   string `json:"close_reason,omitempty"`
	ChangedAt     string `json:"changed_at"`
}

type candidateExamAttemptAccessChangedData struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	State         string `json:"state"`
	ReasonCode    string `json:"reason_code,omitempty"`
	ChangedAt     string `json:"changed_at"`
}

type managerExamAttemptSuspendedData struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	CandidateID   string `json:"candidate_user_id"`
	ConnectionID  string `json:"attempt_connection_id,omitempty"`
	FlagID        string `json:"integrity_flag_id,omitempty"`
	SuspensionID  string `json:"suspension_id"`
	Revision      int64  `json:"attempt_revision"`
	ChangedAt     string `json:"changed_at"`
}

type managerExamAttemptReallowedData struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	CandidateID   string `json:"candidate_user_id"`
	SuspensionID  string `json:"suspension_id"`
	Revision      int64  `json:"attempt_revision"`
	ChangedAt     string `json:"changed_at"`
}

type candidateExamAttemptWorkspaceChangedData struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	EntryID       string `json:"entry_id"`
	Operation     string `json:"operation"`
	Cursor        int64  `json:"workspace_cursor"`
	ChangedAt     string `json:"changed_at"`
}

type candidateExamAttemptFocusLossWarningData struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	WarningCode   string `json:"warning_code"`
	ChangedAt     string `json:"changed_at"`
}

type managerExamAttemptIntegrityFlaggedData struct {
	ExamSittingID     string `json:"exam_sitting_id"`
	ExamAttemptID     string `json:"exam_attempt_id"`
	CandidateID       string `json:"candidate_user_id"`
	FlagID            string `json:"integrity_flag_id"`
	PolicyKind        string `json:"policy_kind"`
	Outcome           string `json:"outcome"`
	RetainedEvidence  int    `json:"retained_evidence_count"`
	EvidenceOverflow  int64  `json:"evidence_overflow_count"`
	EvidenceAvailable bool   `json:"evidence_available"`
	ChangedAt         string `json:"changed_at"`
}

type managerExamAttemptSubmittedData struct {
	ExamSittingID   string `json:"exam_sitting_id"`
	ExamAttemptID   string `json:"exam_attempt_id"`
	CandidateID     string `json:"candidate_user_id"`
	SubmissionID    string `json:"submission_id"`
	State           string `json:"state"`
	Provenance      string `json:"provenance"`
	WorkspaceCursor int64  `json:"workspace_cursor"`
	ManifestDigest  string `json:"manifest_digest"`
	SubmittedAt     string `json:"submitted_at"`
}

type candidateExamAttemptSubmittedData struct {
	ExamSittingID   string `json:"exam_sitting_id"`
	ExamAttemptID   string `json:"exam_attempt_id"`
	SubmissionID    string `json:"submission_id"`
	State           string `json:"state"`
	Provenance      string `json:"provenance"`
	WorkspaceCursor int64  `json:"workspace_cursor"`
	ManifestDigest  string `json:"manifest_digest"`
	SubmittedAt     string `json:"submitted_at"`
}

type managerExamIntegrityReviewChangedData struct {
	SubmissionID  string `json:"submission_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	CandidateID   string `json:"candidate_user_id"`
	ReviewID      string `json:"submission_review_id"`
	ReviewState   string `json:"review_state"`
	Revision      int64  `json:"review_revision"`
	ReleaseState  string `json:"release_state"`
	ChangedAt     string `json:"changed_at"`
}

type managerExamIntegrityDiscrepancyData struct {
	SubmissionID  string `json:"submission_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	CandidateID   string `json:"candidate_user_id"`
	DiscrepancyID string `json:"integrity_discrepancy_id"`
	RecordedAt    string `json:"recorded_at"`
}

// NewExamIntegrityDiscrepancyRecordedEvent tells viewers of a Submission that
// an Integrity Discrepancy was recorded against it: a Focus Loss claim that
// arrived after the Attempt or Sitting had ended. The payload carries
// identities and the receipt time only, not the claim's duration or source.
func NewExamIntegrityDiscrepancyRecordedEvent(
	submissionID model.SubmissionID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	discrepancyID model.IntegrityDiscrepancyID,
	recordedAt time.Time,
) (Event, error) {
	if !submissionID.IsValid() ||
		!sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!discrepancyID.IsValid() ||
		recordedAt.IsZero() {
		return Event{}, errors.New("manager Integrity Discrepancy event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamIntegrityDiscrepancyData{
		SubmissionID:  submissionID.String(),
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		CandidateID:   candidateID.String(),
		DiscrepancyID: discrepancyID.String(),
		RecordedAt:    model.FormatRFC3339(recordedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode manager Integrity Discrepancy event: %w", err)
	}

	return Event{
		Name:     examIntegrityDiscrepancyRecordedMessage.name,
		Action:   model.ActionSubmissionView,
		Resource: model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()},
		Data:     data,
	}, nil
}

type candidateStudentResultReleasedData struct {
	SubmissionID  string `json:"submission_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	ReviewID      string `json:"submission_review_id"`
	ReviewState   string `json:"review_state"`
	Revision      int64  `json:"review_revision"`
	ReleaseState  string `json:"release_state"`
	ChangedAt     string `json:"changed_at"`
}

// ExamIntegrityReviewEventFact is the complete bounded lifecycle fact shared
// by manager Review events and the candidate release notification. It excludes
// decisions, rationale, evidence, private notes, and student remarks.
type ExamIntegrityReviewEventFact struct {
	SubmissionID model.SubmissionID
	AttemptID    model.ExamAttemptID
	CandidateID  model.UserID
	ReviewID     model.SubmissionReviewID
	State        model.SubmissionReviewState
	Revision     int64
	ReleaseState model.SubmissionReviewReleaseState
	ChangedAt    time.Time
}

// NewExamIntegrityReviewChangedEvent tells those who may view the Submission
// that its Integrity Review changed, in any state.
func NewExamIntegrityReviewChangedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	return newManagerExamIntegrityReviewEvent(examIntegrityReviewChangedMessage.name, fact)
}

// NewExamIntegrityReviewFinalizedEvent tells those who may view the
// Submission that its Integrity Review was finalized. It refuses a Review
// that is not finalized and still withheld.
func NewExamIntegrityReviewFinalizedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	if fact.State != model.SubmissionReviewFinalized || fact.ReleaseState != model.SubmissionReviewWithheld {
		return Event{}, errors.New("finalized Integrity Review event requires a withheld finalized Review")
	}
	return newManagerExamIntegrityReviewEvent(examIntegrityReviewFinalizedMessage.name, fact)
}

// NewExamStudentResultReleasedEvent tells those who may view the Submission
// that its Student Result was released. It refuses a Review that is not
// finalized and released.
func NewExamStudentResultReleasedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	if fact.State != model.SubmissionReviewFinalized || fact.ReleaseState != model.SubmissionReviewReleased {
		return Event{}, errors.New("released Student Result event requires a released finalized Review")
	}
	return newManagerExamIntegrityReviewEvent(examStudentResultReleasedMessage.name, fact)
}

// NewCandidateStudentResultReleasedEvent tells the candidate alone that
// their Student Result was released. It carries the Review's identity and
// state, never its content.
func NewCandidateStudentResultReleasedEvent(fact ExamIntegrityReviewEventFact) (Event, error) {
	if !fact.SubmissionID.IsValid() ||
		!fact.AttemptID.IsValid() ||
		!fact.CandidateID.IsValid() ||
		!fact.ReviewID.IsValid() ||
		fact.State != model.SubmissionReviewFinalized ||
		fact.ReleaseState != model.SubmissionReviewReleased ||
		fact.Revision < 1 ||
		fact.ChangedAt.IsZero() {
		return Event{}, errors.New("candidate Student Result event requires valid released Review metadata")
	}

	data, err := json.Marshal(candidateStudentResultReleasedData{
		SubmissionID:  fact.SubmissionID.String(),
		ExamAttemptID: fact.AttemptID.String(),
		ReviewID:      fact.ReviewID.String(),
		ReviewState:   string(fact.State),
		Revision:      fact.Revision,
		ReleaseState:  string(fact.ReleaseState),
		ChangedAt:     model.FormatRFC3339(fact.ChangedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode candidate Student Result event: %w", err)
	}

	return Event{Name: candidateExamStudentResultReleasedMessage.name, UserID: fact.CandidateID, Data: data}, nil
}

func newManagerExamIntegrityReviewEvent(name string, fact ExamIntegrityReviewEventFact) (Event, error) {
	if !fact.SubmissionID.IsValid() ||
		!fact.AttemptID.IsValid() ||
		!fact.CandidateID.IsValid() ||
		!fact.ReviewID.IsValid() ||
		fact.Revision < 1 ||
		fact.ChangedAt.IsZero() ||
		(fact.State != model.SubmissionReviewDraft && fact.State != model.SubmissionReviewFinalized) ||
		(fact.ReleaseState != model.SubmissionReviewWithheld && fact.ReleaseState != model.SubmissionReviewReleased) {
		return Event{}, errors.New("manager Integrity Review event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamIntegrityReviewChangedData{
		SubmissionID:  fact.SubmissionID.String(),
		ExamAttemptID: fact.AttemptID.String(),
		CandidateID:   fact.CandidateID.String(),
		ReviewID:      fact.ReviewID.String(),
		ReviewState:   string(fact.State),
		Revision:      fact.Revision,
		ReleaseState:  string(fact.ReleaseState),
		ChangedAt:     model.FormatRFC3339(fact.ChangedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode manager Integrity Review event: %w", err)
	}

	return Event{
		Name:     name,
		Action:   model.ActionSubmissionView,
		Resource: model.Resource{Type: model.ResourceSubmission, ID: fact.SubmissionID.String()},
		Data:     data,
	}, nil
}

// NewExamAttemptSubmittedEvent tells Sitting viewers that an Attempt was
// submitted; provenance says whether the candidate submitted, a manager ended
// the Attempt, or the Sitting closed. Unlike the candidate event of the same
// wire name, its payload identifies the candidate. manifestDigest must be a
// lowercase hex SHA-256.
func NewExamAttemptSubmittedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	submissionID model.SubmissionID,
	workspaceCursor int64,
	manifestDigest string,
	provenance model.ExamSubmissionProvenance,
	submittedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!submissionID.IsValid() ||
		workspaceCursor < 0 ||
		!validEventSHA256(manifestDigest) ||
		!provenance.IsValid() ||
		submittedAt.IsZero() {
		return Event{}, errors.New("manager Exam Attempt submitted event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamAttemptSubmittedData{
		ExamSittingID:   sittingID.String(),
		ExamAttemptID:   attemptID.String(),
		CandidateID:     candidateID.String(),
		SubmissionID:    submissionID.String(),
		State:           string(model.ExamAttemptSubmitted),
		Provenance:      string(provenance),
		WorkspaceCursor: workspaceCursor,
		ManifestDigest:  manifestDigest,
		SubmittedAt:     model.FormatRFC3339(submittedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     examAttemptSubmittedMessage.name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewCandidateExamAttemptSubmittedEvent is the candidate's own copy of the
// submission fact. It is addressed to candidateID alone, is authorized under
// the participation action rather than the view action, and leaves the
// candidate identity out of the payload.
func NewCandidateExamAttemptSubmittedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	submissionID model.SubmissionID,
	workspaceCursor int64,
	manifestDigest string,
	provenance model.ExamSubmissionProvenance,
	submittedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!submissionID.IsValid() ||
		workspaceCursor < 0 ||
		!validEventSHA256(manifestDigest) ||
		!provenance.IsValid() ||
		submittedAt.IsZero() {
		return Event{}, errors.New("candidate Exam Attempt submitted event requires valid bounded metadata")
	}

	data, err := json.Marshal(candidateExamAttemptSubmittedData{
		ExamSittingID:   sittingID.String(),
		ExamAttemptID:   attemptID.String(),
		SubmissionID:    submissionID.String(),
		State:           string(model.ExamAttemptSubmitted),
		Provenance:      string(provenance),
		WorkspaceCursor: workspaceCursor,
		ManifestDigest:  manifestDigest,
		SubmittedAt:     model.FormatRFC3339(submittedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     candidateExamAttemptSubmittedMessage.name,
		UserID:   candidateID,
		Action:   model.ActionExamSittingParticipate,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

func validEventSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

// NewCandidateExamAttemptFocusLossWarningEvent warns the candidate alone that
// their Focus Loss crossed the policy threshold under a flag-and-warn
// outcome. The payload carries a fixed warning code and no counts,
// thresholds, or evidence.
func NewCandidateExamAttemptFocusLossWarningEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() || !attemptID.IsValid() || !candidateID.IsValid() || changedAt.IsZero() {
		return Event{}, errors.New("candidate Exam Attempt Focus Loss warning requires valid bounded metadata")
	}

	data, err := json.Marshal(candidateExamAttemptFocusLossWarningData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		WarningCode:   "focus_loss_policy_warning",
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     examAttemptFocusWarningMessage.name,
		UserID:   candidateID,
		Action:   model.ActionExamSittingParticipate,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamAttemptIntegrityFlaggedEvent tells Sitting viewers that a Focus Loss
// Integrity Flag was raised on an Attempt. The payload gives the policy
// outcome and how many evidence episodes were retained and how many
// overflowed, not the evidence itself. Focus Loss is the only policy kind it
// reports.
func NewExamAttemptIntegrityFlaggedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	flagID model.IntegrityFlagID,
	outcome model.IntegrityOutcome,
	retainedEvidence int,
	evidenceOverflow int64,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!flagID.IsValid() ||
		retainedEvidence < 0 ||
		retainedEvidence > model.FocusLossMaximumEvidenceEpisodes ||
		evidenceOverflow < 0 ||
		(retainedEvidence == 0 && evidenceOverflow == 0) ||
		changedAt.IsZero() {
		return Event{}, errors.New("manager Exam Attempt integrity Flag event requires valid bounded metadata")
	}

	switch outcome {
	case model.IntegrityOutcomeFlag, model.IntegrityOutcomeFlagAndWarn, model.IntegrityOutcomeFlagAndSuspend:
	default:
		return Event{}, errors.New("manager Exam Attempt integrity Flag event requires a valid outcome")
	}

	data, err := json.Marshal(managerExamAttemptIntegrityFlaggedData{
		ExamSittingID:     sittingID.String(),
		ExamAttemptID:     attemptID.String(),
		CandidateID:       candidateID.String(),
		FlagID:            flagID.String(),
		PolicyKind:        string(model.IntegrityPolicyFocusLoss),
		Outcome:           string(outcome),
		RetainedEvidence:  retainedEvidence,
		EvidenceOverflow:  evidenceOverflow,
		EvidenceAvailable: true,
		ChangedAt:         model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     examAttemptIntegrityFlaggedMessage.name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewCandidateExamAttemptWorkspaceChangedEvent publishes only a targeted
// refetch hint. Logical paths, content metadata, object selectors, continuity
// selectors, and mutation bodies remain confined to protected HTTP responses.
func NewCandidateExamAttemptWorkspaceChangedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	entryID model.AttemptWorkspaceEntryID,
	operation model.AttemptWorkspaceMutationKind,
	cursor int64,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!entryID.IsValid() ||
		!operation.IsValid() ||
		cursor < 1 ||
		changedAt.IsZero() {
		return Event{}, errors.New("candidate Exam Attempt Workspace event requires valid bounded metadata")
	}

	data, err := json.Marshal(candidateExamAttemptWorkspaceChangedData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		EntryID:       entryID.String(),
		Operation:     string(operation),
		Cursor:        cursor,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     examAttemptWorkspaceChangedMessage.name,
		UserID:   candidateID,
		Action:   model.ActionExamSittingParticipate,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewCandidateExamAttemptSuspendedEvent tells the candidate their Attempt is
// suspended with the safe reason that belongs to the episode's source: a
// policy reason for automatic enforcement, manager_suspended for a kick.
func NewCandidateExamAttemptSuspendedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	source model.AttemptSuspensionSource,
	reason model.AttemptSuspensionCandidateReason,
	changedAt time.Time,
) (Event, error) {
	switch {
	case source == model.AttemptSuspensionSourcePolicy && reason.IsPolicy():
	case source == model.AttemptSuspensionSourceManual && reason == model.AttemptSuspensionCandidateReasonManagerSuspended:
	default:
		return Event{}, errors.New("candidate Exam Attempt suspension event requires the safe reason of its source")
	}

	return newCandidateExamAttemptAccessEvent(
		examAttemptAccessSuspendedMessage.name,
		sittingID,
		attemptID,
		candidateID,
		model.ExamAttemptSuspended,
		string(reason),
		changedAt,
	)
}

// NewCandidateExamAttemptReallowedEvent tells the candidate alone that their
// suspended Attempt was re-allowed and is active again. Unlike the suspension
// event it carries no reason code.
func NewCandidateExamAttemptReallowedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	changedAt time.Time,
) (Event, error) {
	return newCandidateExamAttemptAccessEvent(
		examAttemptAccessReallowedMessage.name,
		sittingID,
		attemptID,
		candidateID,
		model.ExamAttemptActive,
		"",
		changedAt,
	)
}

func newCandidateExamAttemptAccessEvent(
	name string,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	state model.ExamAttemptState,
	reason string,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		changedAt.IsZero() ||
		(state != model.ExamAttemptSuspended && state != model.ExamAttemptActive) {
		return Event{}, errors.New("candidate Exam Attempt access event requires valid bounded metadata")
	}

	data, err := json.Marshal(candidateExamAttemptAccessChangedData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		State:         string(state),
		ReasonCode:    reason,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     name,
		UserID:   candidateID,
		Action:   model.ActionExamSittingParticipate,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamAttemptPolicySuspendedEvent announces an automatic policy
// suspension (Connection Loss or Focus Loss) to Sitting viewers. Policy
// enforcement always links its Integrity Flag and the Connection the
// enforced generation held, so both are required.
func NewExamAttemptPolicySuspendedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	connectionID model.AttemptConnectionID,
	flagID model.IntegrityFlagID,
	suspensionID model.AttemptSuspensionID,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !connectionID.IsValid() || !flagID.IsValid() {
		return Event{}, errors.New("policy Exam Attempt suspension event requires its Connection and Integrity Flag")
	}
	return newExamAttemptSuspendedEvent(sittingID, attemptID, candidateID, connectionID, flagID, suspensionID, revision, changedAt)
}

// NewExamAttemptKickedEvent announces an Exam Manager's kick to Sitting
// viewers. A kick is a manager control, not suspicion, so it links no
// Integrity Flag; connectionID is zero when the kicked generation had no
// open Connection (a reconnecting candidate).
func NewExamAttemptKickedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	connectionID model.AttemptConnectionID,
	suspensionID model.AttemptSuspensionID,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !connectionID.IsValid() && !connectionID.IsZero() {
		return Event{}, errors.New("manual Exam Attempt suspension event requires a valid Connection when present")
	}
	return newExamAttemptSuspendedEvent(sittingID, attemptID, candidateID, connectionID, "", suspensionID, revision, changedAt)
}

// newExamAttemptSuspendedEvent builds the exam_attempt_suspended payload;
// attempt_connection_id and integrity_flag_id are omitted when absent, as the
// source-specific constructors allow.
func newExamAttemptSuspendedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	connectionID model.AttemptConnectionID,
	flagID model.IntegrityFlagID,
	suspensionID model.AttemptSuspensionID,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!suspensionID.IsValid() ||
		revision < 1 ||
		changedAt.IsZero() {
		return Event{}, errors.New("manager Exam Attempt suspension event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamAttemptSuspendedData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		CandidateID:   candidateID.String(),
		ConnectionID:  connectionID.String(),
		FlagID:        flagID.String(),
		SuspensionID:  suspensionID.String(),
		Revision:      revision,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     examAttemptSuspendedMessage.name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamAttemptReallowedEvent tells Sitting viewers that an Attempt
// Suspension was closed and the Attempt returned to active. It names the
// closed suspension and the Attempt's revision after the change; the
// manager's private reason is not included.
func NewExamAttemptReallowedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	suspensionID model.AttemptSuspensionID,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!suspensionID.IsValid() ||
		revision < 1 ||
		changedAt.IsZero() {
		return Event{}, errors.New("manager Exam Attempt re-allow event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamAttemptReallowedData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		CandidateID:   candidateID.String(),
		SuspensionID:  suspensionID.String(),
		Revision:      revision,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     examAttemptReallowedMessage.name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamAttemptConnectionOpenedEvent tells Sitting viewers that a candidate's
// desktop was admitted to the Sitting and now holds an open Attempt
// Connection. It is addressed to viewers of the Sitting, not to the candidate.
func NewExamAttemptConnectionOpenedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	connectionID model.AttemptConnectionID,
	openedAt time.Time,
) (Event, error) {
	return newExamAttemptConnectionChangedEvent(
		examAttemptConnectionOpenedMessage.name,
		sittingID,
		attemptID,
		candidateID,
		connectionID,
		model.AttemptConnectionOpen,
		"",
		openedAt,
	)
}

// NewExamAttemptConnectionClosedEvent tells Sitting viewers that an Attempt
// Connection closed and why: reason separates, for example, a dropped
// transport or expired lease from a policy suspension, a kick, a submission,
// or the Sitting closing. It is addressed to viewers of the Sitting, not to
// the candidate.
func NewExamAttemptConnectionClosedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	connectionID model.AttemptConnectionID,
	reason model.AttemptConnectionCloseReason,
	closedAt time.Time,
) (Event, error) {
	if !reason.IsValid() {
		return Event{}, errors.New("Exam Attempt Connection closed event requires a valid reason")
	}
	return newExamAttemptConnectionChangedEvent(
		examAttemptConnectionClosedMessage.name,
		sittingID,
		attemptID,
		candidateID,
		connectionID,
		model.AttemptConnectionClosed,
		reason,
		closedAt,
	)
}

func newExamAttemptConnectionChangedEvent(
	name string,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	connectionID model.AttemptConnectionID,
	state model.AttemptConnectionState,
	reason model.AttemptConnectionCloseReason,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!connectionID.IsValid() ||
		changedAt.IsZero() ||
		(state != model.AttemptConnectionOpen && state != model.AttemptConnectionClosed) {
		return Event{}, errors.New("Exam Attempt Connection event requires valid bounded metadata")
	}

	data, err := json.Marshal(examAttemptConnectionChangedData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		CandidateID:   candidateID.String(),
		ConnectionID:  connectionID.String(),
		State:         string(state),
		CloseReason:   string(reason),
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Attempt Connection event: %w", err)
	}

	return Event{
		Name:     name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamSittingContentCorrectedEvent constructs the content-free fact that
// tells authorized Sitting subscribers to refetch authoritative presentation.
func NewExamSittingContentCorrectedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	previousRevisionID,
	revisionID model.ExamRevisionID,
	sittingRevision int64,
	effectiveAt time.Time,
) (Event, error) {
	return newExamSittingContentCorrectedEvent(
		examSittingContentCorrectedMessage,
		model.ActionExamSittingView,
		examID,
		sittingID,
		previousRevisionID,
		revisionID,
		sittingRevision,
		effectiveAt,
	)
}

// NewCandidateExamSittingContentCorrectedEvent constructs the independently
// authorized candidate fact for the same committed correction. Candidate and
// manager subscriptions deliberately use different actions so later manager
// payloads cannot become candidate-visible by accident.
func NewCandidateExamSittingContentCorrectedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	previousRevisionID,
	revisionID model.ExamRevisionID,
	sittingRevision int64,
	effectiveAt time.Time,
) (Event, error) {
	return newExamSittingContentCorrectedEvent(
		candidateExamSittingCorrectionMessage,
		model.ActionExamSittingParticipate,
		examID,
		sittingID,
		previousRevisionID,
		revisionID,
		sittingRevision,
		effectiveAt,
	)
}

func newExamSittingContentCorrectedEvent(
	declared message,
	action model.Action,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	previousRevisionID,
	revisionID model.ExamRevisionID,
	sittingRevision int64,
	effectiveAt time.Time,
) (Event, error) {
	if !examID.IsValid() ||
		!sittingID.IsValid() ||
		!previousRevisionID.IsValid() ||
		!revisionID.IsValid() ||
		previousRevisionID == revisionID ||
		sittingRevision < 1 ||
		effectiveAt.IsZero() ||
		(action != model.ActionExamSittingView && action != model.ActionExamSittingParticipate) {
		return Event{}, errors.New("Exam Sitting correction event requires valid bounded metadata")
	}

	data, err := json.Marshal(examSittingContentCorrectedData{
		ExamID:             examID.String(),
		ExamSittingID:      sittingID.String(),
		PreviousRevisionID: previousRevisionID.String(),
		RevisionID:         revisionID.String(),
		SittingRevision:    sittingRevision,
		EffectiveAt:        model.FormatRFC3339(effectiveAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Sitting correction event: %w", err)
	}

	return Event{
		Name:     declared.name,
		Action:   action,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamSittingLifecycleChangedEvent constructs the safe authoritative fact
// emitted after one committed lifecycle transition or deadline extension.
func NewExamSittingLifecycleChangedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	reasonCode string,
	scheduledEndAt,
	changedAt time.Time,
) (Event, error) {
	return newExamSittingLifecycleChangedEvent(
		examSittingLifecycleChangedMessage,
		model.ActionExamSittingView,
		examID,
		sittingID,
		state,
		revision,
		reasonCode,
		scheduledEndAt,
		changedAt,
	)
}

// NewCandidateExamSittingLifecycleChangedEvent constructs the candidate-safe
// lifecycle fact under the relationship-only participation action.
func NewCandidateExamSittingLifecycleChangedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	reasonCode string,
	scheduledEndAt,
	changedAt time.Time,
) (Event, error) {
	return newExamSittingLifecycleChangedEvent(
		candidateExamSittingLifecycleMessage,
		model.ActionExamSittingParticipate,
		examID,
		sittingID,
		state,
		revision,
		reasonCode,
		scheduledEndAt,
		changedAt,
	)
}

func newExamSittingLifecycleChangedEvent(
	declared message,
	action model.Action,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	reasonCode string,
	scheduledEndAt,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() ||
		!sittingID.IsValid() ||
		!state.IsValid() ||
		revision < 1 ||
		!validExamSittingLifecycleEventReason(reasonCode) ||
		scheduledEndAt.IsZero() ||
		changedAt.IsZero() ||
		(action != model.ActionExamSittingView && action != model.ActionExamSittingParticipate) {
		return Event{}, errors.New("Exam Sitting lifecycle event requires valid bounded metadata")
	}

	data, err := json.Marshal(examSittingLifecycleChangedData{
		ExamID:        examID.String(),
		ExamSittingID: sittingID.String(),
		State:         string(state),
		Revision:      revision,
		ReasonCode:    reasonCode,
		ScheduledEnd:  model.FormatRFC3339(scheduledEndAt),
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Sitting lifecycle event: %w", err)
	}

	return Event{
		Name:     declared.name,
		Action:   action,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

func validExamSittingLifecycleEventReason(value string) bool {
	switch value {
	case "scheduled_start_reached",
		"manager_paused",
		"manager_resumed",
		"manager_extended",
		"manager_closed",
		"scheduled_end_reached",
		"schedule_elapsed",
		"academic_structure_invalid",
		"closed_no_attempts",
		"sealing_completed":
		return true
	default:
		return false
	}
}

// NewExamSittingScheduledEvent tells subscribers holding the view action on a
// Sitting that it was scheduled. The payload carries the Sitting's state and
// revision but nothing of the schedule itself: no times, Class, or Exam
// Revision. state must be Scheduled.
func NewExamSittingScheduledEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if state != model.ExamSittingScheduled {
		return Event{}, errors.New("scheduled Exam Sitting event requires Scheduled state")
	}
	return newExamSittingChangedEvent(examSittingScheduledMessage.name, examID, sittingID, state, revision, changedAt)
}

// NewExamSittingScheduleUpdatedEvent tells Sitting viewers that a Scheduled
// Sitting's Exam Revision, Class, start, or end was changed. The payload
// carries the new Sitting revision but not which of those changed or their
// new values.
func NewExamSittingScheduleUpdatedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if state != model.ExamSittingScheduled {
		return Event{}, errors.New("Exam Sitting schedule update event requires Scheduled state")
	}
	return newExamSittingChangedEvent(examSittingScheduleUpdatedMessage.name, examID, sittingID, state, revision, changedAt)
}

// NewExamSittingCanceledEvent tells Sitting viewers that a Sitting was
// canceled before it opened. state must be Canceled, and the payload carries
// no cancellation reason.
func NewExamSittingCanceledEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if state != model.ExamSittingCanceled {
		return Event{}, errors.New("canceled Exam Sitting event requires Canceled state")
	}
	return newExamSittingChangedEvent(examSittingCanceledMessage.name, examID, sittingID, state, revision, changedAt)
}

func newExamSittingChangedEvent(
	name string,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() || !sittingID.IsValid() || !state.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("Exam Sitting event requires valid bounded metadata")
	}

	data, err := json.Marshal(examSittingChangedData{
		ExamID:        examID.String(),
		ExamSittingID: sittingID.String(),
		State:         string(state),
		Revision:      revision,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Sitting event: %w", err)
	}

	return Event{
		Name:     name,
		Action:   model.ActionExamSittingView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamRevisionPublishedEvent tells viewers of an Exam that a new Exam
// Revision was published. The payload carries the Revision's number,
// publication kind, and policy digest, and none of the published content.
// policyDigest must be 64 characters long.
func NewExamRevisionPublishedEvent(
	revisionID model.ExamRevisionID,
	examID model.ExamID,
	number int64,
	policyDigest string,
	kind model.ExamRevisionPublicationKind,
	publishedAt time.Time,
) (Event, error) {
	if !revisionID.IsValid() || !examID.IsValid() || number < 1 || len(policyDigest) != 64 || !kind.IsValid() || publishedAt.IsZero() {
		return Event{}, errors.New("Exam Revision published event requires valid bounded metadata")
	}

	data, err := json.Marshal(examRevisionPublishedData{
		ExamID:       examID.String(),
		RevisionID:   revisionID.String(),
		Number:       number,
		PolicyDigest: policyDigest,
		Kind:         string(kind),
		PublishedAt:  model.FormatRFC3339(publishedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Revision published event: %w", err)
	}

	return Event{
		Name:     examRevisionPublishedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamCreatedEvent constructs the stable, content-free authoring event.
func NewExamCreatedEvent(examID model.ExamID) (Event, error) {
	if !examID.IsValid() {
		return Event{}, errors.New("exam created event requires a valid Exam ID")
	}
	return Event{
		Name:     examCreatedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
	}, nil
}

// NewExamDraftUpdatedEvent owns the bounded wire projection for a Draft text
// or policy change. Authored content and policy values are deliberately absent.
func NewExamDraftUpdatedEvent(examID model.ExamID, revision int64) (Event, error) {
	if !examID.IsValid() || revision < 1 {
		return Event{}, errors.New("exam Draft update event requires a valid identity and revision")
	}

	data, err := json.Marshal(examDraftUpdatedData{ExamID: examID.String(), DraftRevision: revision})
	if err != nil {
		return Event{}, fmt.Errorf("encode exam Draft update event: %w", err)
	}

	return Event{
		Name:     examDraftUpdatedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamStarterWorkspaceChangedEvent constructs the content-free fact emitted
// after one Starter Workspace hierarchy/content mutation commits. Logical
// paths, opaque object identities, checksums, and authored bytes are absent.
func NewExamStarterWorkspaceChangedEvent(
	examID model.ExamID,
	entryID model.StarterWorkspaceEntryID,
	revision int64,
	operation string,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() || !entryID.IsValid() || revision < 1 || !validExamStarterWorkspaceOperation(operation) || changedAt.IsZero() {
		return Event{}, errors.New("Starter Workspace change event requires valid identities, operation, revision, and time")
	}

	data, err := json.Marshal(examStarterWorkspaceChangedData{
		ExamID:        examID.String(),
		EntryID:       entryID.String(),
		Operation:     operation,
		DraftRevision: revision,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Starter Workspace change event: %w", err)
	}

	return Event{
		Name:     examStarterWorkspaceChangedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

func validExamStarterWorkspaceOperation(operation string) bool {
	switch operation {
	case "directory_created", "file_created", "entry_moved", "file_replaced", "entry_removed":
		return true
	default:
		return false
	}
}

// NewExamArchivedEvent constructs the content-free Exam lifecycle event.
func NewExamArchivedEvent(examID model.ExamID, revision int64, archivedAt time.Time) (Event, error) {
	if !examID.IsValid() || revision < 1 || archivedAt.IsZero() {
		return Event{}, errors.New("exam archived event requires a valid identity, revision, and time")
	}

	data, err := json.Marshal(examArchivedData{
		ExamID:       examID.String(),
		ExamRevision: revision,
		ArchivedAt:   model.FormatRFC3339(archivedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode exam archived event: %w", err)
	}

	return Event{
		Name:     examArchivedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamManagerChangedEvent tells viewers of an Exam that userID was added
// as an Exam Manager (present true) or removed (present false). revision is
// the Exam's revision after the change. An ownership transfer is announced by
// NewExamOwnerTransferredEvent instead.
func NewExamManagerChangedEvent(
	examID model.ExamID,
	userID model.UserID,
	present bool,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() || !userID.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("Exam Manager change event requires valid identities, revision, and time")
	}

	data, err := json.Marshal(examManagerChangedData{
		ExamID:       examID.String(),
		UserID:       userID.String(),
		Present:      present,
		ExamRevision: revision,
		ChangedAt:    model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Manager change event: %w", err)
	}

	return Event{
		Name:     examManagerChangedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamOwnerTransferredEvent tells those who may view the Exam that it has
// a new owner, at the Exam revision the transfer produced.
func NewExamOwnerTransferredEvent(examID model.ExamID, ownerID model.UserID, revision int64, changedAt time.Time) (Event, error) {
	if !examID.IsValid() || !ownerID.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("Exam owner transfer event requires valid identities, revision, and time")
	}

	data, err := json.Marshal(examOwnerTransferredData{
		ExamID:       examID.String(),
		OwnerUserID:  ownerID.String(),
		ExamRevision: revision,
		ChangedAt:    model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam owner transfer event: %w", err)
	}

	return Event{
		Name:     examOwnerTransferredMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewAcademicUnitCreatedEvent tells everyone who may view the Academic Unit
// that it was created. Like its update and archive siblings it carries no
// payload: subscribers refetch the Academic Unit.
func NewAcademicUnitCreatedEvent(unitID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitCreatedMessage, unitID)
}

// NewAcademicUnitUpdatedEvent tells everyone who may view the Academic Unit
// that it changed.
func NewAcademicUnitUpdatedEvent(unitID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitUpdatedMessage, unitID)
}

// NewAcademicUnitArchivedEvent tells everyone who may view the Academic Unit
// that it was archived.
func NewAcademicUnitArchivedEvent(unitID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitArchivedMessage, unitID)
}

func newAcademicUnitEvent(declared message, unitID model.AcademicUnitID) (Event, error) {
	if !unitID.IsValid() {
		return Event{}, errors.New("Academic Unit event requires a valid Academic Unit identity")
	}
	return Event{
		Name:     declared.name,
		Action:   model.ActionAcademicUnitView,
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()},
	}, nil
}

type userProfilePictureChangedData struct {
	ActiveFileEntryID string `json:"active_file_entry_id"`
	Revision          int64  `json:"revision"`
	ChangedAt         string `json:"changed_at"`
}

// NewUserProfilePictureChangedEvent tells the User's live sessions which File
// Entry is now their profile picture, with the User revision and the instant
// of the change.
func NewUserProfilePictureChangedEvent(
	userID model.UserID,
	activeFileEntryID model.FileEntryID,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !userID.IsValid() || !activeFileEntryID.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("User profile picture event requires valid bounded metadata")
	}

	data, err := json.Marshal(userProfilePictureChangedData{
		ActiveFileEntryID: activeFileEntryID.String(),
		Revision:          revision,
		ChangedAt:         model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode User profile picture event: %w", err)
	}

	return Event{Name: userProfilePictureChangedMessage.name, UserID: userID, Data: data}, nil
}

// Clone returns a deep copy safe for concurrent local and cluster delivery.
func (e Event) Clone() Event {
	cloned := e
	if e.Data != nil {
		cloned.Data = append(json.RawMessage(nil), e.Data...)
	}
	return cloned
}

// ValidateForPublish checks local publication invariants. It performs no I/O.
func (e Event) ValidateForPublish() error {
	if e.ID != "" && !model.IsValidOpaqueID(e.ID) {
		return errors.New("realtime event ID is invalid")
	}

	if len(e.Name) == 0 ||
		len(e.Name) > maxRealtimeEventNameBytes ||
		!validRealtimeName(e.Name) {
		return fmt.Errorf("invalid realtime event %q", e.Name)
	}

	if e.UserID != "" && !e.UserID.IsValid() {
		return errors.New("realtime event user ID is invalid")
	}

	if e.Action == "" && e.Resource == (model.Resource{}) {
		if e.UserID == "" {
			return errors.New("realtime event requires a user target or authorized resource")
		}
	} else {
		// An event may address an owning scope the action accepts, as an
		// onboarding import's Academic Unit or Class.
		definition, ok := model.DefinitionForAction(e.Action)
		if !ok || e.Resource.Validate() != nil || !definition.AcceptsResource(e.Resource.Type) {
			return errors.New("realtime event authorization target is invalid")
		}
	}

	if len(e.Data) > maxRealtimeEventDataBytes {
		return fmt.Errorf("realtime event data exceeds %d bytes", maxRealtimeEventDataBytes)
	}
	if len(e.Data) != 0 && !json.Valid(e.Data) {
		return errors.New("realtime event data is not valid JSON")
	}

	return nil
}

// ConnectionCloseReason is a transport-neutral reason the local connection
// boundary maps to protocol close codes and text.
type ConnectionCloseReason string

// SessionRevoked closes the connections of a revoked Session or user;
// AuthorizationChanged closes connections whose subscriptions may no longer be
// authorized.
const (
	ConnectionCloseSessionRevoked       ConnectionCloseReason = "session_revoked"
	ConnectionCloseAuthorizationChanged ConnectionCloseReason = "authorization_changed"
)

func validRealtimeName(value string) bool {
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' &&
			character != '_' &&
			character != '-' &&
			character != ':' {
			return false
		}
	}

	return true
}
