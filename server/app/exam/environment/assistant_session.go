// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Assistant session limits bound the time and bytes of each interaction.
const (
	MaxAssistantSessions           = model.MaxAssistantSessions
	DefaultAssistantSessionTimeout = 300 * time.Second
	AssistantSessionSettle         = 500 * time.Millisecond
	MinAssistantWait               = 250 * time.Millisecond
	MaxAssistantWait               = 30 * time.Second
	MaxAssistantInputBytes         = 4 << 10
	MaxAssistantKeys               = 32
)

// AssistantSessionWindow gives every assistant session the same PTY size.
var AssistantSessionWindow = Window{Cols: 120, Rows: 40}

// AssistantSessionStart describes a command reserved by the assistant Store.
type AssistantSessionStart struct {
	Tag     model.AttemptEnvironmentProcessTag
	Command string
	Timeout time.Duration
}

// AssistantSessionOutput carries the absolute stream position after the returned bytes.
type AssistantSessionOutput struct {
	Data      []byte
	Next      uint64
	Dropped   bool
	Exit      *ProcessExit
	Ended     time.Time
	Screen    []string
	AltScreen bool
}

func (t *Terminals) assistantSessionMachine(ctx context.Context, inv invocation.Invocation, access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag) (reached, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return reached{}, err
	}
	if !strings.HasPrefix(string(tag), AssistantTagPrefix) || !model.ValidAssistantProcessTag(string(tag)) {
		return reached{}, ErrAssistantCommandInvalid
	}
	at, err := t.reach(ctx, candidate.AttemptID)
	return at, terminalFailure(err)
}

// StartAssistantSession starts the reserved PTY under the current environment fence.
func (t *Terminals) StartAssistantSession(ctx context.Context, inv invocation.Invocation, access CandidateAccess,
	start AssistantSessionStart) error {
	at, err := t.assistantSessionMachine(ctx, inv, access, start.Tag)
	if err != nil {
		return err
	}
	if start.Timeout < time.Second || start.Timeout > MaxAssistantCommandTimeout {
		return ErrAssistantCommandInvalid
	}
	window := AssistantSessionWindow
	spec := ProcessSpec{
		Tag: start.Tag, Argv: []string{"/bin/sh", "-c", start.Command}, Streams: ProcessStreamsPTY, PTY: &window, Timeout: start.Timeout,
		Env: []string{"TERM=xterm-256color", "NO_COLOR=1", "PAGER=cat", "GIT_PAGER=cat", "LANG=C.UTF-8"},
	}
	err = t.fenced(ctx, &at, func(fence Fence) error {
		_, startErr := at.machine.StartProcess(ctx, fence, spec)
		return startErr
	})
	if errors.Is(err, ErrMachineBusy) {
		return terminalFailure(ErrTerminalLimit)
	}
	return terminalFailure(terminalError(err))
}

func assistantSessionProcess(ctx context.Context, at reached, tag model.AttemptEnvironmentProcessTag) (ProcessInfo, error) {
	processes, err := at.machine.ListProcesses(ctx)
	if err != nil {
		return ProcessInfo{}, terminalFailure(terminalError(err))
	}
	for _, process := range processes {
		if process.Tag == tag && process.Streams == ProcessStreamsPTY {
			return process, nil
		}
	}
	return ProcessInfo{}, terminalFailure(ErrTerminalNotFound)
}

// AssistantSessionStatus is empty while the process is still running.
type AssistantSessionStatus struct {
	Outcome string
	Exit    *ProcessExit
	Ended   time.Time
}

// EndTime clamps a known host exit to this session's lifetime.
func (s AssistantSessionStatus) EndTime(started, now time.Time) time.Time {
	if s.Ended.IsZero() {
		return time.Time{}
	}
	ended := s.Ended
	if ended.After(now) {
		ended = now
	}
	if ended.Before(started) {
		ended = started
	}
	return model.TimeUTC(ended)
}

