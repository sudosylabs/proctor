// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAssistantSessionWritesRequireApproval(t *testing.T) {
	t.Parallel()
	text := json.RawMessage(`{"session":"assistant:1","text":"print(3)","keys":["enter"]}`)
	keys := json.RawMessage(`{"session":"assistant:1","keys":["ctrl_c"]}`)
	poll := json.RawMessage(`{"session":"assistant:1","wait_ms":250}`)
	require.True(t, toolNeedsApproval("start_session", nil))
	require.True(t, toolNeedsApproval("send_input", text))
	require.True(t, toolNeedsApproval("send_input", keys))
	require.False(t, toolNeedsApproval("send_input", poll))
	require.False(t, toolNeedsApproval("stop_session", poll))
	require.Equal(t, `"print(3)" + [enter]`, approvalSummary("send_input", text))
	parsed, ok := parseSessionInput(text)
	require.True(t, ok)
	require.Equal(t, []byte("print(3)\r"), parsed.data())

	for _, raw := range []string{
		`{"session":"terminal:1","text":"invalid"}`,
		`{"session":"assistant:1","text":"\u001b[A"}`,
		`{"session":"assistant:1","keys":["unknown"]}`,
		`{"session":"assistant:1","wait_ms":249}`,
		`{"session":"assistant:1","data":"raw"}`,
	} {
		_, valid := parseSessionInput(json.RawMessage(raw))
		require.False(t, valid, raw)
	}
}

func TestAssistantSessionOutputAnswersWithCleanTextOrScreen(t *testing.T) {
	t.Parallel()
	require.Equal(t, "red plain\nnext", stripTerminal("\x1b[31mred\x1b[0m \x1b]0;title\aplain\r\n\x1b(B\x1bPstrings\x1b\\next"))
	session := model.AssistantSession{Tag: "assistant:3", ReadOffset: 10}
	screen := sessionResult(session, sessionInputArgs{Text: "3", Keys: []string{"enter"}},
		environment.AssistantSessionOutput{Data: []byte("redraw"), AltScreen: true, Screen: []string{"first", "second"}}, "")
	require.Contains(t, screen.forModel, "session: assistant:3")
	require.Contains(t, screen.forModel, "Full-screen program")
	require.Contains(t, screen.forModel, "first\nsecond")
	require.NotContains(t, screen.forModel, "redraw")
	require.JSONEq(t, `{"text":"3","keys":["enter"],"output":"first\nsecond","output_bytes":12}`, screen.record)
	large := sessionResult(session, sessionInputArgs{}, environment.AssistantSessionOutput{Data: []byte(strings.Repeat("x", 11000)), Next: 11010}, "")
	require.Contains(t, large.forModel, "11000 bytes total")
	require.True(t, large.truncated)
}

type sessionToolStore struct {
	store.AssistantTranscriptStore
	requests []model.AssistantCall
	decision string
}

func (s *sessionToolStore) AppendCall(_ context.Context, input *store.AssistantCallAppend) (*model.AssistantRecord, error) {
	s.requests = append(s.requests, input.Call)
	return &model.AssistantRecord{Call: &input.Call}, nil
}

func (s *sessionToolStore) CompleteCall(ctx context.Context, input *store.AssistantCallAppend) (*model.AssistantRecord, error) {
	return s.AppendCall(ctx, input)
}

func (s *sessionToolStore) Decision(context.Context, store.AssistantRunAccess, model.AssistantCallID) (string, error) {
	return s.decision, nil
}

func (*sessionToolStore) ListSessions(context.Context, store.AssistantSessionQuery) ([]model.AssistantSession, error) {
	return nil, nil
}

func (*sessionToolStore) ReserveSession(context.Context, *store.AssistantSessionReservation) (*store.AssistantSessionSlot, error) {
	return nil, store.NewErrConflict("assistant_session", "assistant_session_limit", nil)
}

func TestAssistantSessionRefusalsReachTheModel(t *testing.T) {
	t.Parallel()
	transcripts := &sessionToolStore{decision: "decline"}
	publisher := &realtimetest.Publisher{}
	r := &turnRun{
		service: &Service{terminals: &environment.Terminals{}, transcripts: transcripts, publisher: publisher,
			newCallID: model.NewAssistantCallID, now: time.Now, limits: Limits{ApprovalTimeout: time.Second}},
		ctx: t.Context(), decided: make(chan struct{}, 1), conversation: model.NewAssistantConversationID(),
		key:    store.AssistantRunAccess{TurnID: model.NewAssistantTurnID()},
		recipe: store.AssistantRecipe{Execution: true, Policy: model.AssistantPolicy{Level: model.AssistantLevelPair, Approvals: model.AssistantApprovalAlwaysAsk}},
	}
	r.target = realtime.AssistantTarget{SittingID: model.NewExamSittingID(), AttemptID: model.NewExamAttemptID(),
		CandidateID: model.NewUserID(), SessionID: model.NewSessionID(), TurnID: r.key.TurnID}
	publisher.Observe = func(call realtimetest.Call) {
		if call.Event.Name == "exam_attempt_assistant_approval_requested" {
			require.Contains(t, string(call.Event.Data), "enter")
			r.notifyDecision()
		}
	}
	result, failed, end := r.runTool(ModelToolCall{Name: "send_input", Arguments: json.RawMessage(`{"session":"assistant:1","text":"hello","keys":["enter"]}`)})
	require.True(t, failed)
	require.Nil(t, end)
	require.Contains(t, result, "did not approve")
	require.Len(t, transcripts.requests, 2)
	require.Equal(t, "pending", transcripts.requests[0].Approval)
	require.Equal(t, "declined", transcripts.requests[1].Outcome)

	r.recipe.Policy.Approvals = model.AssistantApprovalAutoWithinLevel
	result, failed, end = r.runTool(ModelToolCall{Name: "start_session", Arguments: json.RawMessage(`{"command":"cat"}`)})
	require.True(t, failed)
	require.Nil(t, end)
	require.Equal(t, "Two sessions are already running. Stop one first.", result)
}
