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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
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

func follow(t *testing.T, ctx context.Context, session *environment.TerminalSession, from uint64) *output {
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

func newTerminals(t *testing.T, r *rig) *environment.Terminals {
	t.Helper()
	terminals, err := environment.NewTerminals(environment.TerminalsConfig{
		Environments: r.environments, Evidence: r.evidence, Hosts: r.hosts, InputFlush: 20 * time.Millisecond,
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
	terminals := newTerminals(t, r)
	candidate := environment.CandidateAttempt{AttemptID: r.attemptID, CandidateUserID: model.NewUserID()}

	opened, err := terminals.Open(ctx, candidate, environment.Window{})
	require.NoError(t, err)
	require.Equal(t, "terminal:1", opened.Tag)
	require.True(t, opened.Running)

	session, err := terminals.Attach(ctx, candidate, opened.Tag)
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
	require.Equal(t, "terminal:1", inputs[0].Tag)
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
	second, err := terminals.Open(ctx, candidate, environment.Window{Cols: 120, Rows: 40})
	require.NoError(t, err)
	require.Equal(t, "terminal:2", second.Tag)
	_, err = terminals.Open(ctx, candidate, environment.Window{})
	require.ErrorIs(t, err, environment.ErrTerminalLimit)
	require.NoError(t, terminals.Signal(ctx, candidate, second.Tag, environment.SignalInterrupt))
	require.Eventually(t, func() bool {
		listed, listErr := terminals.List(ctx, candidate)
		return listErr == nil && listed.Limit == 2 && len(listed.Terminals) == 2 && !listed.Terminals[1].Running
	}, eventually, tick)
	third, err := terminals.Open(ctx, candidate, environment.Window{})
	require.NoError(t, err)
	require.Equal(t, "terminal:3", third.Tag)
	_, err = terminals.Attach(ctx, candidate, "terminal:9")
	require.ErrorIs(t, err, environment.ErrTerminalNotFound)
	require.ErrorIs(t, terminals.Signal(ctx, candidate, "shell", environment.SignalInterrupt), environment.ErrTerminalNotFound)

	// A frozen environment opens nothing, and a session on it ends.
	reattached, err := terminals.Attach(ctx, candidate, opened.Tag)
	require.NoError(t, err)
	defer reattached.Close(ctx)
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	r.poke()
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)
	_, err = terminals.Open(ctx, candidate, environment.Window{})
	require.ErrorIs(t, err, environment.ErrEnvironmentNotRunning)
	_, err = reattached.Write(ctx, uint64(len(line)), []byte("x"))
	require.ErrorIs(t, err, environment.ErrEnvironmentNotRunning)
	r.environments.desire(model.AttemptEnvironmentDesiredEnded)
	r.poke()
	require.Eventually(t, func() bool {
		return errors.Is(reattached.Check(ctx), environment.ErrTerminalEnded)
	}, eventually, tick)
}

func TestTerminalsRefuseAnAttemptWithoutAnEnvironment(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	terminals := newTerminals(t, r)
	other := environment.CandidateAttempt{AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID()}
	_, err := terminals.Open(context.Background(), other, environment.Window{})
	require.ErrorIs(t, err, environment.ErrNoEnvironment)
	_, err = environment.NewTerminals(environment.TerminalsConfig{})
	require.Error(t, err)
}
