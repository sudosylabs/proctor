// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func assertPreparedIdempotency(t *testing.T, got *store.CommandIdempotency, userID model.UserID, operation, key, document string) {
	t.Helper()
	wantKey := sha256.Sum256([]byte(key))
	wantFingerprint := sha256.Sum256([]byte(operation + "\x00" + document))
	require.NotNil(t, got, "prepared idempotency")
	require.Equal(t, userID, got.UserID, "prepared idempotency")
	require.Equal(t, operation, got.Operation, "prepared idempotency")
	require.Equal(t, wantKey, got.KeyDigest, "prepared idempotency")
	require.Equal(t, wantFingerprint, got.Fingerprint, "prepared idempotency")
	require.Equal(t, 24*time.Hour, got.Retention, "prepared idempotency")
	require.Equal(t, 2*time.Second, got.Wait, "prepared idempotency")
}

func TestWorkspaceBatchFingerprintCompatibility(t *testing.T) {
	t.Parallel()
	userID, attemptID := model.NewUserID(), model.NewExamAttemptID()
	entryID := model.NewAttemptWorkspaceEntryID()
	version := model.WorkspaceContentVersion("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	digest := strings.Repeat("a", 64)
	inv := invocation.New(model.Principal{UserID: userID}, model.RequestMetadata{})
	cursor := int64(7)
	tests := []struct {
		name     string
		mutation WorkspaceBatchMutation
		cursor   *int64
		document string
	}{
		{
			name:     "create directory",
			mutation: WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory, DestinationPath: "src"},
			document: `{"operation":"create_directory","destination_path":"src"}`,
		},
		{
			name: "create file",
			mutation: WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: "main.go",
				MediaType: "application/octet-stream", SHA256: digest, Size: 12},
			document: fmt.Sprintf(`{"operation":"create_file","destination_path":"main.go","media_type":"application/octet-stream","sha256":%q,"size":12}`, digest),
		},
		{
			name: "replace file",
			mutation: WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: entryID, ExpectedPath: "main.go",
				ExpectedContentVersion: version, MediaType: "application/octet-stream", SHA256: digest, Size: 12},
			document: fmt.Sprintf(`{"operation":"replace_file","entry_id":%q,"expected_path":"main.go","expected_content_version":%q,"media_type":"application/octet-stream","sha256":%q,"size":12}`,
				entryID, version, digest),
		},
		{
			name:     "move",
			mutation: WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: entryID, ExpectedPath: "old", DestinationPath: "new"},
			document: fmt.Sprintf(`{"operation":"move_entry","entry_id":%q,"expected_path":"old","destination_path":"new"}`, entryID),
		},
		{
			name:     "delete",
			mutation: WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: entryID, ExpectedPath: "old", ExpectedContentVersion: version},
			document: fmt.Sprintf(`{"operation":"delete_entry","entry_id":%q,"expected_path":"old","expected_content_version":%q}`, entryID, version),
		},
		{
			name:     "recursive delete",
			mutation: WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: entryID, ExpectedPath: "old", Recursive: true},
			cursor:   &cursor,
			document: fmt.Sprintf(`{"operation":"delete_entry","entry_id":%q,"expected_path":"old","recursive":true}`, entryID),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			prepared, err := prepareWorkspaceBatchIdempotency(inv, "execution-key", attemptID, []WorkspaceBatchMutation{test.mutation}, test.cursor)
			require.NoError(t, err)
			outer := fmt.Sprintf(`{"exam_attempt_id":%q,"mutations":[%s]}`, attemptID, test.document)
			if test.cursor != nil {
				outer = fmt.Sprintf(`{"exam_attempt_id":%q,"mutations":[%s],"expected_workspace_cursor":%d}`, attemptID, test.document, *test.cursor)
			}
			assertPreparedIdempotency(t, prepared, userID, store.ExamAttemptWorkspaceBatchOperation,
				"execution-key", outer)
		})
	}
}

func TestWorkspaceBatchFingerprintFollowsOrderAndPaths(t *testing.T) {
	t.Parallel()
	inv := invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{})
	attemptID := model.NewExamAttemptID()
	directory := WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory, DestinationPath: "src"}
	file := WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: "src/main.go",
		MediaType: "text/plain", SHA256: strings.Repeat("a", 64), Size: 4}
	fingerprint := func(mutations ...WorkspaceBatchMutation) [32]byte {
		t.Helper()
		prepared, err := prepareWorkspaceBatchIdempotency(inv, "batch-key", attemptID, mutations, nil)
		require.NoError(t, err)
		return prepared.Fingerprint
	}

	original := fingerprint(directory, file)
	require.Equal(t, original, fingerprint(directory, file), "an exact retry must match")
	require.NotEqual(t, original, fingerprint(file, directory), "order is part of the batch")
	renamed := file
	renamed.DestinationPath = "src/other.go"
	require.NotEqual(t, original, fingerprint(directory, renamed), "paths are part of the batch")
	require.NotEqual(t, original, fingerprint(directory), "every mutation is part of the batch")
}

