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
	FirstCursor   int64  `json:"first_workspace_cursor"`
	Cursor        int64  `json:"workspace_cursor"`
	ChangedAt     string `json:"changed_at"`
}

type candidateScreenClipsRequestedData struct {
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
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
	ExamSittingID             string `json:"exam_sitting_id"`
	ExamAttemptID             string `json:"exam_attempt_id"`
	CandidateID               string `json:"candidate_user_id"`
	SubmissionID              string `json:"submission_id"`
	State                     string `json:"state"`
	Provenance                string `json:"provenance"`
	WorkspaceCursor           int64  `json:"workspace_cursor"`
	ManifestDigest            string `json:"manifest_digest"`
	AssistantTranscriptDigest string `json:"assistant_transcript_digest"`
	SubmittedAt               string `json:"submitted_at"`
}

type candidateExamAttemptSubmittedData struct {
	ExamSittingID             string `json:"exam_sitting_id"`
	ExamAttemptID             string `json:"exam_attempt_id"`
	SubmissionID              string `json:"submission_id"`
	State                     string `json:"state"`
	Provenance                string `json:"provenance"`
	WorkspaceCursor           int64  `json:"workspace_cursor"`
	ManifestDigest            string `json:"manifest_digest"`
	AssistantTranscriptDigest string `json:"assistant_transcript_digest"`
	SubmittedAt               string `json:"submitted_at"`
}

