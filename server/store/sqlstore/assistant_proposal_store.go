// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// A candidate applying a proposal is still the candidate's ordinary batch.
// This records a matching fingerprint; an unused proposal is never called declined.
func matchAssistantProposals(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID, mutations []store.ExamAttemptWorkspaceMutation) error {
	var proposals [][]byte
	if err := tx.Select(ctx, &proposals, `SELECT p.canonical FROM exam_attempt_assistant_calls p
 WHERE p.exam_attempt_id=? AND p.tool='propose_edit' AND p.phase='completed'
 AND NOT EXISTS(SELECT 1 FROM exam_attempt_assistant_calls applied WHERE applied.exam_attempt_id=p.exam_attempt_id AND
 applied.call_id=p.call_id AND applied.phase='applied')
 ORDER BY p.sequence DESC LIMIT 20000`, id.String()); err != nil {
		return err
	}
	if len(proposals) == 0 {
		return nil
	}
	var inventory *assistantInventoryRow
	matchedPaths := map[string]bool{}
	for _, raw := range proposals {
		record, err := model.DecodeAssistantRecord(raw)
		if err != nil {
			return err
		}
		if record.Call == nil || record.Call.Outcome != "proposed" {
			continue
		}
		var proposal struct {
			Path               string `json:"path"`
			SHA256             string `json:"sha256"`
			SizeBytes          int64  `json:"size_bytes"`
			BaseContentVersion string `json:"base_content_version"`
		}
		if json.Unmarshal([]byte(record.Call.Result), &proposal) != nil {
			continue
		}
		matched := false
		for _, mutation := range mutations {
			path := mutation.DestinationPath
			if mutation.Operation == model.AttemptWorkspaceMutationReplaceFile {
				path = mutation.ExpectedPath
			} else if mutation.Operation != model.AttemptWorkspaceMutationCreateFile {
				continue
			}
			if path != proposal.Path || mutation.ExpectedContentVersion.String() != proposal.BaseContentVersion {
				continue
			}
			var object struct {
				SHA256    string `db:"sha256"`
				SizeBytes int64  `db:"size_bytes"`
			}
			if err = tx.Get(ctx, &object, `SELECT sha256,size_bytes FROM exam_attempt_workspace_objects WHERE id=?`, mutation.ObjectID.String()); err != nil {
				return err
			}
			if object.SHA256 == proposal.SHA256 && object.SizeBytes == proposal.SizeBytes {
				matched = true
				break
			}
		}
		if matchedPaths[proposal.Path] || !matched {
			continue
		}
		if inventory == nil {
			value, lockErr := lockAssistantInventory(ctx, tx, id)
			if lockErr != nil {
				return lockErr
			}
			inventory = &value
		}
		// Assistant history capacity must never prevent the candidate's write.
		if inventory.Records >= model.AssistantTranscriptMaximumRecords-32 || inventory.Bytes+4096 > model.AssistantTranscriptMaximumBytes-(64<<10) {
			break
		}
		call := *record.Call
		call.Phase = "applied"
		call.Outcome = "applied"
		call.Arguments = json.RawMessage(`{}`)
		call.Approval = ""
		call.Result = "Matched candidate batch at transcript record " + strconv.FormatInt(inventory.Records+1, 10)
		appended, appendErr := appendAssistantRecord(ctx, tx, id, model.AssistantRecord{Call: &call}, false)
		if appendErr != nil {
			return appendErr
		}
		canonical, encodeErr := appended.Canonical()
		if encodeErr != nil {
			return encodeErr
		}
		inventory.Records++
		inventory.Bytes += int64(len(canonical))
		// Attribute each path to its latest matching proposal only.
		matchedPaths[proposal.Path] = true
	}
	return nil
}
