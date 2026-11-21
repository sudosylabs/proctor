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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
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
	parsed, ok := parseSessionInput(text)
	require.True(t, ok)
	require.Equal(t, []byte("print(3)\r"), parsed.data())
	require.Equal(t, `assistant:1 (python): "print(3)" + [enter]`, parsed.summary("python"))

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
	require.Equal(t, "red plain\nnext", environment.CleanAssistantScreen("\x1b[31mred\x1b[0m \x1b]0;title\aplain\r\n\x1b(B\x1bPstrings\x1b\\next"))
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
	requests    []model.AssistantCall
	sessions    []model.AssistantSession
	decision    string
	ends        []store.AssistantSessionEnd
	checkpoints []sessionCheckpoint
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

func (s *sessionToolStore) ListSessions(_ context.Context, query store.AssistantSessionQuery) ([]model.AssistantSession, error) {
	if query.Tag == "" {
		return nil, nil
	}
	return s.sessions, nil
}

type sessionCheckpoint struct {
	offset uint64
	wrote  bool
}

func (s *sessionToolStore) AdvanceSessionRead(_ context.Context, _ store.AssistantSessionAccess, _ string, offset uint64, wrote bool) error {
	s.checkpoints = append(s.checkpoints, sessionCheckpoint{offset: offset, wrote: wrote})
	return nil
}

func (s *sessionToolStore) EndSession(_ context.Context, end *store.AssistantSessionEnd) (*store.AssistantSessionEndResult, error) {
	s.ends = append(s.ends, *end)
	original := s.ends[0]
	return &store.AssistantSessionEndResult{Changed: len(s.ends) == 1,
		Record: &model.AssistantRecord{CreatedAt: time.Now(), Call: &model.AssistantCall{ProcessTag: original.Tag, Outcome: original.Outcome}}}, nil
}

func (*sessionToolStore) ReserveSession(context.Context, *store.AssistantSessionReservation) (*store.AssistantSessionSlot, error) {
	return nil, store.NewErrConflict("assistant_session", "assistant_session_limit", nil)
}

