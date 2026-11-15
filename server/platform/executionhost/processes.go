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

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
)

// The core's processes are terminals: one PTY stream each.

func toWindow(window environment.Window) execenv.Window {
	return execenv.Window{Cols: window.Cols, Rows: window.Rows}
}

func fromInfo(info execenv.ProcessInfo) environment.ProcessInfo {
	return environment.ProcessInfo{
		Tag: info.Tag, PTY: info.PTY, Started: info.Started, Running: info.Running,
		ExitCode: info.Exit.Code, ExitSignal: info.Exit.Signal, Output: info.Output[execenv.StreamPTY], Input: info.Input,
	}
}

func (m *machine) StartProcess(ctx context.Context, fence environment.Fence, spec environment.ProcessSpec) (environment.ProcessInfo, error) {
	executionSpec := execenv.ProcessSpec{Tag: spec.Tag, Argv: spec.Argv}
	if spec.PTY != nil {
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

func (m *machine) AttachProcess(ctx context.Context, tag string, from uint64) (environment.ProcessAttachment, error) {
	attachment, err := m.env.AttachProcess(ctx, tag, map[execenv.Stream]uint64{execenv.StreamPTY: from})
	if err != nil {
		return nil, m.fail("attach process", err)
	}
	return &processAttachment{attachment: attachment, machine: m}, nil
}

// processAttachment translates one execenv attachment's events.
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
			if output.Stream != execenv.StreamPTY {
				continue
			}
			return environment.ProcessOutput{Kind: environment.OutputData, Offset: output.Offset, Data: output.Data}, nil
		case execenv.OutputTruncated:
			if output.Stream != execenv.StreamPTY {
				continue
			}
			return environment.ProcessOutput{Kind: environment.OutputTruncated, Offset: output.Offset, To: output.To}, nil
		case execenv.OutputExit:
			return environment.ProcessOutput{Kind: environment.OutputExit, ExitCode: output.Exit.Code,
				ExitSignal: output.Exit.Signal}, nil
		}
	}
}

func (a *processAttachment) Close() error { return a.attachment.Close() }

func (m *machine) WriteInput(ctx context.Context, fence environment.Fence, tag string, offset uint64, data []byte) (uint64, error) {
	next, err := m.env.WriteInput(ctx, toFence(fence), tag, offset, data)
	if err != nil {
		return next, m.fail("write input", err)
	}
	return next, nil
}

func (m *machine) Resize(ctx context.Context, fence environment.Fence, tag string, window environment.Window) error {
	if err := m.env.Resize(ctx, toFence(fence), tag, toWindow(window)); err != nil {
		return m.fail("resize", err)
	}
	return nil
}

func (m *machine) SignalProcess(ctx context.Context, fence environment.Fence, tag string, signal environment.ProcessSignal) error {
	var executionSignal execenv.Signal
	switch signal {
	case environment.SignalInterrupt:
		executionSignal = execenv.SignalInterrupt
	default:
		return hostError("signal", errors.New("unknown signal"))
	}
	if err := m.env.SignalProcess(ctx, toFence(fence), tag, executionSignal); err != nil {
		return m.fail("signal", err)
	}
	return nil
}

func (m *machine) Screen(ctx context.Context, tag string) (environment.Screen, error) {
	screen, err := m.env.Screen(ctx, tag)
	if err != nil {
		return environment.Screen{}, m.fail("screen", err)
	}
	return environment.Screen{
		Data: screen.Data, Offset: screen.Offset, Window: environment.Window{Cols: screen.Window.Cols, Rows: screen.Window.Rows},
		AltScreen: screen.AltScreen, CursorRow: screen.CursorRow, CursorCol: screen.CursorCol, CursorHidden: screen.CursorHidden,
	}, nil
}
