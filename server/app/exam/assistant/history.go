// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// history is the conversation so far as model messages: the candidate's
// messages and the assistant's replies, newest last. Earlier tool calls are
// left out; their results were for the turn that made them. The oldest
// messages go first when the history is over its bound.
func (r *turnRun) history() ([]ModelMessage, error) {
	editor := r.key.Editor
	owner := &store.AssistantTranscriptOwnerAccess{AttemptID: editor.AttemptID, CandidateUserID: editor.CandidateUserID,
		SessionID: editor.SessionID}
	var messages []ModelMessage
	var lastTurn model.AssistantTurnID
	after := int64(0)
	for {
		page, err := r.service.transcripts.ListForAttempt(r.ctx, store.AssistantTranscriptQuery{AttemptID: editor.AttemptID,
			AfterSequence: after, Limit: 200, Owner: owner})
		if err != nil {
			return nil, err
		}
		for _, record := range page.Records {
			if record.Turn == nil || record.Turn.Text == "" {
				continue
			}
			role := "assistant"
			if record.Turn.Role == "candidate" {
				role = "user"
			}
			if last := len(messages) - 1; last >= 0 && messages[last].Role == role && lastTurn == record.Turn.TurnID {
				messages[last].Text += record.Turn.Text
				continue
			}
			messages = append(messages, ModelMessage{Role: role, Text: record.Turn.Text})
			lastTurn = record.Turn.TurnID
		}
		if page.NextSequence == 0 {
			break
		}
		after = page.NextSequence
	}
	return trimHistory(messages, r.service.limits.HistoryBytes), nil
}

func trimHistory(messages []ModelMessage, limit int) []ModelMessage {
	total := 0
	for _, message := range messages {
		total += len(message.Text)
	}
	for len(messages) > 1 && total > limit {
		total -= len(messages[0].Text)
		messages = messages[1:]
	}
	for len(messages) > 1 && messages[0].Role != "user" {
		messages = messages[1:]
	}
	return messages
}
