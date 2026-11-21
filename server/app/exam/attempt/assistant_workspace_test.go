// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAssistantBatchUsesTheEditorPathAndExactAssistantAuthor(t *testing.T) {
	f := newFixture(t)
	workspaceTargetFixture(f)
	checksum := strings.Repeat("a", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: checksum}
	access := validWorkspaceMutationAccess(f)
	editor, err := workspaceMutationSelector(f.inv, access)
	require.NoError(t, err)
	conversation, turn, call := model.NewAssistantConversationID(), model.NewAssistantTurnID(), model.NewAssistantCallID()
	result, err := f.service.ApplyAssistantWorkspaceBatch(context.Background(), invocation.Task("exam.attempt.assistant"), AssistantWorkspaceBatchCommand{
		Candidate: AssistantCandidate{Editor: editor}, ConversationID: conversation, TurnID: turn, CallID: call, Mutations: []WorkspaceBatchMutation{createFile("main.go", checksum, 4)}, Content: batchContent(f, "main"), IdempotencyKey: "assistant-call",
	})
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	author := model.NewAssistantWorkspaceAuthor(conversation, turn, call)
	require.Equal(t, author, f.workspace.batch().Access.WorkspaceAuthor())
	require.Equal(t, author, result.Changes[0].Author)
}
