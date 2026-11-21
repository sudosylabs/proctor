// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// output gathers a terminal's output from one attachment.
type output struct {
	mu     sync.Mutex
	first  uint64
	seen   bool
	data   []byte
	exited bool
	done   chan struct{}
}

func follow(t *testing.T, ctx context.Context, session environment.TerminalAttachment, from uint64) *output {
	t.Helper()
	attachment, err := session.Output(ctx, from)
	require.NoError(t, err)
	gathered := &output{done: make(chan struct{})}
	go func() {
		defer close(gathered.done)
		defer func() { _ = attachment.Close() }()
		for {
			event, err := attachment.Next(ctx)
			if err != nil {
				return
			}
			gathered.mu.Lock()
			switch event.Kind {
			case environment.OutputData:
				if !gathered.seen {
					gathered.first, gathered.seen = event.Offset, true
				}
				gathered.data = append(gathered.data, event.Data...)
			case environment.OutputExit:
				gathered.exited = true
			}
			gathered.mu.Unlock()
		}
	}()
	return gathered
}

func (o *output) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.data)
}

func (o *output) start() (uint64, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.first, o.seen
}

// candidatesFake verifies every candidate as the Attempt's, until refuse
// names the code it answers with instead.
type candidatesFake struct {
	mu     sync.Mutex
	userID model.UserID
	refuse string
}

func (c *candidatesFake) VerifyCandidate(
	_ context.Context,
	_ invocation.Invocation,
	access environment.CandidateAccess,
) (environment.CandidateAttempt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refuse != "" {
		return environment.CandidateAttempt{}, apperror.New(apperror.Code(c.refuse))
	}
	return environment.CandidateAttempt{AttemptID: access.AttemptID, CandidateUserID: c.userID}, nil
}

func (c *candidatesFake) refuseWith(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refuse = code
}

