// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"encoding/json"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// prepareWorkspaceBatchIdempotency fingerprints a batch by what the writer
// asked for, in order. Server-chosen identities and the author's reference are
// left out, so an exact retry matches even through a new connection. The
// author kind is fingerprinted so one kind's batch never replays as another's
// under the same candidate-scoped key.
func prepareWorkspaceBatchIdempotency(
	candidateID model.UserID,
	authorKind model.AttemptWorkspaceAuthorKind,
	key string,
	attemptID model.ExamAttemptID,
	mutations []WorkspaceBatchMutation,
	expectedWorkspaceCursor *int64,
) (*store.CommandIdempotency, error) {
	type semanticMutation struct {
		Operation              string `json:"operation"`
		EntryID                string `json:"entry_id,omitempty"`
		ExpectedPath           string `json:"expected_path,omitempty"`
		DestinationPath        string `json:"destination_path,omitempty"`
		ExpectedContentVersion string `json:"expected_content_version,omitempty"`
		Recursive              bool   `json:"recursive,omitempty"`
		MediaType              string `json:"media_type,omitempty"`
		SHA256                 string `json:"sha256,omitempty"`
		Size                   int64  `json:"size,omitempty"`
	}
	semantic := make([]semanticMutation, len(mutations))
	for index, mutation := range mutations {
		semantic[index] = semanticMutation{
			Operation:              string(mutation.Operation),
			EntryID:                mutation.EntryID.String(),
			ExpectedPath:           mutation.ExpectedPath,
			DestinationPath:        mutation.DestinationPath,
			ExpectedContentVersion: mutation.ExpectedContentVersion.String(),
			Recursive:              mutation.Recursive,
			MediaType:              mutation.MediaType,
			SHA256:                 mutation.SHA256,
			Size:                   mutation.Size,
		}
	}

	return idempotency.RequireFor(candidateID, store.ExamAttemptWorkspaceBatchOperation, key, struct {
		AttemptID               string             `json:"exam_attempt_id"`
		Mutations               []semanticMutation `json:"mutations"`
		ExpectedWorkspaceCursor *int64             `json:"expected_workspace_cursor,omitempty"`
		AuthorKind              string             `json:"author_kind"`
	}{attemptID.String(), semantic, expectedWorkspaceCursor, string(authorKind)})
}

func prepareConnectIdempotency(inv invocation.Invocation, command ConnectCommand) (*store.CommandIdempotency, error) {
	principal := inv.Principal()
	var proposal json.RawMessage
	if command.InitialConfiguration != nil {
		canonical, err := command.InitialConfiguration.CanonicalAdmission()
		if err != nil {
			return nil, apperror.New("request.invalid").Wrap(err)
		}

		proposal = canonical
	}

	return idempotency.Require(inv, store.ExamAttemptConnectOperation, command.IdempotencyKey, struct {
		SittingID                string                `json:"exam_sitting_id"`
		SessionID                string                `json:"session_id"`
		ContinuityCredentialHash string                `json:"continuity_credential_hash"`
		ConfigurationManifest    string                `json:"configuration_manifest_fingerprint"`
		InitialConfiguration     json.RawMessage       `json:"initial_configuration"`
		Security                 model.ConnectSecurity `json:"security"`
	}{
		command.SittingID.String(),
		principal.SessionID.String(),
		model.HashToken(command.ContinuityCredential),
		command.ConfigurationManifestFingerprint,
		proposal,
		command.Security,
	})
}

func prepareReallowIdempotency(inv invocation.Invocation, command ReallowCommand) (*store.CommandIdempotency, error) {
	return idempotency.Require(
		inv,
		store.ExamAttemptReallowOperation,
		command.IdempotencyKey,
		struct {
			ExamID                  string `json:"exam_id"`
			SittingID               string `json:"exam_sitting_id"`
			AttemptID               string `json:"exam_attempt_id"`
			SuspensionID            string `json:"suspension_id"`
			ExpectedAttemptRevision int64  `json:"expected_attempt_revision"`
			PrivateReason           string `json:"private_reason"`
		}{
			command.ExamID.String(),
			command.SittingID.String(),
			command.AttemptID.String(),
			command.SuspensionID.String(),
			command.ExpectedAttemptRevision,
			command.PrivateReason,
		},
	)
}

func prepareManualSuspensionIdempotency(
	inv invocation.Invocation,
	command ManualSuspensionCommand,
) (*store.CommandIdempotency, error) {
	return idempotency.Require(inv, store.ExamAttemptSuspendOperation, command.IdempotencyKey, struct {
		ExamID                  string `json:"exam_id"`
		SittingID               string `json:"exam_sitting_id"`
		AttemptID               string `json:"exam_attempt_id"`
		ExpectedAttemptRevision int64  `json:"expected_attempt_revision"`
		PrivateReason           string `json:"private_reason"`
	}{
		command.ExamID.String(),
		command.SittingID.String(),
		command.AttemptID.String(),
		command.ExpectedAttemptRevision,
		command.PrivateReason,
	})
}

func prepareManagerEndIdempotency(inv invocation.Invocation, command ManagerEndCommand) (*store.CommandIdempotency, error) {
	return idempotency.Require(inv, store.ExamSubmissionManagerEndOperation, command.IdempotencyKey, struct {
		ExamID                  string `json:"exam_id"`
		SittingID               string `json:"exam_sitting_id"`
		AttemptID               string `json:"exam_attempt_id"`
		ExpectedAttemptRevision int64  `json:"expected_attempt_revision"`
		PrivateReason           string `json:"private_reason"`
	}{
		command.ExamID.String(),
		command.SittingID.String(),
		command.AttemptID.String(),
		command.ExpectedAttemptRevision,
		command.PrivateReason,
	})
}

func prepareSubmissionIdempotency(
	inv invocation.Invocation,
	key string,
	attemptID model.ExamAttemptID,
	expectedCurrentRevisionID model.ExamRevisionID,
	expectedWorkspaceCursor, finalFocusLossSequence int64,
) (*store.CommandIdempotency, error) {
	return idempotency.Require(inv, store.ExamSubmissionSealOperation, key, struct {
		AttemptID                 string `json:"exam_attempt_id"`
		ExpectedCurrentRevisionID string `json:"expected_current_revision_id"`
		WorkspaceCursor           int64  `json:"expected_workspace_cursor"`
		FinalFocusLossSequence    int64  `json:"final_focus_loss_sequence"`
	}{attemptID.String(), expectedCurrentRevisionID.String(), expectedWorkspaceCursor, finalFocusLossSequence})
}
