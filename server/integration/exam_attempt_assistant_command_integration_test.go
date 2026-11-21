// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type commandIntegrationModel struct {
	results        chan assistant.ModelMessage
	sessionResults chan assistant.ModelMessage
	sessionCalls   []sessionIntegrationCall
}

func (m *commandIntegrationModel) Stream(ctx context.Context, request assistant.ModelRequest) (assistant.ModelStream, error) {
	last := request.Messages[len(request.Messages)-1]
	usage := assistant.ModelEvent{Kind: assistant.ModelUsageEvent, Usage: assistant.ModelUsage{Input: 80, Output: 15}}
	if last.Role == "user" && last.Text == "Try the interactive program." {
		m.sessionCalls = []sessionIntegrationCall{
			{"start_session", `{"command":"cat","wait_ms":250}`},
			{"send_input", `{"session":"assistant:2","text":"never-sent","keys":["enter"],"wait_ms":250}`},
			{"send_input", `{"session":"assistant:2","wait_ms":250}`},
			{"send_input", `{"session":"assistant:2","text":"hello","keys":["enter"],"wait_ms":500}`},
			{"stop_session", `{"session":"assistant:2"}`},
			{"start_session", `{"command":"cat","wait_ms":250}`},
			{"start_session", `{"command":"cat","wait_ms":250}`},
		}
	}
	if m.sessionCalls != nil {
		if last.Role == "tool" {
			select {
			case m.sessionResults <- last:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if len(m.sessionCalls) == 0 {
			m.sessionCalls = nil
			return &integrationStream{events: []assistant.ModelEvent{usage, {Kind: assistant.ModelStop, Reason: "end_turn"}}}, nil
		}
		call := m.sessionCalls[0]
		m.sessionCalls = m.sessionCalls[1:]
		return &integrationStream{events: []assistant.ModelEvent{
			{Kind: assistant.ModelToolCallStart, ID: "session-" + strconv.Itoa(len(m.sessionCalls)), Name: call.name},
			{Kind: assistant.ModelToolCallArgsDelta, ID: "session-" + strconv.Itoa(len(m.sessionCalls)), Text: call.args},
			{Kind: assistant.ModelToolCallEnd, ID: "session-" + strconv.Itoa(len(m.sessionCalls))},
			usage, {Kind: assistant.ModelStop, Reason: "tool_use"},
		}}, nil
	}
	if last.Role == "tool" {
		select {
		case m.results <- last:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &integrationStream{events: []assistant.ModelEvent{usage, {Kind: assistant.ModelStop, Reason: "end_turn"}}}, nil
	}

	arguments, err := json.Marshal(map[string]string{"command": "echo assistant > " + last.Text + ".txt\nseq 30000"})
	if err != nil {
		return nil, err
	}
	return &integrationStream{events: []assistant.ModelEvent{
		{Kind: assistant.ModelToolCallStart, ID: "command", Name: "run_command"},
		{Kind: assistant.ModelToolCallArgsDelta, ID: "command", Text: string(arguments)},
		{Kind: assistant.ModelToolCallEnd, ID: "command"},
		usage,
		{Kind: assistant.ModelStop, Reason: "tool_use"},
	}}, nil
}

func checkAssistantCommandFlow(t *testing.T, service *assistant.Service, inv invocation.Invocation, access attempt.CandidateAccess,
	gateway *commandIntegrationModel, journal func() *attempt.WorkspaceJournalPage) {
	t.Helper()
	ctx := t.Context()
	transcript := func() []model.AssistantRecord {
		page, err := service.ListTranscript(ctx, inv, assistant.TranscriptQuery{AttemptID: access.AttemptID, Limit: 200})
		require.NoError(t, err)
		return page.Records
	}
	for _, decision := range []string{"decline", "expired", "accept"} {
		t.Run(decision, func(t *testing.T) {
			checkAssistantCommandDecision(t, service, inv, access, gateway, transcript, journal, decision)
		})
	}
}

func checkAssistantCommandDecision(t *testing.T, service *assistant.Service, inv invocation.Invocation, access attempt.CandidateAccess,
	gateway *commandIntegrationModel, transcript func() []model.AssistantRecord, journal func() *attempt.WorkspaceJournalPage, decision string) {
	t.Helper()
	ctx := t.Context()
	started, err := service.SendMessage(ctx, inv, assistant.SendMessageCommand{Access: access,
		Text: decision, IdempotencyKey: "assistant-command-" + decision})
	require.NoError(t, err)

	findPhase := func(phase string) *model.AssistantCall {
		for _, record := range transcript() {
			if call := record.Call; call != nil && call.TurnID == started.TurnID && call.Tool == "run_command" && call.Phase == phase {
				return call
			}
		}
		return nil
	}
	require.Eventually(t, func() bool { return findPhase("requested") != nil }, 15*time.Second, 25*time.Millisecond)
	request := findPhase("requested")
	require.Equal(t, "pending", request.Approval)
	if decision != "expired" {
		require.NoError(t, service.DecideApproval(ctx, inv, assistant.DecideApprovalCommand{
			Access: access, CallID: request.CallID, Accept: decision == "accept",
		}))
	}

	require.Eventually(t, func() bool {
		for _, record := range transcript() {
			if turn := record.Turn; turn != nil && turn.TurnID == started.TurnID && turn.Role == "assistant" && turn.Final {
				return turn.StopReason == "end_turn"
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond)
	answer, completed := findPhase("decision"), findPhase("completed")
	require.NotNil(t, answer)
	require.NotNil(t, completed)
	require.Equal(t, decision, answer.Approval)
	require.Equal(t, decision, completed.Approval)
	preview := <-gateway.results
	if decision != "accept" {
		require.Nil(t, findPhase("reserved"), "a refused command never obtained a process slot")
		require.Empty(t, completed.ProcessTag)
		require.Equal(t, "declined", completed.Outcome)
		require.True(t, preview.IsError)
		for _, entry := range journal().Entries {
			require.NotEqual(t, decision+".txt", entry.NewPath)
		}
		return
	}

	checkAssistantCommandEvidence(t, completed, preview, journal)
}

func checkAssistantCommandEvidence(t *testing.T, completed *model.AssistantCall, preview assistant.ModelMessage, journal func() *attempt.WorkspaceJournalPage) {
	t.Helper()
	require.Equal(t, "assistant:1", completed.ProcessTag, "only the accepted command used a process slot")
	require.Equal(t, "ok", completed.Outcome)
	require.NotNil(t, completed.ExitCode)
	require.Zero(t, *completed.ExitCode)
	require.False(t, completed.Truncated)
	var output strings.Builder
	output.WriteString("[stdout]\n")
	for i := 1; i <= 30000; i++ {
		output.WriteString(strconv.Itoa(i))
		output.WriteByte('\n')
	}
	output.WriteByte('\n')
	require.Equal(t, output.String(), completed.Result)
	require.False(t, preview.IsError)
	require.Less(t, len(preview.Text), 31000)
	require.Contains(t, preview.Text, "[... truncated ...]")
	require.Contains(t, preview.Text, "30000")
	wantAuthor := model.NewEnvironmentWorkspaceAuthor(0, model.AttemptEnvironmentProcessTag(completed.ProcessTag))
	require.Eventually(t, func() bool {
		for _, entry := range journal().Entries {
			if entry.NewPath == "accept.txt" {
				return entry.Author == wantAuthor
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond, "the harvested file links to the accepted command's process")
}

type sessionIntegrationCall struct{ name, args string }
