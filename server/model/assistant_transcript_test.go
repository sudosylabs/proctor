// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAssistantInventoryChainsOrderedCanonicalRecordsIndependently(t *testing.T) {
	t.Parallel()
	inventory := NewAssistantInventory()
	require.NoError(t, inventory.Validate())
	record := AssistantRecord{Sequence: 1, CreatedAt: TimeUTC(time.Now()), Turn: &AssistantTurn{ConversationID: NewAssistantConversationID(), TurnID: NewAssistantTurnID(), Role: "candidate", Text: "A hint, please.", Final: true}}
	first, err := inventory.Append(record)
	require.NoError(t, err)
	encoded, err := record.Canonical()
	require.NoError(t, err)
	require.Equal(t, int64(len(encoded)), first.Bytes)
	require.NotEqual(t, inventory.Digest, first.Digest)
	repeat, err := inventory.Append(record)
	require.NoError(t, err)
	require.Equal(t, first, repeat)
	changed := *record.Turn
	changed.Text = "A different question"
	record.Turn = &changed
	altered, err := inventory.Append(record)
	require.NoError(t, err)
	require.NotEqual(t, first.Digest, altered.Digest)
	_, err = first.Append(record)
	require.Error(t, err)
	record.Sequence = 2
	record.Gap = true
	next, err := first.Append(record)
	require.NoError(t, err)
	require.Equal(t, int64(1), next.Gaps)
	full := next
	full.Records = AssistantTranscriptMaximumRecords
	require.False(t, full.Admits(1))
	full = next
	full.Bytes = AssistantTranscriptMaximumBytes
	require.False(t, full.Admits(1))
	require.NotEqual(t, NewAttemptEvidenceInventory().Digest, inventory.Digest)
}
func TestAssistantTranscriptRejectsUnboundedOrInvalidRecords(t *testing.T) {
	t.Parallel()
	turn := AssistantTurn{ConversationID: NewAssistantConversationID(), TurnID: NewAssistantTurnID(), Role: "candidate", Text: "Question", Final: true}
	require.True(t, turn.Valid())
	turn.Text = strings.Repeat("x", AssistantCandidateMessageMaximumBytes+1)
	require.False(t, turn.Valid())
	turn.Role = "assistant"
	turn.Text = ""
	turn.Model = "campus"
	turn.StopReason = "unavailable"
	turn.Usage = AssistantUsage{Output: 100, Estimated: true}
	require.True(t, turn.Valid())
	turn.Final = false
	require.False(t, turn.Valid())
	call := AssistantCall{ConversationID: turn.ConversationID, TurnID: turn.TurnID, CallID: NewAssistantCallID(), Tool: "run_command", Phase: "requested", Arguments: json.RawMessage(`{"command":"go test ./..."}`), Approval: "pending", ProcessTag: "assistant:1"}
	require.True(t, call.Valid())
	call.ProcessTag = "candidate:1"
	require.False(t, call.Valid())
	call.ProcessTag = "assistant:01"
	require.False(t, call.Valid())
	call.ProcessTag = "assistant:2"
	call.Arguments = json.RawMessage(`null`)
	require.False(t, call.Valid())
}
