// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package executionhost

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
)

// runningMachine places the test machine on the in-memory host and starts
// it under its first fence.
func runningMachine(t *testing.T) (environment.Machine, environment.Fence) {
	t.Helper()
	hosts, _, _ := memoryHosts(t, true)
	machine, err := hosts.Ensure(t.Context(), testGrantA, testSpec)
	require.NoError(t, err)
	fence := environment.Fence{GrantID: machine.GrantID(), Epoch: machine.Epoch(), Revision: 1}
	_, err = machine.Control(t.Context(), fence, environment.ControlRunning)
	require.NoError(t, err)
	return machine, fence
}

// readAll gathers one attachment's events until the process exits.
func readAll(t *testing.T, machine environment.Machine, tag model.AttemptEnvironmentProcessTag) (
	map[environment.ProcessStream]string, environment.ProcessOutput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	attachment, err := machine.AttachProcess(ctx, tag, 0)
	require.NoError(t, err)
	defer func() { _ = attachment.Close() }()
	data := map[environment.ProcessStream]string{}
	for {
		output, err := attachment.Next(ctx)
		require.NoError(t, err)
		switch output.Kind {
		case environment.OutputData:
			data[output.Stream] += string(output.Data)
		case environment.OutputExit:
			_, err = attachment.Next(ctx)
			require.ErrorIs(t, err, io.EOF, "the exit is the last event")
			return data, output
		}
	}
}

// process is the tag's process as the machine lists it.
func process(t *testing.T, machine environment.Machine, tag model.AttemptEnvironmentProcessTag) environment.ProcessInfo {
	t.Helper()
	infos, err := machine.ListProcesses(t.Context())
	require.NoError(t, err)
	for _, info := range infos {
		if info.Tag == tag {
			return info
		}
	}
	t.Fatalf("no process %s", tag)
	return environment.ProcessInfo{}
}

func exited(t *testing.T, machine environment.Machine, tag model.AttemptEnvironmentProcessTag) *environment.ProcessExit {
	t.Helper()
	var exit *environment.ProcessExit
	require.Eventually(t, func() bool {
		info := process(t, machine, tag)
		exit = info.Exit
		return !info.Running
	}, 5*time.Second, 10*time.Millisecond)
	require.NotNil(t, exit, "an exited process says how it ended")
	return exit
}

func TestProcessesKeepATerminalOnOnePTYStream(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	machine, fence := runningMachine(t)
	window := environment.Window{Cols: 80, Rows: 24}
	started, err := machine.StartProcess(ctx, fence, environment.ProcessSpec{Tag: "terminal:1",
		Streams: environment.ProcessStreamsPTY, PTY: &window})
	require.NoError(t, err)
	require.Equal(t, environment.ProcessStreamsPTY, started.Streams)
	require.True(t, started.Running)
	require.Nil(t, started.Exit, "a running process has no exit")

	_, err = machine.WriteInput(ctx, fence, "terminal:1", 0, []byte("echo hi\nexit 3\n"))
	require.NoError(t, err)
	data, exit := readAll(t, machine, "terminal:1")
	require.Equal(t, []environment.ProcessStream{environment.ProcessStreamPTY}, keys(data), "a terminal has only its PTY")
	require.Contains(t, data[environment.ProcessStreamPTY], "hi\r\n")
	require.Equal(t, 3, exit.ExitCode)

	info := process(t, machine, "terminal:1")
	require.Equal(t, &environment.ProcessExit{Code: 3}, info.Exit)
	require.EqualValues(t, len(data[environment.ProcessStreamPTY]), info.Output[environment.ProcessStreamPTY])
	screen, err := machine.Screen(ctx, "terminal:1")
	require.NoError(t, err)
	require.Contains(t, screen.Text, "hi", "the screen keeps its rows as text after the exit")
}

func TestProcessesPipeACommandsOutputApart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	machine, fence := runningMachine(t)
	started, err := machine.StartProcess(ctx, fence, environment.ProcessSpec{Tag: "assistant:1", Argv: []string{"echo", "hi"},
		Env: []string{"A=1", "A=2"}, Dir: "src", Streams: environment.ProcessStreamsPipes})
	require.NoError(t, err)
	require.Equal(t, environment.ProcessStreamsPipes, started.Streams)

	data, exit := readAll(t, machine, "assistant:1")
	require.Equal(t, "hi\n", data[environment.ProcessStreamStdout])
	require.NotContains(t, data, environment.ProcessStreamPTY, "a piped command has no terminal")
	require.Zero(t, exit.ExitCode)
	info := process(t, machine, "assistant:1")
	require.Equal(t, &environment.ProcessExit{}, info.Exit)
	require.EqualValues(t, 3, info.Output[environment.ProcessStreamStdout])
	require.Contains(t, info.Output, environment.ProcessStreamStderr)
	_, err = machine.Screen(ctx, "assistant:1")
	require.Error(t, err, "a piped command has no screen")

	_, err = machine.StartProcess(ctx, fence, environment.ProcessSpec{Tag: "assistant:2", Argv: []string{"false"},
		Streams: environment.ProcessStreamsPipes})
	require.NoError(t, err)
	require.Equal(t, &environment.ProcessExit{Code: 1}, exited(t, machine, "assistant:2"))
}