func TestCommandIdempotencyDocumentsAndStoreBoundaryCompatibility(t *testing.T) {
	t.Parallel()
	userID, sessionID := model.NewUserID(), model.NewSessionID()
	inv := invocation.New(model.Principal{UserID: userID, SessionID: sessionID}, model.RequestMetadata{})
	sittingID, attemptID := model.NewExamSittingID(), model.NewExamAttemptID()
	credential := model.NewCredentialToken()

	manifest := model.CurrentAttemptConfigurationManifestFingerprint()
	security := model.ConnectSecurity{Kind: "preflight", PreflightID: "fixture-preflight", ReportDigest: model.SHA256Fingerprint([]byte("fixture-report"))}
	connected, err := prepareConnectIdempotency(inv, ConnectCommand{Security: security, SittingID: sittingID,
		ContinuityCredential: credential, ConfigurationManifestFingerprint: manifest, IdempotencyKey: "connect-key"})
	require.NoError(t, err)
	assertPreparedIdempotency(t, connected, userID, store.ExamAttemptConnectOperation, "connect-key",
		fmt.Sprintf(`{"exam_sitting_id":%q,"session_id":%q,"continuity_credential_hash":%q,"configuration_manifest_fingerprint":%q,"initial_configuration":null,"security":{"kind":"preflight","preflight_id":"fixture-preflight","report_digest":%q}}`,
			sittingID, sessionID, model.HashToken(credential), manifest, security.ReportDigest))

	examID, suspensionID := model.NewExamID(), model.NewAttemptSuspensionID()
	reallowed, err := prepareReallowIdempotency(inv, ReallowCommand{ExamID: examID, SittingID: sittingID,
		AttemptID: attemptID, SuspensionID: suspensionID, ExpectedAttemptRevision: 3,
		PrivateReason: "reviewed", IdempotencyKey: "reallow-key"})
	require.NoError(t, err)
	assertPreparedIdempotency(t, reallowed, userID, store.ExamAttemptReallowOperation, "reallow-key",
		fmt.Sprintf(`{"exam_id":%q,"exam_sitting_id":%q,"exam_attempt_id":%q,"suspension_id":%q,"expected_attempt_revision":3,"private_reason":"reviewed"}`,
			examID, sittingID, attemptID, suspensionID))

	revisionID := model.NewExamRevisionID()
	submitted, err := prepareSubmissionIdempotency(inv, "submit-key", attemptID, revisionID, 11, 7)
	require.NoError(t, err)
	assertPreparedIdempotency(t, submitted, userID, store.ExamSubmissionSealOperation, "submit-key",
		fmt.Sprintf(`{"exam_attempt_id":%q,"expected_current_revision_id":%q,"expected_workspace_cursor":11,"final_focus_loss_sequence":7}`,
			attemptID, revisionID))
}

func TestIdempotencyOperationCompatibility(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		store.ExamAttemptConnectOperation:        "exam.attempt.connect",
		store.ExamAttemptReallowOperation:        "exam.attempt.reallow",
		store.ExamAttemptWorkspaceBatchOperation: "exam.attempt.workspace.batch",
		store.ExamSubmissionSealOperation:        "exam.attempt.submit",
	}
	require.Len(t, tests, 4)
	for operation, want := range tests {
		assert.Equal(t, want, operation, "operation")
	}
}

func TestPrepareIdempotencyFaultMapping(t *testing.T) {
	t.Parallel()
	valid := invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{})
	_, err := idempotency.Require(valid, "operation", "", struct{}{})
	require.Equal(t, "idempotency.key_required", faultCode(err), "missing key error")
	invalid := invocation.New(model.Principal{}, model.RequestMetadata{})
	_, err = idempotency.Require(invalid, "operation", "key", struct{}{})
	require.Equal(t, "idempotency.key_invalid", faultCode(err), "invalid principal error")
	_, err = idempotency.Require(valid, "operation", "key", make(chan struct{}))
	require.Equal(t, "request.invalid", faultCode(err), "encoding error")
}

func faultCode(err error) string {
	if fault, ok := apperror.As(err); ok {
		return fault.Code()
	}
	return ""
}