func TestAssistantSessionRefusalsReachTheModel(t *testing.T) {
	t.Parallel()
	transcripts := &sessionToolStore{decision: "decline", sessions: []model.AssistantSession{
		{Tag: "assistant:1", Command: "python", State: "running"},
	}}
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
			var approval struct {
				Summary string `json:"summary"`
			}
			require.NoError(t, json.Unmarshal(call.Event.Data, &approval))
			require.Equal(t, `assistant:1 (python): "hello" + [enter]`, approval.Summary)
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

type sessionToolEnvironment struct {
	store.AttemptEnvironmentStore
	row model.AttemptEnvironment
}

func (e sessionToolEnvironment) Get(context.Context, model.ExamAttemptID) (*model.AttemptEnvironment, error) {
	return &e.row, nil
}

type sessionToolCandidates struct{}

func (sessionToolCandidates) VerifyCandidate(_ context.Context, _ invocation.Invocation, access environment.CandidateAccess) (environment.CandidateAttempt, error) {
	return environment.CandidateAttempt{AttemptID: access.AttemptID}, nil
}

type sessionToolHosts struct {
	environment.Hosts
	machine *sessionToolMachine
}

func (h sessionToolHosts) Ensure(context.Context, environment.GrantRef, environment.MachineSpec) (environment.Machine, error) {
	return h.machine, nil
}

type sessionToolMachine struct {
	environment.Machine
	process    environment.ProcessInfo
	writes     []string
	signals    []environment.ProcessSignal
	attachment environment.ProcessAttachment
}

func (*sessionToolMachine) Epoch() string { return "epoch" }
func (m *sessionToolMachine) ListProcesses(context.Context) ([]environment.ProcessInfo, error) {
	return []environment.ProcessInfo{m.process}, nil
}
func (m *sessionToolMachine) SignalProcess(_ context.Context, _ environment.Fence, _ model.AttemptEnvironmentProcessTag, signal environment.ProcessSignal) error {
	m.signals = append(m.signals, signal)
	m.process.Running = false
	m.process.Exit = &environment.ProcessExit{Signal: "KILL"}
	return nil
}
func (m *sessionToolMachine) WriteInput(_ context.Context, _ environment.Fence, _ model.AttemptEnvironmentProcessTag, at uint64, data []byte) (uint64, error) {
	m.writes = append(m.writes, string(data))
	return at + uint64(len(data)), nil
}
func (m *sessionToolMachine) AttachProcess(context.Context, model.AttemptEnvironmentProcessTag, uint64) (environment.ProcessAttachment, error) {
	if m.attachment != nil {
		return m.attachment, nil
	}
	return nil, errors.New("attachment unavailable")
}

type sessionToolEvidence struct {
	store.ExamAttemptWorkspaceStore
}

func testSessionTool(t *testing.T, started time.Time, now func() time.Time) (*turnRun, *sessionToolStore, *sessionToolMachine, *realtimetest.Publisher) {
	t.Helper()
	tag := "assistant:1"
	machine := &sessionToolMachine{process: environment.ProcessInfo{
		Tag: model.AttemptEnvironmentProcessTag(tag), Streams: environment.ProcessStreamsPTY, Running: true,
	}}
	id := model.NewExamAttemptID()
	terminals, err := environment.NewTerminals(environment.TerminalsDependencies{
		Environments: sessionToolEnvironment{row: model.AttemptEnvironment{AttemptID: id, State: model.AttemptEnvironmentRunning,
			Desired: model.AttemptEnvironmentDesiredRunning, GrantID: "grant", Epoch: "epoch"}},
		Hosts: sessionToolHosts{machine: machine}, Candidates: sessionToolCandidates{}, Evidence: sessionToolEvidence{}, Now: now,
	})
	require.NoError(t, err)
	transcript := &sessionToolStore{sessions: []model.AssistantSession{
		{Tag: tag, State: "running", StartedAt: started, ReservedMilliseconds: 1000, ReadOffset: 12},
	}}
	publisher := &realtimetest.Publisher{}
	editor := store.ExamAttemptWorkspaceEditorAccess{AttemptID: id, CandidateUserID: model.NewUserID()}
	run := &turnRun{service: &Service{terminals: terminals, transcripts: transcript, publisher: publisher, now: now},
		ctx: t.Context(), access: attempt.CandidateAccess{AttemptID: id}, key: store.AssistantRunAccess{Editor: editor},
		target: realtime.AssistantTarget{SittingID: model.NewExamSittingID()}}
	return run, transcript, machine, publisher
}

func TestAssistantToolKillsExpiredSessionBeforeItWrites(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r, transcript, machine, publisher := testSessionTool(t, now.Add(-time.Second), func() time.Time { return now })
	result := r.sendInput(json.RawMessage(`{"session":"assistant:1","text":"late"}`))
	require.True(t, result.isError)
	require.Equal(t, []environment.ProcessSignal{environment.SignalKill}, machine.signals)
	require.Empty(t, machine.writes)
	require.Len(t, transcript.ends, 1)
	require.Equal(t, "timed_out", transcript.ends[0].Outcome)
	require.Nil(t, transcript.ends[0].ExitCode)
	// The second caller observes the first terminal record and has nothing to publish.
	_, err := r.service.endSession(t.Context(), r.target.SittingID, r.key.Editor, transcript.sessions[0], "stopped", nil)
	require.NoError(t, err)
	require.Len(t, publisher.Calls(), 1)
	require.Equal(t, "exam_attempt_assistant_session_ended", publisher.Calls()[0].Event.Name)
}

func TestAssistantToolCountsAWriteWhoseReadFails(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r, transcript, machine, _ := testSessionTool(t, now, func() time.Time { return now })
	result := r.sendInput(json.RawMessage(`{"session":"assistant:1","text":"hello"}`))
	require.True(t, result.isError)
	require.Contains(t, result.forModel, "output could not be read")
	require.Equal(t, []string{"hello"}, machine.writes)
	require.Equal(t, []sessionCheckpoint{{offset: 12, wrote: true}}, transcript.checkpoints)
}

type sessionExitAttachment struct {
	onNext func()
}

func (a sessionExitAttachment) Next(context.Context) (environment.ProcessOutput, error) {
	a.onNext()
	return environment.ProcessOutput{Kind: environment.OutputExit, ExitCode: 7}, nil
}

func (sessionExitAttachment) Close() error { return nil }

func TestAssistantToolKeepsNormalExitWhenReadCrossesTheDeadline(t *testing.T) {
	t.Parallel()
	started := time.Now()
	now := started
	r, transcript, machine, _ := testSessionTool(t, started, func() time.Time { return now })
	machine.attachment = sessionExitAttachment{onNext: func() {
		now = started.Add(time.Second)
		machine.process.Running = false
		machine.process.Exit = &environment.ProcessExit{Code: 7}
	}}
	result := r.sendInput(json.RawMessage(`{"session":"assistant:1"}`))
	require.False(t, result.isError)
	require.Empty(t, machine.signals)
	require.Len(t, transcript.ends, 1)
	require.Equal(t, "exited", transcript.ends[0].Outcome)
	require.NotNil(t, transcript.ends[0].ExitCode)
	require.Equal(t, 7, *transcript.ends[0].ExitCode)
}