func TestProcessesEndACommandPastItsTimeout(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	machine, fence := runningMachine(t)
	// With its input open the in-memory command waits for more, so only
	// the timeout ends it.
	_, err := machine.StartProcess(ctx, fence, environment.ProcessSpec{Tag: "assistant:1", Argv: []string{"true"},
		Streams: environment.ProcessStreamsPipes, Stdin: true, Timeout: 50 * time.Millisecond})
	require.NoError(t, err)
	require.Equal(t, &environment.ProcessExit{Signal: "TERM"}, exited(t, machine, "assistant:1"))
	require.ErrorIs(t, machine.SignalProcess(ctx, fence, "assistant:1", environment.SignalKill), environment.ErrProcessExited,
		"an exited process takes no signal")

	// The core kills a command it gives up on.
	_, err = machine.StartProcess(ctx, fence, environment.ProcessSpec{Tag: "assistant:2", Argv: []string{"true"},
		Streams: environment.ProcessStreamsPipes, Stdin: true})
	require.NoError(t, err)
	require.NoError(t, machine.SignalProcess(ctx, fence, "assistant:2", environment.SignalKill))
	require.Equal(t, &environment.ProcessExit{Signal: "KILL"}, exited(t, machine, "assistant:2"))
	_, err = machine.StartProcess(ctx, fence, environment.ProcessSpec{Tag: "assistant:3", Argv: []string{"true"},
		Streams: environment.ProcessStreamsPipes, Stdin: true})
	require.NoError(t, err)
	require.NoError(t, machine.SignalProcess(ctx, fence, "assistant:3", environment.SignalTerminate))
	require.Equal(t, &environment.ProcessExit{Signal: "TERM"}, exited(t, machine, "assistant:3"))
	require.Error(t, machine.SignalProcess(ctx, fence, "assistant:3", "hangup"), "the core sends only its own signals")
}

func TestProcessesRefuseASpecThatCannotStart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	machine, fence := runningMachine(t)
	window := environment.Window{Cols: 80, Rows: 24}
	for name, spec := range map[string]environment.ProcessSpec{
		"no wiring":          {Tag: "assistant:1", Argv: []string{"true"}},
		"pipes with no argv": {Tag: "assistant:1", Streams: environment.ProcessStreamsPipes},
		"pipes with a pty":   {Tag: "assistant:1", Argv: []string{"true"}, Streams: environment.ProcessStreamsPipes, PTY: &window},
		"pty with no window": {Tag: "terminal:1", Streams: environment.ProcessStreamsPTY},
		"negative timeout":   {Tag: "assistant:1", Argv: []string{"true"}, Streams: environment.ProcessStreamsPipes, Timeout: -1},
		"bad env entry":      {Tag: "assistant:1", Argv: []string{"true"}, Streams: environment.ProcessStreamsPipes, Env: []string{"A"}},
		"dir leaves root":    {Tag: "assistant:1", Argv: []string{"true"}, Streams: environment.ProcessStreamsPipes, Dir: "/etc"},
	} {
		_, err := machine.StartProcess(ctx, fence, spec)
		require.Error(t, err, name)
	}
	infos, err := machine.ListProcesses(ctx)
	require.NoError(t, err)
	require.Empty(t, infos, "nothing started")
}

func TestSignalNamesRoundTrip(t *testing.T) {
	t.Parallel()
	require.Empty(t, environment.SignalName(0))
	require.Equal(t, "KILL", environment.SignalName(9))
	require.Equal(t, "40", environment.SignalName(40), "an unnamed signal keeps its number")
	for _, number := range []int{0, 2, 9, 15, 40} {
		require.Equal(t, number, environment.ProcessExit{Signal: environment.SignalName(number)}.SignalNumber())
	}
}

func keys(data map[environment.ProcessStream]string) []environment.ProcessStream {
	result := make([]environment.ProcessStream, 0, len(data))
	for stream := range data {
		result = append(result, stream)
	}
	return result
}
