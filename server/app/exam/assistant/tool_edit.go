// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// assistantTask names the assistant's writes in the audit. The actor is the
// candidate, on whose behalf it writes.
const assistantTask = "exam.attempt.assistant"

type editInput struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Explanation string `json:"explanation"`
}

func parseEdit(arguments json.RawMessage) (editInput, string, bool) {
	var input editInput
	if json.Unmarshal(arguments, &input) != nil || !utf8.ValidString(input.Content) || len(input.Explanation) > 4<<10 {
		return input, "", false
	}
	path, err := model.NormalizeAttemptWorkspacePath(input.Path)
	if err != nil || path != input.Path {
		return input, "", false
	}
	return input, path, true
}

type preparedEdit struct {
	mutation attempt.WorkspaceBatchMutation
	content  string
	oldFile  *store.CandidateAttemptWorkspaceItem
}

func (r *turnRun) prepareEdit(arguments json.RawMessage) (*preparedEdit, error) {
	input, path, ok := parseEdit(arguments)
	if !ok {
		return nil, errors.New("the path must be a workspace path and the content valid text")
	}
	items, cut, err := r.manifest()
	if err != nil {
		return nil, err
	}
	existing, found := findFile(items, path)
	if !found && cut {
		return nil, errors.New("the workspace listing is incomplete")
	}
	digest := sha256.Sum256([]byte(input.Content))
	plan := &preparedEdit{content: input.Content,
		mutation: attempt.WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: path,
			MediaType: "text/plain", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(input.Content))}}
	if found {
		if existing.Kind != model.StarterWorkspaceEntryFile {
			return nil, errors.New("that path is a directory")
		}
		plan.oldFile = &existing
		plan.mutation = attempt.WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: existing.EntryID,
			ExpectedPath: path, ExpectedContentVersion: existing.ContentVersion, MediaType: existing.MediaType, SHA256: plan.mutation.SHA256, Size: plan.mutation.Size}
	}
	return plan, nil
}

// proposeEdit writes nothing. Its complete replacement diff and fingerprints
// let the client review the proposal and the store recognize an applied batch.
func (r *turnRun) proposeEdit(arguments json.RawMessage) toolResult {
	plan, err := r.prepareEdit(arguments)
	if err != nil {
		return toolError("The edit cannot be prepared from the current workspace.")
	}
	path := plan.mutation.DestinationPath
	old := ""
	baseVersion := ""
	if plan.oldFile != nil {
		path = plan.oldFile.Path
		baseVersion = plan.oldFile.ContentVersion.String()
		var cut bool
		old, cut, err = r.readText(*plan.oldFile, fileReadMaximumBytes)
		if err != nil || cut {
			return toolError("The existing file cannot be read completely as text for this proposal.")
		}
	}
	document, _ := json.Marshal(struct {
		Path               string `json:"path"`
		BaseContentVersion string `json:"base_content_version"`
		SHA256             string `json:"sha256"`
		SizeBytes          int64  `json:"size_bytes"`
		Diff               string `json:"diff"`
	}{path, baseVersion, plan.mutation.SHA256, plan.mutation.Size, replacementDiff(path, old, plan.content)})
	if len(document) > 384<<10 {
		return toolError("The complete proposal exceeds the transcript limit. Propose a smaller edit.")
	}
	return toolResult{forModel: "The proposal is shown to the candidate, who decides whether to apply it.", record: string(document), outcome: "proposed"}
}

// A diff names both removed and added lines, so approval can show the change
// against the file version the write will require.
func (r *turnRun) editApprovalSummary(plan *preparedEdit) (string, error) {
	path, old := plan.mutation.DestinationPath, ""
	if plan.oldFile != nil {
		path = plan.oldFile.Path
		var cut bool
		var err error
		old, cut, err = r.readText(*plan.oldFile, fileReadMaximumBytes)
		if err != nil || cut {
			return "", errors.New("the existing file cannot be shown completely")
		}
	}
	return replacementDiff(path, old, plan.content), nil
}

func replacementDiff(path, old, next string) string {
	oldLines := strings.SplitAfter(old, "\n")
	newLines := strings.SplitAfter(next, "\n")
	if old == "" {
		oldLines = nil
	} else if oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	if next == "" {
		newLines = nil
	} else if newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}
	var diff strings.Builder
	diff.WriteString("--- a/" + path + "\n+++ b/" + path + "\n")
	oldStart, newStart := 1, 1
	if len(oldLines) == 0 {
		oldStart = 0
	}
	if len(newLines) == 0 {
		newStart = 0
	}
	diff.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, len(oldLines), newStart, len(newLines)))
	for _, group := range []struct {
		prefix string
		lines  []string
	}{{"-", oldLines}, {"+", newLines}} {
		for _, line := range group.lines {
			if line == "" {
				continue
			}
			diff.WriteString(group.prefix)
			diff.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				diff.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return diff.String()
}

// The expected path and version are captured before approval, so accepting an
// edit never silently overwrites work the candidate changed while deciding.
func (r *turnRun) applyEdit(call model.AssistantCallID, plan *preparedEdit) toolResult {
	if plan == nil {
		return toolError("The edit could not be prepared.")
	}
	result, err := r.service.attempts.ApplyAssistantWorkspaceBatch(r.ctx, invocation.Task(assistantTask), attempt.AssistantWorkspaceBatchCommand{
		Candidate: r.candidate, ConversationID: r.conversation, TurnID: r.key.TurnID, CallID: call,
		Mutations: []attempt.WorkspaceBatchMutation{plan.mutation}, Content: &singleContent{body: plan.content}, IdempotencyKey: "assistant-" + call.String(),
	})
	if err != nil {
		return toolError("The edit was refused: the file changed, or the workspace cannot take it now.")
	}
	cursor := int64(0)
	if len(result.Changes) > 0 {
		cursor = result.Changes[len(result.Changes)-1].Cursor
	}
	return toolResult{forModel: "Written.", record: "workspace cursor " + strconv.FormatInt(cursor, 10), outcome: "applied"}
}

// singleContent is the body of a one-file batch.
type singleContent struct {
	body string
	used bool
}

func (c *singleContent) Next(context.Context) (io.Reader, error) {
	if c.used {
		return nil, io.EOF
	}
	c.used = true
	return strings.NewReader(c.body), nil
}