// NewExamAttemptSubmittedEvent tells sitting viewers that an attempt was
// submitted. Unlike the candidate event of the same wire name, its payload
// identifies the candidate. manifestDigest must be a lowercase hex SHA-256.
func NewExamAttemptSubmittedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	submissionID model.SubmissionID,
	workspaceCursor int64,
	manifestDigest string,
	assistantTranscriptDigest string,
	provenance model.ExamSubmissionProvenance,
	submittedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!submissionID.IsValid() ||
		workspaceCursor < 0 ||
		!validEventSHA256(manifestDigest) ||
		!model.IsValidSHA256Fingerprint(assistantTranscriptDigest) ||
		!provenance.IsValid() ||
		submittedAt.IsZero() {
		return Event{}, errors.New("manager Exam Attempt submitted event requires valid bounded metadata")
	}

	data, err := json.Marshal(managerExamAttemptSubmittedData{
		ExamSittingID:             sittingID.String(),
		ExamAttemptID:             attemptID.String(),
		CandidateID:               candidateID.String(),
		SubmissionID:              submissionID.String(),
		State:                     string(model.ExamAttemptSubmitted),
		Provenance:                string(provenance),
		WorkspaceCursor:           workspaceCursor,
		ManifestDigest:            manifestDigest,
		AssistantTranscriptDigest: assistantTranscriptDigest,
		SubmittedAt:               model.FormatRFC3339(submittedAt),
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

// NewCandidateExamAttemptSubmittedEvent is the candidate's own copy, authorized
// under the participation action rather than view, with no candidate identity
// in the payload.
func NewCandidateExamAttemptSubmittedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	submissionID model.SubmissionID,
	workspaceCursor int64,
	manifestDigest string,
	assistantTranscriptDigest string,
	provenance model.ExamSubmissionProvenance,
	submittedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!submissionID.IsValid() ||
		workspaceCursor < 0 ||
		!validEventSHA256(manifestDigest) ||
		!model.IsValidSHA256Fingerprint(assistantTranscriptDigest) ||
		!provenance.IsValid() ||
		submittedAt.IsZero() {
		return Event{}, errors.New("candidate Exam Attempt submitted event requires valid bounded metadata")
	}

	data, err := json.Marshal(candidateExamAttemptSubmittedData{
		ExamSittingID:             sittingID.String(),
		ExamAttemptID:             attemptID.String(),
		SubmissionID:              submissionID.String(),
		State:                     string(model.ExamAttemptSubmitted),
		Provenance:                string(provenance),
		WorkspaceCursor:           workspaceCursor,
		ManifestDigest:            manifestDigest,
		AssistantTranscriptDigest: assistantTranscriptDigest,
		SubmittedAt:               model.FormatRFC3339(submittedAt),
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

// NewCandidateExamAttemptFocusLossWarningEvent carries a fixed warning code and
// no counts, thresholds or evidence.
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

// NewCandidateScreenClipsRequestedEvent carries no requests: Desktop lists
// them, and only its live participation may see them.
func NewCandidateScreenClipsRequestedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() || !attemptID.IsValid() || !candidateID.IsValid() || changedAt.IsZero() {
		return Event{}, errors.New("candidate screen clip request hint requires valid bounded metadata")
	}

	data, err := json.Marshal(candidateScreenClipsRequestedData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, err
	}

	return Event{
		Name:     examAttemptScreenClipsRequestedMessage.name,
		UserID:   candidateID,
		Action:   model.ActionExamSittingParticipate,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

// NewExamAttemptIntegrityFlaggedEvent gives the policy, outcome and evidence
// episode counts, never the evidence itself.
func NewExamAttemptIntegrityFlaggedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	flagID model.IntegrityFlagID,
	kind model.IntegrityPolicyKind,
	outcome model.IntegrityOutcome,
	retainedEvidence int,
	evidenceOverflow int64,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		!flagID.IsValid() ||
		!kind.IsValid() ||
		retainedEvidence < 0 ||
		retainedEvidence > model.FocusLossMaximumEvidenceEpisodes ||
		evidenceOverflow < 0 ||
		(retainedEvidence == 0 && evidenceOverflow == 0) ||
		changedAt.IsZero() {
		return Event{}, errors.New("manager Exam Attempt integrity Flag event requires valid bounded metadata")
	}

	if !outcome.IsValid() {
		return Event{}, errors.New("manager Exam Attempt integrity Flag event requires a valid outcome")
	}

	data, err := json.Marshal(managerExamAttemptIntegrityFlaggedData{
		ExamSittingID:     sittingID.String(),
		ExamAttemptID:     attemptID.String(),
		CandidateID:       candidateID.String(),
		FlagID:            flagID.String(),
		PolicyKind:        string(kind),
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

// NewCandidateExamAttemptWorkspaceChangedEvent is one refetch hint per
// committed batch, naming only its cursors. Paths, metadata and bodies stay in
// the authorized HTTP responses.
func NewCandidateExamAttemptWorkspaceChangedEvent(
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	candidateID model.UserID,
	firstCursor, lastCursor int64,
	changedAt time.Time,
) (Event, error) {
	if !sittingID.IsValid() ||
		!attemptID.IsValid() ||
		!candidateID.IsValid() ||
		firstCursor < 1 ||
		lastCursor < firstCursor ||
		changedAt.IsZero() {
		return Event{}, errors.New("candidate Exam Attempt Workspace event requires valid bounded metadata")
	}

	data, err := json.Marshal(candidateExamAttemptWorkspaceChangedData{
		ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(),
		FirstCursor:   firstCursor,
		Cursor:        lastCursor,
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

// NewCandidateExamAttemptSuspendedEvent gives a policy reason for automatic
// enforcement and manager_suspended for a kick.
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

// NewCandidateExamAttemptReallowedEvent carries no reason code, unlike the
// suspension event.
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

// NewExamAttemptPolicySuspendedEvent requires both the integrity flag and the
// connection, because policy enforcement always links them.
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

// NewExamAttemptKickedEvent links no integrity flag, because a kick is a
// manager control, not suspicion. connectionID is zero when the candidate had
// no open connection.
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

// newExamAttemptSuspendedEvent omits attempt_connection_id and
// integrity_flag_id when absent.
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

// NewExamAttemptReallowedEvent names the closed suspension and the attempt's
// new revision, never the manager's private reason.
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

// NewExamAttemptConnectionOpenedEvent is for sitting viewers, not the
// candidate.
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

// NewExamAttemptConnectionClosedEvent is for sitting viewers, not the
// candidate; reason says why the connection closed.
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