func newTerminals(t *testing.T, r *rig, candidates environment.CandidateVerifier) *environment.Terminals {
	t.Helper()
	terminals, err := environment.NewTerminals(environment.TerminalsDependencies{
		Environments: r.environments, Evidence: r.evidence, Hosts: r.hosts, Candidates: candidates,
		InputFlush: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	return terminals
}

func TestTerminalsOverAnExecutionHost(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.environments.mu.Lock()
	r.environments.row.RecordInput = true
	r.environments.mu.Unlock()
	r.running()
	ctx := t.Context()
	candidates := &candidatesFake{userID: model.NewUserID()}
	terminals := newTerminals(t, r, candidates)
	var inv invocation.Invocation
	access := environment.CandidateAccess{AttemptID: r.attemptID, ConnectionID: model.NewAttemptConnectionID()}

	opened, err := terminals.OpenTerminal(ctx, inv, access, environment.Window{})
	require.NoError(t, err)
	require.Equal(t, model.AttemptEnvironmentProcessTag("terminal:1"), opened.Tag)
	require.True(t, opened.Running)

	session, err := terminals.AttachTerminal(ctx, inv, access, opened.Tag)
	require.NoError(t, err)
	gathered := follow(t, ctx, session, 0)

	// Input written at an offset twice types once.
	line := []byte("echo hi\n")
	next, err := session.Write(ctx, 0, line)
	require.NoError(t, err)
	require.EqualValues(t, len(line), next)
	next, err = session.Write(ctx, 0, line)
	require.NoError(t, err)
	require.EqualValues(t, len(line), next, "a resend is dropped")
	_, err = session.Write(ctx, 100, []byte("x"))
	var ahead *environment.InputAheadError
	require.ErrorAs(t, err, &ahead)
	require.EqualValues(t, len(line), ahead.Input)
	require.Eventually(t, func() bool { return strings.Contains(gathered.text(), "hi\r\n") }, eventually, tick)
	require.Equal(t, 1, strings.Count(gathered.text(), "echo hi"), "the command ran once")
	start, seen := gathered.start()
	require.True(t, seen)
	require.Zero(t, start)

	// A reattach from a later offset continues there.
	later := uint64(len(gathered.text())) - 4
	resumed := follow(t, ctx, session, later)
	require.Eventually(t, func() bool {
		first, ok := resumed.start()
		return ok && first == later && resumed.text() == gathered.text()[later:]
	}, eventually, tick)

	// A client without a screen of its own draws the screen.
	screen, err := session.Screen(ctx)
	require.NoError(t, err)
	require.Contains(t, string(screen.Data), "hi")
	require.EqualValues(t, len(gathered.text()), screen.Offset)
	require.Equal(t, environment.DefaultTerminalWindow, screen.Window)
	require.NoError(t, session.Resize(ctx, environment.Window{Cols: 100, Rows: 30}))

	// The command log names the terminal each command ran in.
	require.Eventually(t, func() bool {
		for _, detail := range r.evidence.details(model.AttemptEvidenceProcess) {
			events, _ := detail["events"].([]any)
			for _, raw := range events {
				event, _ := raw.(map[string]any)
				argv, _ := event["argv"].([]any)
				if event["kind"] == "started" && len(argv) == 2 && argv[0] == "echo" {
					return event["origin"] == "terminal:1" && event["tag"] == nil
				}
			}
		}
		return false
	}, eventually, tick, "the child process is attributed to terminal:1 and keeps its own empty tag")

	// The candidate's input is recorded, once, numbered by its offset.
	session.Close(ctx)
	inputs := r.evidence.terminalInputs()
	require.Len(t, inputs, 1)
	require.Equal(t, model.AttemptEnvironmentProcessTag("terminal:1"), inputs[0].Tag)
	require.EqualValues(t, 1, inputs[0].Sequence)
	var detail struct {
		Offset uint64 `json:"offset"`
		Events []struct {
			Input string `json:"input"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(inputs[0].Detail, &detail))
	var recorded []byte
	for _, event := range detail.Events {
		decoded, decodeErr := base64.StdEncoding.DecodeString(event.Input)
		require.NoError(t, decodeErr)
		recorded = append(recorded, decoded...)
	}
	require.Equal(t, string(line), string(recorded), "input is recorded, output never")

	// The policy's limit bounds running terminals; tags are never reused.
	second, err := terminals.OpenTerminal(ctx, inv, access, environment.Window{Cols: 120, Rows: 40})
	require.NoError(t, err)
	require.Equal(t, model.AttemptEnvironmentProcessTag("terminal:2"), second.Tag)
	_, err = terminals.OpenTerminal(ctx, inv, access, environment.Window{})
	require.ErrorIs(t, err, environment.ErrTerminalLimit)
	require.True(t, apperror.Is(err, "exam.attempt.terminal_limit_reached"))
	require.NoError(t, terminals.InterruptTerminal(ctx, inv, access, second.Tag))
	require.Eventually(t, func() bool {
		listed, listErr := terminals.ListTerminals(ctx, inv, access)
		return listErr == nil && listed.Limit == 2 && len(listed.Terminals) == 2 && !listed.Terminals[1].Running
	}, eventually, tick)
	third, err := terminals.OpenTerminal(ctx, inv, access, environment.Window{})
	require.NoError(t, err)
	require.Equal(t, model.AttemptEnvironmentProcessTag("terminal:3"), third.Tag)
	_, err = terminals.AttachTerminal(ctx, inv, access, "terminal:9")
	require.ErrorIs(t, err, environment.ErrTerminalNotFound)
	require.ErrorIs(t, terminals.InterruptTerminal(ctx, inv, access, "shell"), environment.ErrTerminalNotFound)

	// A session whose candidate access lapses for a moment goes on; one
	// whose access is refused ends.
	reattached, err := terminals.AttachTerminal(ctx, inv, access, opened.Tag)
	require.NoError(t, err)
	defer reattached.Close(ctx)
	candidates.refuseWith("exam.attempt.unavailable")
	require.True(t, apperror.Is(reattached.Check(ctx), "exam.attempt.unavailable"))
	candidates.refuseWith("exam.attempt.invalid")
	require.ErrorIs(t, reattached.Check(ctx), environment.ErrTerminalAccessEnded)
	candidates.refuseWith("")
	require.NoError(t, reattached.Check(ctx))

	// A frozen environment opens nothing, and a session on it ends.
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	r.poke()
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)
	_, err = terminals.OpenTerminal(ctx, inv, access, environment.Window{})
	require.ErrorIs(t, err, environment.ErrEnvironmentNotRunning)
	_, err = reattached.Write(ctx, uint64(len(line)), []byte("x"))
	require.ErrorIs(t, err, environment.ErrEnvironmentNotRunning)
	r.environments.desire(model.AttemptEnvironmentDesiredEnded)
	r.poke()
	require.Eventually(t, func() bool {
		return errors.Is(reattached.Check(ctx), environment.ErrTerminalAccessEnded)
	}, eventually, tick)
}

func TestViewTerminalReadsTheScreenAndWritesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.environments.mu.Lock()
	r.environments.row.RecordInput = true
	r.environments.mu.Unlock()
	r.running()
	ctx := t.Context()
	candidates := &candidatesFake{userID: model.NewUserID()}
	terminals := newTerminals(t, r, candidates)
	var inv invocation.Invocation
	access := environment.CandidateAccess{AttemptID: r.attemptID, ConnectionID: model.NewAttemptConnectionID()}

	opened, err := terminals.OpenTerminal(ctx, inv, access, environment.Window{})
	require.NoError(t, err)
	session, err := terminals.AttachTerminal(ctx, inv, access, opened.Tag)
	require.NoError(t, err)
	line := []byte("echo hi\n")
	_, err = session.Write(ctx, 0, line)
	require.NoError(t, err)
	session.Close(ctx)

	var screen environment.TerminalScreen
	require.Eventually(t, func() bool {
		screen, err = terminals.ViewTerminal(ctx, inv, access, opened.Tag)
		return err == nil && slices.Contains(screen.Text, "hi")
	}, eventually, tick)
	require.Equal(t, environment.DefaultTerminalWindow.Cols, screen.Cols)
	require.Equal(t, environment.DefaultTerminalWindow.Rows, screen.Rows)
	require.True(t, screen.Running)
	require.Nil(t, screen.Exit)
	for range 3 {
		_, err = terminals.ViewTerminal(ctx, inv, access, opened.Tag)
		require.NoError(t, err)
	}

	// Viewing typed nothing, resized nothing and recorded nothing.
	listed, err := terminals.ListTerminals(ctx, inv, access)
	require.NoError(t, err)
	require.Len(t, listed.Terminals, 1)
	require.EqualValues(t, len(line), listed.Terminals[0].Input, "only the author's input reached the terminal")
	require.True(t, listed.Terminals[0].Running)
	require.Len(t, r.evidence.terminalInputs(), 1, "only the author's session recorded input")
	again, err := terminals.ViewTerminal(ctx, inv, access, opened.Tag)
	require.NoError(t, err)
	require.Equal(t, screen.Text, again.Text, "the screen is as the author left it")

	// The same refusals as an attach.
	_, err = terminals.ViewTerminal(ctx, inv, access, "terminal:9")
	require.ErrorIs(t, err, environment.ErrTerminalNotFound)
	require.True(t, apperror.Is(err, "exam.attempt.terminal_not_found"))
	_, err = terminals.ViewTerminal(ctx, inv, access, "assistant:1")
	require.ErrorIs(t, err, environment.ErrTerminalNotFound, "only a candidate's terminal is viewed")
	candidates.refuseWith("exam.attempt.invalid")
	_, err = terminals.ViewTerminal(ctx, inv, access, opened.Tag)
	require.True(t, apperror.Is(err, "exam.attempt.invalid"), "a refused candidate views nothing")
	candidates.refuseWith("")
	other := environment.CandidateAccess{AttemptID: model.NewExamAttemptID(), ConnectionID: model.NewAttemptConnectionID()}
	_, err = terminals.ViewTerminal(ctx, inv, other, opened.Tag)
	require.ErrorIs(t, err, environment.ErrNoEnvironment)
	var none *environment.Terminals
	_, err = none.ViewTerminal(ctx, inv, access, opened.Tag)
	require.True(t, apperror.Is(err, "exam.attempt.environment_unavailable"))

	// An exited terminal still shows its last screen, and how it ended.
	require.NoError(t, terminals.InterruptTerminal(ctx, inv, access, opened.Tag))
	require.Eventually(t, func() bool {
		screen, err = terminals.ViewTerminal(ctx, inv, access, opened.Tag)
		return err == nil && !screen.Running
	}, eventually, tick)
	require.Equal(t, &environment.ProcessExit{Signal: "INT"}, screen.Exit)
	require.Contains(t, screen.Text, "hi")

	// A frozen environment is not viewed.
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	r.poke()
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)
	_, err = terminals.ViewTerminal(ctx, inv, access, opened.Tag)
	require.ErrorIs(t, err, environment.ErrEnvironmentNotRunning)
}

func TestTerminalsRefuseAnAttemptWithoutAnEnvironment(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	terminals := newTerminals(t, r, &candidatesFake{userID: model.NewUserID()})
	other := environment.CandidateAccess{AttemptID: model.NewExamAttemptID(), ConnectionID: model.NewAttemptConnectionID()}
	_, err := terminals.OpenTerminal(context.Background(), invocation.Invocation{}, other, environment.Window{})
	require.ErrorIs(t, err, environment.ErrNoEnvironment)
	require.True(t, apperror.Is(err, "exam.attempt.environment_not_found"))
	_, err = environment.NewTerminals(environment.TerminalsDependencies{})
	require.Error(t, err)
}

func TestNilTerminalsAnswerUnavailable(t *testing.T) {
	t.Parallel()
	var terminals *environment.Terminals
	_, err := terminals.ListTerminals(context.Background(), invocation.Invocation{}, environment.CandidateAccess{})
	require.True(t, apperror.Is(err, "exam.attempt.environment_unavailable"), "an installation without execution: %v", err)
}