// CheckAssistantSession enforces the durable deadline before the candidate
// or model can use the process again.
func (t *Terminals) CheckAssistantSession(ctx context.Context, inv invocation.Invocation, access CandidateAccess,
	session model.AssistantSession) (AssistantSessionStatus, error) {
	tag := model.AttemptEnvironmentProcessTag(session.Tag)
	at, err := t.assistantSessionMachine(ctx, inv, access, tag)
	if err != nil {
		return AssistantSessionStatus{}, err
	}
	return checkAssistantSession(ctx, at, session, t.deps.Now(), func() error {
		return t.fenced(ctx, &at, func(fence Fence) error { return at.machine.SignalProcess(ctx, fence, tag, SignalKill) })
	})
}

func checkAssistantSession(ctx context.Context, at reached, session model.AssistantSession, now time.Time, kill func() error) (AssistantSessionStatus, error) {
	tag := model.AttemptEnvironmentProcessTag(session.Tag)
	deadline := session.StartedAt.Add(time.Duration(session.ReservedMilliseconds) * time.Millisecond)
	info, err := assistantSessionProcess(ctx, at, tag)
	if errors.Is(err, ErrTerminalNotFound) {
		if session.Starting {
			return AssistantSessionStatus{}, nil
		}
		return AssistantSessionStatus{Outcome: "ended"}, nil
	}
	if err != nil {
		return AssistantSessionStatus{}, err
	}
	if !info.Running {
		return AssistantSessionStatus{Outcome: "exited", Exit: info.Exit, Ended: info.Ended}, nil
	}
	if !now.Before(deadline) {
		if err = kill(); errors.Is(err, ErrProcessExited) {
			finished, readErr := assistantSessionProcess(ctx, at, tag)
			if readErr != nil {
				return AssistantSessionStatus{}, readErr
			}
			return AssistantSessionStatus{Outcome: "exited", Exit: finished.Exit, Ended: finished.Ended}, nil
		}
		if err != nil {
			return AssistantSessionStatus{}, terminalFailure(terminalError(err))
		}
		return AssistantSessionStatus{Outcome: "timed_out", Exit: &ProcessExit{Signal: "KILL"}}, nil
	}
	return AssistantSessionStatus{}, nil
}

// WriteAssistantInput writes once from the host-confirmed input position.
func (t *Terminals) WriteAssistantInput(ctx context.Context, inv invocation.Invocation, access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag, data []byte) error {
	at, err := t.assistantSessionMachine(ctx, inv, access, tag)
	if err != nil {
		return err
	}
	info, err := assistantSessionProcess(ctx, at, tag)
	if err != nil {
		return err
	}
	if !info.Running {
		return terminalFailure(ErrTerminalExited)
	}
	err = t.fenced(ctx, &at, func(fence Fence) error {
		_, writeErr := at.machine.WriteInput(ctx, fence, tag, info.Input, data)
		return writeErr
	})
	return terminalFailure(terminalError(err))
}

// ReadAssistantSession returns available PTY output and waits for quiet or exit.
func (t *Terminals) ReadAssistantSession(ctx context.Context, inv invocation.Invocation, access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag, from uint64, wait time.Duration) (AssistantSessionOutput, error) {
	at, err := t.assistantSessionMachine(ctx, inv, access, tag)
	if err != nil {
		return AssistantSessionOutput{}, err
	}
	if _, err = assistantSessionProcess(ctx, at, tag); err != nil {
		return AssistantSessionOutput{}, err
	}
	out, err := t.collectAssistantSession(ctx, at, tag, from, min(MaxAssistantWait, max(MinAssistantWait, wait)), AssistantSessionSettle)
	if err != nil {
		return out, err
	}
	info, err := assistantSessionProcess(ctx, at, tag)
	if out.Exit != nil {
		if err == nil {
			out.Ended = info.Ended
		}
		return out, nil
	}
	if err != nil || !info.Running {
		return out, err
	}
	screen, err := at.machine.Screen(ctx, tag)
	if err != nil {
		return out, terminalFailure(terminalError(err))
	}
	out.Screen, out.AltScreen = screen.Text, screen.AltScreen
	return out, nil
}

