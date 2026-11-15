// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
)

// Terminals serves a candidate's terminals: the Attempt service proves the
// candidate's access, then the environment's terminals act on the machine.
// Every refusal carries its public code.
type Terminals struct {
	attempts     *attempt.Service
	terminals    *environment.Terminals
	environments store.AttemptEnvironmentStore
}

func newTerminals(deps Dependencies, attempts *attempt.Service) (*Terminals, error) {
	if deps.EnvironmentHosts == nil || deps.Store.AttemptEnvironment() == nil || attempts == nil {
		return nil, nil
	}
	terminals, err := environment.NewTerminals(environment.TerminalsConfig{
		Environments: deps.Store.AttemptEnvironment(),
		Evidence:     deps.Store.ExamAttemptWorkspace(),
		Hosts:        deps.EnvironmentHosts,
		Report:       deps.EnvironmentDiagnostics,
	})
	if err != nil {
		return nil, err
	}
	return &Terminals{attempts: attempts, terminals: terminals, environments: deps.Store.AttemptEnvironment()}, nil
}

// GetEnvironment returns the candidate's execution environment: what its
// machine does and how far the Workspace has reached it. A Workspace entry
// whose last change is past ProjectionCursor is not in the machine yet.
func (t *Terminals) GetEnvironment(
	ctx context.Context,
	inv invocation.Invocation,
	access attempt.CandidateAccess,
) (model.AttemptEnvironment, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return model.AttemptEnvironment{}, err
	}
	environment, err := t.environments.Get(ctx, candidate.AttemptID)
	if store.IsNotFound(err) {
		return model.AttemptEnvironment{}, apperror.New("exam.attempt.environment_not_found")
	}
	if err != nil {
		return model.AttemptEnvironment{}, apperror.New("exam.attempt.environment_unavailable").Wrap(err)
	}
	return *environment, nil
}

// verify proves the candidate's live access to the Attempt.
func (t *Terminals) verify(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess) (environment.CandidateAttempt, error) {
	// An installation that runs no execution environments has no
	// terminals: a nil Terminals answers every call so.
	if t == nil {
		return environment.CandidateAttempt{}, apperror.New("exam.attempt.environment_unavailable")
	}
	verified, err := t.attempts.VerifyCandidateAttemptAccess(ctx, inv, access)
	if err != nil {
		return environment.CandidateAttempt{}, err
	}
	return environment.CandidateAttempt{AttemptID: verified.AttemptID, CandidateUserID: verified.CandidateUserID}, nil
}

// OpenTerminal opens the candidate's next terminal.
func (t *Terminals) OpenTerminal(
	ctx context.Context,
	inv invocation.Invocation,
	access attempt.CandidateAccess,
	window environment.Window,
) (environment.Terminal, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return environment.Terminal{}, err
	}
	if window != (environment.Window{}) && !window.Valid() {
		return environment.Terminal{}, apperror.Invalid("exam.attempt.invalid", "window")
	}
	terminal, err := t.terminals.Open(ctx, candidate, window)
	return terminal, terminalFailure(err)
}

// ListTerminals lists the candidate's terminals on the current machine.
func (t *Terminals) ListTerminals(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess) (environment.TerminalList, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return environment.TerminalList{}, err
	}
	terminals, err := t.terminals.List(ctx, candidate)
	return terminals, terminalFailure(err)
}

// InterruptTerminal interrupts what a terminal runs.
func (t *Terminals) InterruptTerminal(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess, tag string) error {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return err
	}
	return terminalFailure(t.terminals.Signal(ctx, candidate, tag, environment.SignalInterrupt))
}

// TerminalAttachment is one candidate session on a terminal. Check reruns
// the candidate's access check and the environment's, so a socket kept open
// ends once either no longer holds.
type TerminalAttachment struct {
	*environment.TerminalSession
	terminals *Terminals
	inv       invocation.Invocation
	access    attempt.CandidateAccess
}

// AttachTerminal opens a session on one of the candidate's terminals.
func (t *Terminals) AttachTerminal(
	ctx context.Context,
	inv invocation.Invocation,
	access attempt.CandidateAccess,
	tag string,
) (*TerminalAttachment, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return nil, err
	}
	session, err := t.terminals.Attach(ctx, candidate, tag)
	if err != nil {
		return nil, terminalFailure(err)
	}
	return &TerminalAttachment{TerminalSession: session, terminals: t, inv: inv, access: access}, nil
}

// ErrTerminalAccessEnded reports that a session's access no longer holds:
// the Attempt is sealed, the Participation ended, or the machine ended.
var ErrTerminalAccessEnded = errors.New("terminal access ended")

// Check reports ErrTerminalAccessEnded once the session must end. Any other
// error is transient and the session goes on.
func (a *TerminalAttachment) Check(ctx context.Context) error {
	if err := a.TerminalSession.Check(ctx); errors.Is(err, environment.ErrTerminalEnded) {
		return ErrTerminalAccessEnded
	} else if err != nil {
		return err
	}
	_, err := a.terminals.verify(ctx, a.inv, a.access)
	if err != nil && !apperror.Is(err, "exam.attempt.unavailable") {
		return ErrTerminalAccessEnded
	}
	return err
}

// terminalFailure presents a terminal refusal with its public code.
func terminalFailure(err error) error {
	var ahead *environment.InputAheadError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, environment.ErrNoEnvironment):
		return apperror.New("exam.attempt.environment_not_found").Wrap(err)
	case errors.Is(err, environment.ErrEnvironmentNotRunning), errors.Is(err, environment.ErrTerminalEnded):
		return apperror.New("exam.attempt.environment_not_running").Wrap(err)
	case errors.Is(err, environment.ErrTerminalLimit):
		return apperror.New("exam.attempt.terminal_limit_reached").Wrap(err)
	case errors.Is(err, environment.ErrTerminalNotFound):
		return apperror.New("exam.attempt.terminal_not_found").Wrap(err)
	case errors.Is(err, environment.ErrTerminalExited):
		return apperror.New("exam.attempt.terminal_exited").Wrap(err)
	case errors.As(err, &ahead), errors.Is(err, environment.ErrTerminalNotAuthor):
		return apperror.Invalid("exam.attempt.invalid", "terminal_input").Wrap(err)
	case errors.Is(err, context.Canceled):
		return err
	}
	return apperror.New("exam.attempt.environment_unavailable").Wrap(err)
}

// TerminalCode is the public code of a failure of an attached session, for
// a transport that reports it inside the session.
func TerminalCode(err error) string {
	return apperror.CodeOf(terminalFailure(err), "exam.attempt.environment_unavailable")
}
