// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package executionhost

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
)

func toWindow(window environment.Window) execenv.Window {
	return execenv.Window{Cols: window.Cols, Rows: window.Rows}
}

func fromStream(stream execenv.Stream) environment.ProcessStream {
	switch stream {
	case execenv.StreamStdout:
		return environment.ProcessStreamStdout
	case execenv.StreamStderr:
		return environment.ProcessStreamStderr
	}
	return environment.ProcessStreamPTY
}

func fromInfo(info execenv.ProcessInfo) environment.ProcessInfo {
	result := environment.ProcessInfo{
		Tag: model.AttemptEnvironmentProcessTag(info.Tag), Streams: environment.ProcessStreamsPipes, Started: info.Started, Ended: info.Ended,
		Running: info.Running, Output: make(map[environment.ProcessStream]uint64, len(info.Output)), Input: info.Input,
	}
	if info.PTY {
		result.Streams = environment.ProcessStreamsPTY
	}
	for stream, end := range info.Output {
		result.Output[fromStream(stream)] = end
	}
	if !info.Running {
		result.Exit = &environment.ProcessExit{Code: info.Exit.Code, Signal: environment.SignalName(info.Exit.Signal)}
	}
	return result
}

// toEnv turns NAME=value entries into execenv's map. A later entry for a
// name wins, as it does in a shell's environment.
func toEnv(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			return nil, errors.New("an environment entry is not NAME=value")
		}
		env[name] = value
	}
	return env, nil
}

func (m *machine) StartProcess(ctx context.Context, fence environment.Fence, spec environment.ProcessSpec) (environment.ProcessInfo, error) {
	if !spec.Valid() {
		return environment.ProcessInfo{}, hostError("start process", errors.New("invalid process spec"))
	}
	env, err := toEnv(spec.Env)
	if err != nil {
		return environment.ProcessInfo{}, hostError("start process", err)
	}
	executionSpec := execenv.ProcessSpec{Tag: string(spec.Tag), Argv: spec.Argv, Env: env, Dir: spec.Dir,
		Stdin: spec.Stdin, Timeout: spec.Timeout}
	if spec.Streams == environment.ProcessStreamsPTY {
		window := toWindow(*spec.PTY)
		executionSpec.PTY = &window
	}
	info, err := m.env.Start(ctx, toFence(fence), executionSpec)
	if err != nil {
		return environment.ProcessInfo{}, m.fail("start process", err)
	}
	return fromInfo(info), nil
}

func (m *machine) ListProcesses(ctx context.Context) ([]environment.ProcessInfo, error) {
	infos, err := m.env.ListProcesses(ctx)
	if err != nil {
		return nil, m.fail("list processes", err)
	}
	result := make([]environment.ProcessInfo, len(infos))
	for index, info := range infos {
		result[index] = fromInfo(info)
	}
	return result, nil
}

func (m *machine) AttachProcess(
	ctx context.Context,
	tag model.AttemptEnvironmentProcessTag,
	from uint64,
) (environment.ProcessAttachment, error) {
	// execenv refuses an offset for a stream the process lacks, so the
	// process's wiring decides which streams are named.
	info, err := m.env.Process(ctx, string(tag))
	if err != nil {
		return nil, m.fail("attach process", err)
	}
	offsets := map[execenv.Stream]uint64{execenv.StreamPTY: from}
	if !info.PTY {
		offsets = map[execenv.Stream]uint64{execenv.StreamStdout: from, execenv.StreamStderr: from}
	}
	attachment, err := m.env.AttachProcess(ctx, string(tag), offsets)
	if err != nil {
		return nil, m.fail("attach process", err)
	}
	return &processAttachment{attachment: attachment, machine: m}, nil
}

// processAttachment translates one execenv attachment's events, every
// stream's.
type processAttachment struct {
	attachment execenv.Attachment
	machine    *machine
}

func (a *processAttachment) Next(ctx context.Context) (environment.ProcessOutput, error) {
	for {
		output, err := a.attachment.Next(ctx)
		if errors.Is(err, io.EOF) {
			return environment.ProcessOutput{}, io.EOF
		}
		if err != nil {
			return environment.ProcessOutput{}, a.machine.fail("read process output", err)
		}
		switch output.Kind {
		case execenv.OutputData:
			return environment.ProcessOutput{Kind: environment.OutputData, Stream: fromStream(output.Stream),
				Offset: output.Offset, Data: output.Data}, nil
		case execenv.OutputTruncated:
			return environment.ProcessOutput{Kind: environment.OutputTruncated, Stream: fromStream(output.Stream),
				Offset: output.Offset, To: output.To}, nil
		case execenv.OutputExit:
			return environment.ProcessOutput{Kind: environment.OutputExit, ExitCode: output.Exit.Code,
				ExitSignal: output.Exit.Signal}, nil
		}
	}
}

func (a *processAttachment) Close() error { return a.attachment.Close() }

func (m *machine) WriteInput(
	ctx context.Context,
	fence environment.Fence,
	tag model.AttemptEnvironmentProcessTag,
	offset uint64,
	data []byte,
) (uint64, error) {
	next, err := m.env.WriteInput(ctx, toFence(fence), string(tag), offset, data)
	if err != nil {
		return next, m.fail("write input", err)
	}
	return next, nil
}

func (m *machine) Resize(
	ctx context.Context,
	fence environment.Fence,
	tag model.AttemptEnvironmentProcessTag,
	window environment.Window,
) error {
	if err := m.env.Resize(ctx, toFence(fence), string(tag), toWindow(window)); err != nil {
		return m.fail("resize", err)
	}
	return nil
}

func (m *machine) SignalProcess(
	ctx context.Context,
	fence environment.Fence,
	tag model.AttemptEnvironmentProcessTag,
	signal environment.ProcessSignal,
) error {
	var executionSignal execenv.Signal
	switch signal {
	case environment.SignalInterrupt:
		executionSignal = execenv.SignalInterrupt
	case environment.SignalTerminate:
		executionSignal = execenv.SignalTerminate
	case environment.SignalKill:
		// The guest signals the process's whole session, so a command's
		// children die with it.
		executionSignal = execenv.SignalKill
	default:
		return hostError("signal", errors.New("unknown signal"))
	}
	if err := m.env.SignalProcess(ctx, toFence(fence), string(tag), executionSignal); err != nil {
		return m.fail("signal", err)
	}
	return nil
}

func (m *machine) Screen(ctx context.Context, tag model.AttemptEnvironmentProcessTag) (environment.Screen, error) {
	screen, err := m.env.Screen(ctx, string(tag))
	if err != nil {
		return environment.Screen{}, m.fail("screen", err)
	}
	return environment.Screen{
		Data: screen.Data, Offset: screen.Offset, Window: environment.Window{Cols: screen.Window.Cols, Rows: screen.Window.Rows},
		AltScreen: screen.AltScreen, CursorRow: screen.CursorRow, CursorCol: screen.CursorCol, CursorHidden: screen.CursorHidden,
		Text: screen.Text,
	}, nil
}