// collectAssistantSession bounds both the wait and memory even when a
// program continuously writes to its terminal.
func (t *Terminals) collectAssistantSession(ctx context.Context, at reached, tag model.AttemptEnvironmentProcessTag,
	from uint64, wait, settle time.Duration) (AssistantSessionOutput, error) {
	out := AssistantSessionOutput{Next: from}
	attached, err := at.machine.AttachProcess(ctx, tag, from)
	if err != nil {
		return out, terminalFailure(terminalError(err))
	}
	defer func() { _ = attached.Close() }()
	deadline := t.deps.Now().Add(wait)
	readUntil := deadline
	var data AssistantStream
	var terminal assistantTerminalText
	const outputLimit = model.AssistantCallResultMaximumBytes - (64 << 10)
	for {
		remaining := readUntil.Sub(t.deps.Now())
		if remaining <= 0 {
			break
		}
		readCtx, cancel := context.WithTimeout(ctx, remaining)
		event, readErr := attached.Next(readCtx)
		expired := errors.Is(readCtx.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if expired || errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return out, terminalFailure(terminalError(readErr))
		}
		switch event.Kind {
		case OutputData:
			data.add(terminal.append(event.Data, event.Offset), outputLimit)
			out.Next = max(out.Next, event.Offset+uint64(len(event.Data)))
			if settle > 0 && len(event.Data) != 0 {
				readUntil = deadline
				if quietUntil := t.deps.Now().Add(settle); quietUntil.Before(readUntil) {
					readUntil = quietUntil
				}
			}
		case OutputTruncated:
			terminal = assistantTerminalText{}
			data.add([]byte(assistantOutputSeparator), outputLimit)
			out.Next = max(out.Next, event.To)
			out.Dropped = true
		case OutputExit:
			out.Exit = &ProcessExit{Code: event.ExitCode, Signal: SignalName(event.ExitSignal)}
		}
		if out.Exit != nil {
			break
		}
	}
	out.Data = sessionStreamBytes(data)
	if out.Exit == nil {
		out.Next = terminal.checkpoint(out.Next)
	}
	out.Dropped = out.Dropped || data.Dropped
	return out, nil
}

// StopAssistantSession interrupts a running PTY and escalates if it does not exit.
func (t *Terminals) StopAssistantSession(ctx context.Context, inv invocation.Invocation, access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag, from uint64) (AssistantSessionOutput, error) {
	at, err := t.assistantSessionMachine(ctx, inv, access, tag)
	if err != nil {
		return AssistantSessionOutput{}, err
	}
	info, err := assistantSessionProcess(ctx, at, tag)
	if err != nil {
		return AssistantSessionOutput{}, err
	}
	signal := func(name ProcessSignal) error {
		signalErr := t.fenced(ctx, &at, func(fence Fence) error { return at.machine.SignalProcess(ctx, fence, tag, name) })
		if errors.Is(signalErr, ErrProcessExited) {
			return nil
		}
		return terminalFailure(terminalError(signalErr))
	}
	if info.Running {
		if err = signal(SignalInterrupt); err != nil {
			return AssistantSessionOutput{}, err
		}
	}
	out, err := t.collectAssistantSession(ctx, at, tag, from, 2*time.Second, 0)
	if err != nil || out.Exit != nil {
		return out, err
	}
	if err = signal(SignalKill); err != nil {
		return out, err
	}
	final, err := t.collectAssistantSession(ctx, at, tag, out.Next, 2*time.Second, 0)
	if err != nil {
		return out, err
	}
	if final.Exit == nil {
		return out, terminalFailure(ErrTerminalEnded)
	}
	out.Next, out.Exit = final.Next, final.Exit
	out.Data = append(out.Data, final.Data...)
	out.Dropped = out.Dropped || final.Dropped
	return out, nil
}

