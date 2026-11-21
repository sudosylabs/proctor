// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type sessionTestHosts struct {
	environment.Hosts
	start           *environment.ProcessSpec
	signals         *[]environment.ProcessSignal
	ignoreInterrupt bool
	now             func() time.Time
}

func (h sessionTestHosts) Ensure(ctx context.Context, grant environment.GrantRef, spec environment.MachineSpec) (environment.Machine, error) {
	machine, err := h.Hosts.Ensure(ctx, grant, spec)
	if err != nil {
		return nil, err
	}
	return sessionTestMachine{Machine: machine, hosts: h}, nil
}

type sessionTestMachine struct {
	environment.Machine
	hosts sessionTestHosts
}

func (m sessionTestMachine) StartProcess(ctx context.Context, fence environment.Fence, spec environment.ProcessSpec) (environment.ProcessInfo, error) {
	if m.hosts.start != nil {
		*m.hosts.start = spec
	}
	return m.Machine.StartProcess(ctx, fence, spec)
}

func (m sessionTestMachine) SignalProcess(ctx context.Context, fence environment.Fence, tag model.AttemptEnvironmentProcessTag,
	signal environment.ProcessSignal) error {
	if m.hosts.signals != nil {
		*m.hosts.signals = append(*m.hosts.signals, signal)
	}
	if m.hosts.ignoreInterrupt && signal == environment.SignalInterrupt {
		return nil
	}
	return m.Machine.SignalProcess(ctx, fence, tag, signal)
}

func sessionTerminals(t *testing.T, hosts sessionTestHosts) (*environment.Terminals, environment.CandidateAccess, *candidatesFake) {
	t.Helper()
	r := newRig(t, environment.OwnerTiming{})
	r.running()
	hosts.Hosts = r.hosts
	candidate := &candidatesFake{userID: model.NewUserID()}
	terminals, err := environment.NewTerminals(environment.TerminalsDependencies{
		Environments: r.environments, Evidence: r.evidence, Hosts: hosts, Candidates: candidate, Now: hosts.now,
	})
	require.NoError(t, err)
	return terminals, environment.CandidateAccess{AttemptID: r.attemptID, ConnectionID: model.NewAttemptConnectionID()}, candidate
}

func TestAssistantSessionReadsAndWritesUnderALiveCandidate(t *testing.T) {
	t.Parallel()
	var spec environment.ProcessSpec
	terminals, access, candidate := sessionTerminals(t, sessionTestHosts{start: &spec})
	ctx := t.Context()
	var inv invocation.Invocation
	tag := model.AttemptEnvironmentProcessTag("assistant:1")
	err := terminals.StartAssistantSession(ctx, inv, access, environment.AssistantSessionStart{Tag: tag, Command: "true", Timeout: time.Minute})
	require.NoError(t, err)
	require.Equal(t, time.Minute, spec.Timeout)
	require.Equal(t, environment.ProcessStreamsPTY, spec.Streams)
	require.Equal(t, environment.AssistantSessionWindow, *spec.PTY)
	require.Equal(t, []string{"/bin/sh", "-c", "true"}, spec.Argv)
	require.Equal(t, []string{"TERM=xterm-256color", "NO_COLOR=1", "PAGER=cat", "GIT_PAGER=cat", "LANG=C.UTF-8"}, spec.Env)

	started := time.Now()
	output, err := terminals.ReadAssistantSession(ctx, inv, access, tag, 0, 5*time.Second)
	require.NoError(t, err)
	require.Less(t, time.Since(started), 4*time.Second, "settle returns before the full wait once output arrives")
	require.Contains(t, string(output.Data), "true\n", "the memory PTY echoes the starting command")
	require.NotEmpty(t, output.Screen)
	require.Nil(t, output.Exit)
	require.NoError(t, terminals.WriteAssistantInput(ctx, inv, access, tag, []byte("echo hello\rexit\r")))
	finished, err := terminals.ReadAssistantSession(ctx, inv, access, tag, output.Next, 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, string(finished.Data), "echo hello\nhello\n")
	require.NotNil(t, finished.Exit)
	require.Zero(t, finished.Exit.Code)
	require.Empty(t, finished.Screen)
	require.ErrorIs(t, terminals.WriteAssistantInput(ctx, inv, access, tag, []byte("again")), environment.ErrTerminalExited)

	_, err = terminals.ReadAssistantSession(ctx, inv, access, "terminal:1", 0, time.Second)
	require.ErrorIs(t, err, environment.ErrAssistantCommandInvalid)
	require.ErrorIs(t, terminals.WriteAssistantInput(ctx, inv, access, "terminal:1", []byte("refused")), environment.ErrAssistantCommandInvalid)
	_, err = terminals.StopAssistantSession(ctx, inv, access, "terminal:1", 0)
	require.ErrorIs(t, err, environment.ErrAssistantCommandInvalid)
	candidate.refuseWith("exam.attempt.unavailable")
	_, err = terminals.ReadAssistantSession(ctx, inv, access, tag, 0, time.Second)
	require.Error(t, err, "access is checked again after the start")
}

