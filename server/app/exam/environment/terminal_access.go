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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CandidateAccess is what a candidate presents for every terminal call: the
// attempt, its open connection and that connection's continuity credential.
type CandidateAccess struct {
	AttemptID            model.ExamAttemptID
	ConnectionID         model.AttemptConnectionID
	ContinuityCredential string
}

// CandidateAttempt is an attempt whose candidate access is verified. Every
// terminal a candidate opens is the candidate's.
type CandidateAttempt struct {
	AttemptID       model.ExamAttemptID
	CandidateUserID model.UserID
	Generation      int64
}

// CandidateVerifier proves a candidate's live access to an attempt and
// writes nothing. A refusal carries its public code; exam.attempt.unavailable
// is transient.
type CandidateVerifier interface {
	VerifyCandidate(ctx context.Context, inv invocation.Invocation, access CandidateAccess) (CandidateAttempt, error)
}

// ErrTerminalAccessEnded reports that a session's access no longer holds:
// the attempt is sealed, the participation ended, or the machine ended.
var ErrTerminalAccessEnded = errors.New("terminal access ended")

// TerminalAttachment is one candidate session on a terminal. Write and
// Resize act as the terminal's author; TerminalCode names their refusals.
type TerminalAttachment interface {
	// Output reads the terminal's output from offset, replaying what its
	// history still holds.
	Output(ctx context.Context, from uint64) (ProcessAttachment, error)
	// Screen is the terminal's screen now, for a client with no screen of
	// its own: it draws Data, then reads output from Offset.
	Screen(ctx context.Context) (Screen, error)
	// Write writes input at offset and returns the terminal's input offset
	// after it. Bytes the terminal already took are dropped; an offset past
	// its input is an InputAheadError.
	Write(ctx context.Context, offset uint64, data []byte) (uint64, error)
	Resize(ctx context.Context, window Window) error
	// Check reruns the candidate's access check and the environment's,
	// and reports ErrTerminalAccessEnded once either no longer holds, so a
	// socket kept open ends. Any other error is transient.
	Check(ctx context.Context) error
	// Close stores the input the session recorded and not yet stored.
	Close(ctx context.Context)
}

// verify refuses every call on a nil Terminals as environment_unavailable.
func (t *Terminals) verify(ctx context.Context, inv invocation.Invocation, access CandidateAccess) (CandidateAttempt, error) {
	if t == nil {
		return CandidateAttempt{}, apperror.New("exam.attempt.environment_unavailable")
	}
	return t.deps.Candidates.VerifyCandidate(ctx, inv, access)
}

// GetEnvironment returns the candidate's execution environment: what its
// machine does and how far the workspace has reached it. A workspace entry
// whose last change is past ProjectionCursor is not in the machine yet.
func (t *Terminals) GetEnvironment(
	ctx context.Context,
	inv invocation.Invocation,
	access CandidateAccess,
) (model.AttemptEnvironment, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return model.AttemptEnvironment{}, err
	}
	environment, err := t.deps.Environments.Get(ctx, candidate.AttemptID)
	if store.IsNotFound(err) {
		return model.AttemptEnvironment{}, apperror.New("exam.attempt.environment_not_found")
	}
	if err != nil {
		return model.AttemptEnvironment{}, apperror.New("exam.attempt.environment_unavailable").Wrap(err)
	}
	return *environment, nil
}

// OpenTerminal opens the candidate's next terminal; a zero window takes
// DefaultTerminalWindow.
func (t *Terminals) OpenTerminal(
	ctx context.Context,
	inv invocation.Invocation,
	access CandidateAccess,
	window Window,
) (Terminal, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return Terminal{}, err
	}
	if window != (Window{}) && !window.Valid() {
		return Terminal{}, apperror.Invalid("exam.attempt.invalid", "window")
	}
	terminal, err := t.open(ctx, candidate, window)
	return terminal, terminalFailure(err)
}

// ListTerminals lists the candidate's terminals on the current machine.
func (t *Terminals) ListTerminals(ctx context.Context, inv invocation.Invocation, access CandidateAccess) (TerminalList, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return TerminalList{}, err
	}
	terminals, err := t.list(ctx, candidate)
	return terminals, terminalFailure(err)
}

// InterruptTerminal interrupts what a terminal runs.
func (t *Terminals) InterruptTerminal(
	ctx context.Context,
	inv invocation.Invocation,
	access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag,
) error {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return err
	}
	return terminalFailure(t.signal(ctx, candidate, tag, SignalInterrupt))
}

// AttachTerminal opens a session on one of the candidate's terminals. The
// caller closes it.
func (t *Terminals) AttachTerminal(
	ctx context.Context,
	inv invocation.Invocation,
	access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag,
) (TerminalAttachment, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return nil, err
	}
	session, _, err := t.attach(ctx, candidate, tag, true)
	if err != nil {
		return nil, terminalFailure(err)
	}
	session.inv, session.access = inv, access
	return session, nil
}

// ViewTerminal returns the current screen of one of the candidate's
// terminals as text, under the access checks of AttachTerminal. It reads
// through a session that is not the terminal's author, so it never types,
// resizes, signals or records input.
func (t *Terminals) ViewTerminal(
	ctx context.Context,
	inv invocation.Invocation,
	access CandidateAccess,
	tag model.AttemptEnvironmentProcessTag,
) (TerminalScreen, error) {
	candidate, err := t.verify(ctx, inv, access)
	if err != nil {
		return TerminalScreen{}, err
	}
	screen, err := t.view(ctx, candidate, tag)
	return screen, terminalFailure(err)
}

// Check reports ErrTerminalAccessEnded once the session must end. Any other
// error is transient and the session goes on.
func (s *terminalSession) Check(ctx context.Context) error {
	if err := s.ended(ctx); errors.Is(err, ErrTerminalEnded) {
		return ErrTerminalAccessEnded
	} else if err != nil {
		return err
	}
	_, err := s.terminals.verify(ctx, s.inv, s.access)
	if err != nil && !apperror.Is(err, "exam.attempt.unavailable") {
		return ErrTerminalAccessEnded
	}
	return err
}

// terminalFailure presents a terminal refusal with its public code.
func terminalFailure(err error) error {
	var ahead *InputAheadError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNoEnvironment):
		return apperror.New("exam.attempt.environment_not_found").Wrap(err)
	case errors.Is(err, ErrEnvironmentNotRunning), errors.Is(err, ErrTerminalEnded):
		return apperror.New("exam.attempt.environment_not_running").Wrap(err)
	case errors.Is(err, ErrTerminalLimit):
		return apperror.New("exam.attempt.terminal_limit_reached").Wrap(err)
	case errors.Is(err, ErrTerminalNotFound):
		return apperror.New("exam.attempt.terminal_not_found").Wrap(err)
	case errors.Is(err, ErrTerminalExited):
		return apperror.New("exam.attempt.terminal_exited").Wrap(err)
	case errors.As(err, &ahead), errors.Is(err, ErrTerminalNotAuthor):
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