var assistantKeys = map[string]string{
	"enter": "\r", "tab": "\t", "escape": "\x1b", "backspace": "\x7f",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "page_up": "\x1b[5~", "page_down": "\x1b[6~",
	"ctrl_c": "\x03", "ctrl_d": "\x04", "ctrl_z": "\x1a", "ctrl_l": "\x0c",
}

// AssistantKeyBytes maps only server-defined key names to terminal bytes.
func AssistantKeyBytes(name string) ([]byte, bool) {
	value, ok := assistantKeys[name]
	if !ok {
		return nil, false
	}
	return []byte(value), true
}

// reconcileAssistantSessions is driven by the environment owner's confirmed
// fence, so it can end abandoned sessions after candidate access is gone.
func (o *owner) reconcileAssistantSessions(ctx context.Context, fence Fence, freezing bool) error {
	if o.deps.AssistantSessions == nil {
		return nil
	}
	access := store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: o.attemptID, Incarnation: o.row.Incarnation, Fence: o.access().Fence}
	sessions, err := o.deps.AssistantSessions.ListSessions(ctx, store.AssistantSessionQuery{EnvironmentOwner: &access})
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	at := reached{row: o.row, machine: o.machine}
	var joined error
	for _, session := range sessions {
		if session.State != "running" {
			continue
		}
		joined = errors.Join(joined, o.reconcileAssistantSession(ctx, at, access, fence, session, freezing))
	}
	return joined
}

func (o *owner) reconcileAssistantSession(ctx context.Context, at reached, access store.ExamAttemptWorkspaceEnvironmentAccess,
	fence Fence, session model.AssistantSession, stopping bool) error {
	tag := model.AttemptEnvironmentProcessTag(session.Tag)
	signal := func() error { return o.machine.SignalProcess(ctx, fence, tag, SignalKill) }
	now := o.deps.Now()
	status, err := checkAssistantSession(ctx, at, session, now, signal)
	if err != nil {
		return err
	}
	if status.Outcome == "" && (stopping || session.AccessEnded) {
		switch err = signal(); {
		case err == nil:
			status = AssistantSessionStatus{Outcome: "ended", Exit: &ProcessExit{Signal: "KILL"}}
		case errors.Is(err, ErrProcessExited), errors.Is(err, ErrMachineNotFound):
			status = AssistantSessionStatus{Outcome: "ended"}
		default:
			return err
		}
	}
	if status.Outcome == "" {
		return nil
	}
	endedAt := status.EndTime(session.StartedAt, o.deps.Now())
	observedAt := now
	if !endedAt.IsZero() {
		observedAt = endedAt
	}
	end := store.AssistantSessionEnd{EnvironmentOwner: &access, Tag: session.Tag, Outcome: status.Outcome, EndedAt: endedAt,
		DurationMilliseconds: max(0, observedAt.Sub(session.StartedAt).Milliseconds())}
	if status.Exit != nil {
		end.Signal = status.Exit.Signal
		if end.Signal == "" {
			end.ExitCode = &status.Exit.Code
		}
	}
	result, err := o.deps.AssistantSessions.EndSession(ctx, &end)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if result.Changed && o.deps.AssistantSessionEnded != nil {
		return o.deps.AssistantSessionEnded(ctx, o.row.SittingID, o.attemptID, session.CandidateUserID, result)
	}
	return nil
}

// ViewAssistantSession returns screen text only. The candidate never gets
// an input-capable attachment to an assistant-owned process.
func (t *Terminals) ViewAssistantSession(ctx context.Context, inv invocation.Invocation, access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag) ([]string, error) {
	at, err := t.assistantSessionMachine(ctx, inv, access, tag)
	if err != nil {
		return nil, err
	}
	if _, err = assistantSessionProcess(ctx, at, tag); err != nil {
		return nil, err
	}
	screen, err := at.machine.Screen(ctx, tag)
	if err != nil {
		return nil, terminalFailure(terminalError(err))
	}
	return append([]string{}, screen.Text...), nil
}