func TestAssistantSessionStopsWithInterruptOrKill(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		ignore  bool
		signal  string
		signals []environment.ProcessSignal
	}{
		{"interrupt", false, "INT", []environment.ProcessSignal{environment.SignalInterrupt}},
		{"kill", true, "KILL", []environment.ProcessSignal{environment.SignalInterrupt, environment.SignalKill}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var signals []environment.ProcessSignal
			terminals, access, _ := sessionTerminals(t, sessionTestHosts{ignoreInterrupt: test.ignore, signals: &signals})
			ctx := t.Context()
			var inv invocation.Invocation
			tag := model.AttemptEnvironmentProcessTag("assistant:1")
			start := environment.AssistantSessionStart{Tag: tag, Command: "true", Timeout: time.Minute}
			require.NoError(t, terminals.StartAssistantSession(ctx, inv, access, start))
			first, err := terminals.ReadAssistantSession(ctx, inv, access, tag, 0, time.Second)
			require.NoError(t, err)
			require.Contains(t, string(first.Data), "true")
			require.NoError(t, terminals.WriteAssistantInput(ctx, inv, access, tag, []byte("later")))
			out, err := terminals.StopAssistantSession(ctx, inv, access, tag, first.Next)
			require.NoError(t, err)
			require.Contains(t, string(out.Data), "later")
			require.NotContains(t, string(out.Data), "true")
			require.NotNil(t, out.Exit)
			require.Equal(t, test.signal, out.Exit.Signal)
			require.Equal(t, test.signals, signals)
			replayed, err := terminals.StopAssistantSession(ctx, inv, access, tag, first.Next)
			require.NoError(t, err)
			require.Equal(t, out.Exit, replayed.Exit)
			require.Equal(t, test.signals, signals)
		})
	}
}

func TestAssistantKeysMapToXtermInput(t *testing.T) {
	t.Parallel()
	keys := map[string]string{
		"enter": "\r", "tab": "\t", "escape": "\x1b", "backspace": "\x7f",
		"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
		"home": "\x1b[H", "end": "\x1b[F", "page_up": "\x1b[5~", "page_down": "\x1b[6~",
		"ctrl_c": "\x03", "ctrl_d": "\x04", "ctrl_z": "\x1a", "ctrl_l": "\x0c",
	}
	for name, expected := range keys {
		data, ok := environment.AssistantKeyBytes(name)
		require.True(t, ok, name)
		require.Equal(t, expected, string(data), name)
	}
	_, ok := environment.AssistantKeyBytes("\x1b")
	require.False(t, ok)
}

func TestAssistantSessionKillsAProcessAtTheDurableDeadline(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var signals []environment.ProcessSignal
	terminals, access, _ := sessionTerminals(t, sessionTestHosts{signals: &signals, now: func() time.Time { return now }})
	var inv invocation.Invocation
	const tag = "assistant:1"
	require.NoError(t, terminals.StartAssistantSession(t.Context(), inv, access,
		environment.AssistantSessionStart{Tag: tag, Command: "true", Timeout: time.Minute}))
	status, err := terminals.CheckAssistantSession(t.Context(), inv, access, model.AssistantSession{
		Tag: tag, StartedAt: now.Add(-time.Second), ReservedMilliseconds: 1000})
	require.NoError(t, err)
	require.Equal(t, "timed_out", status.Outcome)
	require.Equal(t, []environment.ProcessSignal{environment.SignalKill}, signals)
}

func TestAssistantSessionKeepsANormalExitPastTheDeadline(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var signals []environment.ProcessSignal
	terminals, access, _ := sessionTerminals(t, sessionTestHosts{signals: &signals, now: func() time.Time { return now }})
	ctx := t.Context()
	var inv invocation.Invocation
	const tag = "assistant:1"
	require.NoError(t, terminals.StartAssistantSession(ctx, inv, access,
		environment.AssistantSessionStart{Tag: tag, Command: "exit", Timeout: time.Minute}))
	out, err := terminals.ReadAssistantSession(ctx, inv, access, tag, 0, time.Second)
	require.NoError(t, err)
	require.NotNil(t, out.Exit)
	status, err := terminals.CheckAssistantSession(ctx, inv, access, model.AssistantSession{
		Tag: tag, StartedAt: now.Add(-time.Second), ReservedMilliseconds: 1000})
	require.NoError(t, err)
	require.Equal(t, "exited", status.Outcome)
	require.Equal(t, out.Exit, status.Exit)
	require.Empty(t, signals)
}
