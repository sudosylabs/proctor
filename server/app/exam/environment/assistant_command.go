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
)

// AssistantTagPrefix starts the tag of every process the assistant runs. The
// assistant never types into a candidate's terminal: its commands run apart,
// so the record always shows who ran what, and harvest attributes the files
// they change to the tag.
const AssistantTagPrefix = "assistant:"

// MaxAssistantCommandTimeout is the longest an assistant command may run.
const MaxAssistantCommandTimeout = 10 * time.Minute

// AssistantCommand is one non-interactive command: no terminal, input closed,
// run from the workspace root. OutputLimit bounds what is kept of the head
// and of the tail of each stream.
type AssistantCommand struct {
	Tag         model.AttemptEnvironmentProcessTag
	Argv        []string
	Timeout     time.Duration
	OutputLimit int
}

// AssistantStream is what a command wrote to one stream: its first and last
// OutputLimit bytes, and whether anything between them was dropped.
type AssistantStream struct {
	Head    []byte
	Tail    []byte
	Bytes   uint64
	Dropped bool
}

// AssistantCommandResult is how an assistant command ended. TimedOut means
// the host ended it at its timeout, or the turn was stopped first.
type AssistantCommandResult struct {
	Stdout   AssistantStream
	Stderr   AssistantStream
	Exit     *ProcessExit
	TimedOut bool
	Duration time.Duration
}

// ErrAssistantCommandInvalid refuses a command the assistant may not run.
var ErrAssistantCommandInvalid = errors.New("the assistant command is invalid")

// RunAssistantCommand runs one command for the assistant under the
// candidate's live access and waits for it to end. Canceling ctx kills it.
func (t *Terminals) RunAssistantCommand(
	ctx context.Context,
	inv invocation.Invocation,
	access CandidateAccess,
	command AssistantCommand,
) (AssistantCommandResult, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return AssistantCommandResult{}, err
	}
	if !command.Tag.IsValid() || !strings.HasPrefix(string(command.Tag), AssistantTagPrefix) || len(command.Argv) == 0 ||
		command.Timeout <= 0 || command.Timeout > MaxAssistantCommandTimeout || command.OutputLimit < 1 {
		return AssistantCommandResult{}, ErrAssistantCommandInvalid
	}
	at, err := t.reach(ctx, candidate.AttemptID)
	if err != nil {
		return AssistantCommandResult{}, terminalFailure(err)
	}
	started := t.deps.Now()
	err = t.fenced(ctx, &at, func(fence Fence) error {
		_, startErr := at.machine.StartProcess(ctx, fence, ProcessSpec{Tag: command.Tag, Argv: command.Argv,
			Streams: ProcessStreamsPipes, Timeout: command.Timeout})
		return startErr
	})
	if errors.Is(err, ErrMachineBusy) {
		return AssistantCommandResult{}, terminalFailure(ErrTerminalLimit)
	}
	if err != nil {
		return AssistantCommandResult{}, terminalFailure(terminalError(err))
	}
	attachment, err := at.machine.AttachProcess(ctx, command.Tag, 0)
	if err != nil {
		t.killAssistantCommand(at, command.Tag)
		return AssistantCommandResult{}, terminalFailure(terminalError(err))
	}
	defer func() { _ = attachment.Close() }()
	result := AssistantCommandResult{}
	for {
		output, nextErr := attachment.Next(ctx)
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			if ctx.Err() != nil {
				t.killAssistantCommand(at, command.Tag)
				result.TimedOut = true
				result.Duration = t.deps.Now().Sub(started)
				return result, nil
			}
			t.killAssistantCommand(at, command.Tag)
			return result, terminalFailure(terminalError(nextErr))
		}
		switch output.Kind {
		case OutputData:
			stream := &result.Stdout
			if output.Stream == ProcessStreamStderr {
				stream = &result.Stderr
			}
			stream.add(output.Data, command.OutputLimit)
		case OutputTruncated:
			stream := &result.Stdout
			if output.Stream == ProcessStreamStderr {
				stream = &result.Stderr
			}
			stream.Dropped = true
			stream.Bytes += output.To - output.Offset
		case OutputExit:
			result.Exit = &ProcessExit{Code: output.ExitCode, Signal: SignalName(output.ExitSignal)}
		}
		if output.Kind == OutputExit {
			break
		}
	}
	if result.Exit == nil {
		t.killAssistantCommand(at, command.Tag)
		return result, terminalFailure(ErrTerminalEnded)
	}
	result.Duration = t.deps.Now().Sub(started)
	// The host ends a timed-out command with TERM, then KILL, but a program
	// that traps TERM exits with its own code, so the elapsed time decides.
	result.TimedOut = result.Exit != nil && result.Duration >= command.Timeout
	return result, nil
}

// killAssistantCommand outlives a canceled turn: the process must not run on
// after the assistant stopped waiting for it.
func (t *Terminals) killAssistantCommand(at reached, tag model.AttemptEnvironmentProcessTag) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.fenced(ctx, &at, func(fence Fence) error { return at.machine.SignalProcess(ctx, fence, tag, SignalKill) })
}

func (s *AssistantStream) add(data []byte, limit int) {
	s.Bytes += uint64(len(data))
	if room := limit - len(s.Head); room > 0 {
		take := min(room, len(data))
		s.Head = append(s.Head, data[:take]...)
		data = data[take:]
	}
	if len(data) == 0 {
		return
	}
	s.Tail = append(s.Tail, data...)
	if len(s.Tail) > limit {
		s.Dropped = true
		s.Tail = append(s.Tail[:0:0], s.Tail[len(s.Tail)-limit:]...)
	}
}
