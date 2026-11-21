// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestCommandEvidenceFitsCanonicalLimitAndPreservesExit(t *testing.T) {
	result := strings.Repeat("é\x1b", model.AssistantCallResultMaximumBytes/3)
	exitCode := 7
	record := model.AssistantRecord{Sequence: 6, CreatedAt: model.NowUTC(), Call: &model.AssistantCall{
		ConversationID: model.NewAssistantConversationID(), TurnID: model.NewAssistantTurnID(), CallID: model.NewAssistantCallID(),
		Tool: "run_command", Phase: "completed", Outcome: "error", ProcessTag: "assistant:2", ExitCode: &exitCode,
		Arguments: json.RawMessage("{}"), Result: result,
	}}
	_, err := record.Canonical()
	require.Error(t, err, "JSON escaping can exceed the canonical limit even when the text fits")
	for _, limit := range []int{model.AssistantRecordMaximumBytes, 803} {
		truncated := truncateAssistantCommandRecord(record, limit)
		canonical, encodeErr := truncated.Canonical()
		require.NoError(t, encodeErr)
		require.LessOrEqual(t, len(canonical), limit)
		require.Greater(t, len(canonical), limit-6, "the next character would exceed the limit")
		require.True(t, truncated.Gap)
		require.True(t, truncated.Call.Truncated)
		require.Equal(t, record.Call.ProcessTag, truncated.Call.ProcessTag)
		require.Equal(t, record.Call.ExitCode, truncated.Call.ExitCode)
		require.True(t, strings.HasPrefix(result, truncated.Call.Result))
	}
}
