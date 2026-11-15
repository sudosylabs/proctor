// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TerminalTagPrefix starts the tag of every terminal a candidate opens:
// terminal:1, terminal:2 and so on. execenv's tag alphabet includes ':'.
const TerminalTagPrefix = "terminal:"

// MaxTerminalOpens bounds how many terminals one machine opens over its
// life. Tags are never reused on a machine, so a terminal's input offsets,
// and the input evidence numbered by them, belong to exactly one process.
const MaxTerminalOpens = 64

// DefaultTerminalWindow is a new terminal's size when the client names none.
var DefaultTerminalWindow = Window{Cols: 80, Rows: 24}

// The refusals of the terminal use cases.
var (
	// ErrNoEnvironment: the Attempt's Revision does not enable execution.
	ErrNoEnvironment = errors.New("the Attempt has no execution environment")
	// ErrEnvironmentNotRunning: the environment has no running machine
	// this node can reach now.
	ErrEnvironmentNotRunning = errors.New("the execution environment is not running")
	// ErrTerminalLimit: the candidate has as many terminals open as the
	// policy allows, or the machine opened as many as it may.
	ErrTerminalLimit = errors.New("the terminal limit is reached")
	// ErrTerminalNotFound: no terminal has that tag on the current machine.
	ErrTerminalNotFound = errors.New("no such terminal")
	// ErrTerminalExited: the terminal has exited.
	ErrTerminalExited = errors.New("the terminal has exited")
	// ErrTerminalNotAuthor: the caller is not the terminal's author and may
	// only read it.
	ErrTerminalNotAuthor = errors.New("only the terminal's author writes to it")
	// ErrTerminalEnded: the machine the terminal ran on has ended or been
	// replaced; nothing more comes from it.
	ErrTerminalEnded = errors.New("the terminal's machine has ended")
)

// InputAheadError refuses input written past what the terminal has taken.
// Input is where the terminal's input stands.
type InputAheadError struct{ Input uint64 }

func (e *InputAheadError) Error() string {
	return "terminal input offset is past the terminal's input at " + strconv.FormatUint(e.Input, 10)
}

// CandidateAttempt is an Attempt whose candidate access the caller has
// already verified. Every terminal a candidate opens is the candidate's.
type CandidateAttempt struct {
	AttemptID       model.ExamAttemptID
	CandidateUserID model.UserID
}

// IsTerminalTag reports whether tag names a terminal a candidate opened.
func IsTerminalTag(tag string) bool {
	_, ok := terminalNumber(tag)
	return ok
}

func terminalNumber(tag string) (int, bool) {
	digits, ok := strings.CutPrefix(tag, TerminalTagPrefix)
	if !ok || digits == "" || digits[0] == '0' {
		return 0, false
	}
	number, err := strconv.Atoi(digits)
	if err != nil || number < 1 || number > MaxTerminalOpens || strconv.Itoa(number) != digits {
		return 0, false
	}
	return number, true
}

// TerminalTag is the tag of the terminal numbered n.
func TerminalTag(n int) string { return TerminalTagPrefix + strconv.Itoa(n) }

// Terminal describes one terminal on the environment's current machine.
// Output is how many bytes it has written and Input how many it has taken.
type Terminal struct {
	Tag        string
	Running    bool
	Started    time.Time
	ExitCode   int
	ExitSignal int
	Output     uint64
	Input      uint64
}

func terminalOf(info ProcessInfo) Terminal {
	return Terminal{Tag: info.Tag, Running: info.Running, Started: info.Started, ExitCode: info.ExitCode,
		ExitSignal: info.ExitSignal, Output: info.Output, Input: info.Input}
}

// TerminalsConfig is what NewTerminals needs.
type TerminalsConfig struct {
	Environments store.AttemptEnvironmentStore
	// Evidence stores recorded terminal input.
	Evidence store.ExamAttemptWorkspaceStore
	Hosts    Hosts
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// Report receives failures a terminal survives, such as input that
	// could not be recorded. Nil drops them.
	Report func(attemptID model.ExamAttemptID, err error)
	// InputFlush and InputChunkBytes bound one input record: what arrived
	// within InputFlush, up to InputChunkBytes. Zero takes the defaults.
	InputFlush      time.Duration
	InputChunkBytes int
}

// The input recording's default bounds.
const (
	DefaultInputFlush      = time.Second
	DefaultInputChunkBytes = 16 << 10
)

// Terminals serves candidates' terminals from any node. It holds no lease:
// it reads the environment row, reaches the machine the row names itself,
// and writes under the row's current control fence. Terminal bytes never
// travel over cluster peer delivery.
type Terminals struct {
	config TerminalsConfig
}

// NewTerminals validates the configuration.
func NewTerminals(config TerminalsConfig) (*Terminals, error) {
	if config.Environments == nil || config.Evidence == nil || config.Hosts == nil {
		return nil, errors.New("terminals need the environments, evidence and hosts")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.InputFlush <= 0 {
		config.InputFlush = DefaultInputFlush
	}
	if config.InputChunkBytes <= 0 {
		config.InputChunkBytes = DefaultInputChunkBytes
	}
	return &Terminals{config: config}, nil
}

// reached is the machine an environment row names, as this node reached it.
type reached struct {
	row     *model.AttemptEnvironment
	machine Machine
}

func (r reached) fence() Fence {
	return Fence{GrantID: r.row.GrantID, Epoch: r.row.Epoch, Revision: r.row.ControlFence}
}

// reach reads the environment row and reaches the running machine it
// names, ensuring exactly the spec the owner ensures. A machine under
// another epoch is not the row's: the one the row names is gone, and the
// one Ensure just placed belongs to nobody, so it is given back.
func (t *Terminals) reach(ctx context.Context, attemptID model.ExamAttemptID) (reached, error) {
	row, err := t.config.Environments.Get(ctx, attemptID)
	if store.IsNotFound(err) {
		return reached{}, ErrNoEnvironment
	}
	if err != nil {
		return reached{}, fmt.Errorf("read the attempt environment: %w", err)
	}
	if row.Desired != model.AttemptEnvironmentDesiredRunning || row.State != model.AttemptEnvironmentRunning ||
		row.GrantID == "" {
		return reached{}, ErrEnvironmentNotRunning
	}
	machine, err := t.config.Hosts.Ensure(ctx, row.Host, machineSpec(row, row.GrantID))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return reached{}, err
		}
		return reached{}, fmt.Errorf("%w: %w", ErrEnvironmentNotRunning, err)
	}
	if machine.Epoch() != row.Epoch {
		_ = machine.Revoke(ctx)
		return reached{}, ErrEnvironmentNotRunning
	}
	return reached{row: row, machine: machine}, nil
}

// fenced runs a fenced call under the row's control fence. A conflict may
// mean the owner moved the fence since the row was read, so the row is read
// once more and the call repeated under its fence while it still names the
// same machine.
func (t *Terminals) fenced(ctx context.Context, at *reached, call func(Fence) error) error {
	err := call(at.fence())
	if !errors.Is(err, ErrMachineConflict) {
		return err
	}
	row, readErr := t.config.Environments.Get(ctx, at.row.AttemptID)
	if readErr != nil {
		return err
	}
	if row.GrantID != at.row.GrantID || row.Epoch != at.row.Epoch || row.State == model.AttemptEnvironmentEnded {
		return ErrTerminalEnded
	}
	if row.State != model.AttemptEnvironmentRunning || row.Desired != model.AttemptEnvironmentDesiredRunning {
		return ErrEnvironmentNotRunning
	}
	if row.ControlFence == at.row.ControlFence {
		return err
	}
	at.row = row
	return call(at.fence())
}

// terminalError turns a machine failure into a terminal refusal.
func terminalError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrMachineNotFound):
		return ErrTerminalNotFound
	case errors.Is(err, ErrProcessExited):
		return ErrTerminalExited
	case errors.Is(err, ErrMachineRevoked), errors.Is(err, ErrMachineLost):
		return ErrTerminalEnded
	case errors.Is(err, ErrMachineFrozen), errors.Is(err, ErrHostUnavailable):
		return fmt.Errorf("%w: %w", ErrEnvironmentNotRunning, err)
	}
	return err
}

// terminals lists the candidate's terminals on a machine.
func terminals(ctx context.Context, machine Machine) ([]ProcessInfo, error) {
	processes, err := machine.ListProcesses(ctx)
	if err != nil {
		return nil, terminalError(err)
	}
	var result []ProcessInfo
	for _, info := range processes {
		if IsTerminalTag(info.Tag) {
			result = append(result, info)
		}
	}
	slices.SortFunc(result, func(a, b ProcessInfo) int {
		left, _ := terminalNumber(a.Tag)
		right, _ := terminalNumber(b.Tag)
		return cmp.Compare(left, right)
	})
	return result, nil
}

// Open starts a terminal running the image's login shell and answers with
// its tag: the number after the highest the machine has opened. It refuses
// once the candidate has the policy's number of terminals running, and when
// the environment is not running.
func (t *Terminals) Open(ctx context.Context, candidate CandidateAttempt, window Window) (Terminal, error) {
	if window == (Window{}) {
		window = DefaultTerminalWindow
	}
	if !window.Valid() {
		return Terminal{}, errors.New("the terminal window is out of bounds")
	}
	at, err := t.reach(ctx, candidate.AttemptID)
	if err != nil {
		return Terminal{}, err
	}
	for range 4 {
		open, err := terminals(ctx, at.machine)
		if err != nil {
			return Terminal{}, err
		}
		running, highest := 0, 0
		for _, info := range open {
			if info.Running {
				running++
			}
			number, _ := terminalNumber(info.Tag)
			highest = max(highest, number)
		}
		if running >= at.row.Terminals || highest >= MaxTerminalOpens {
			return Terminal{}, ErrTerminalLimit
		}
		var started ProcessInfo
		err = t.fenced(ctx, &at, func(fence Fence) error {
			var startErr error
			started, startErr = at.machine.StartProcess(ctx, fence, ProcessSpec{Tag: TerminalTag(highest + 1), PTY: &window})
			return startErr
		})
		switch {
		case err == nil:
			return terminalOf(started), nil
		case errors.Is(err, ErrMachineConflict):
			// Another request took the tag: count again.
			continue
		case errors.Is(err, ErrMachineBusy):
			return Terminal{}, ErrTerminalLimit
		default:
			return Terminal{}, terminalError(err)
		}
	}
	return Terminal{}, ErrTerminalLimit
}

// TerminalList is the candidate's terminals on the current machine, running
// or exited, in tag order, and how many may run at once.
type TerminalList struct {
	Terminals []Terminal
	Limit     int
}

// List returns the candidate's terminals on the current machine.
func (t *Terminals) List(ctx context.Context, candidate CandidateAttempt) (TerminalList, error) {
	at, err := t.reach(ctx, candidate.AttemptID)
	if err != nil {
		return TerminalList{}, err
	}
	open, err := terminals(ctx, at.machine)
	if err != nil {
		return TerminalList{}, err
	}
	result := TerminalList{Terminals: make([]Terminal, len(open)), Limit: at.row.Terminals}
	for index, info := range open {
		result.Terminals[index] = terminalOf(info)
	}
	return result, nil
}

// Signal sends a terminal a signal. A candidate may only interrupt.
func (t *Terminals) Signal(ctx context.Context, candidate CandidateAttempt, tag string, signal ProcessSignal) error {
	if !IsTerminalTag(tag) {
		return ErrTerminalNotFound
	}
	if signal != SignalInterrupt {
		return ErrTerminalNotAuthor
	}
	at, err := t.reach(ctx, candidate.AttemptID)
	if err != nil {
		return err
	}
	return terminalError(t.fenced(ctx, &at, func(fence Fence) error {
		return at.machine.SignalProcess(ctx, fence, tag, signal)
	}))
}

// Attach opens a session on one terminal. Any number of sessions may read
// a terminal; the candidate's sessions also write to it. The caller closes
// the session.
func (t *Terminals) Attach(ctx context.Context, candidate CandidateAttempt, tag string) (*TerminalSession, error) {
	if !IsTerminalTag(tag) {
		return nil, ErrTerminalNotFound
	}
	at, err := t.reach(ctx, candidate.AttemptID)
	if err != nil {
		return nil, err
	}
	open, err := terminals(ctx, at.machine)
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(open, func(info ProcessInfo) bool { return info.Tag == tag })
	if index < 0 {
		return nil, ErrTerminalNotFound
	}
	session := &TerminalSession{terminals: t, at: at, tag: tag, author: true}
	if at.row.RecordInput {
		session.recorder = newInputRecorder(t, at.row.AttemptID, at.row.Incarnation, tag, open[index].Input)
	}
	return session, nil
}

// TerminalSession is one attachment to one terminal on the machine the
// environment row named when it was opened.
type TerminalSession struct {
	terminals *Terminals
	at        reached
	tag       string
	// author is set when the session writes as the terminal's author.
	author   bool
	recorder *inputRecorder
}

// Tag is the terminal the session is attached to.
func (s *TerminalSession) Tag() string { return s.tag }

// Output reads the terminal's output from offset, replaying what its
// history still holds.
func (s *TerminalSession) Output(ctx context.Context, from uint64) (ProcessAttachment, error) {
	attachment, err := s.at.machine.AttachProcess(ctx, s.tag, from)
	if err != nil {
		return nil, terminalError(err)
	}
	return terminalAttachment{attachment: attachment}, nil
}

// terminalAttachment reports a machine that ended as ErrTerminalEnded.
type terminalAttachment struct{ attachment ProcessAttachment }

func (a terminalAttachment) Next(ctx context.Context) (ProcessOutput, error) {
	output, err := a.attachment.Next(ctx)
	if err != nil && (errors.Is(err, ErrMachineRevoked) || errors.Is(err, ErrMachineLost)) {
		return output, ErrTerminalEnded
	}
	return output, err
}

func (a terminalAttachment) Close() error { return a.attachment.Close() }

// Screen is the terminal's screen now, for a client with no screen of its
// own: it draws Data, then reads output from Offset.
func (s *TerminalSession) Screen(ctx context.Context) (Screen, error) {
	screen, err := s.at.machine.Screen(ctx, s.tag)
	return screen, terminalError(err)
}

// Write writes input at offset and returns the terminal's input offset
// after it. Bytes the terminal already took are dropped, so a resend after
// a lost connection types nothing twice; an offset past its input is an
// InputAheadError. When the policy records input, what the terminal took
// is recorded.
func (s *TerminalSession) Write(ctx context.Context, offset uint64, data []byte) (uint64, error) {
	if !s.author {
		return 0, ErrTerminalNotAuthor
	}
	var next uint64
	err := s.terminals.fenced(ctx, &s.at, func(fence Fence) error {
		var writeErr error
		next, writeErr = s.at.machine.WriteInput(ctx, fence, s.tag, offset, data)
		return writeErr
	})
	if errors.Is(err, ErrMachineConflict) {
		// The fence is the row's, so the offset is past the input.
		input, inputErr := s.input(ctx)
		if inputErr != nil {
			return 0, inputErr
		}
		if offset > input {
			return input, &InputAheadError{Input: input}
		}
		return input, ErrEnvironmentNotRunning
	}
	if err != nil {
		return next, terminalError(err)
	}
	if s.recorder != nil && next == offset+uint64(len(data)) {
		s.recorder.input(offset, data)
	}
	return next, nil
}

// input is where the terminal's input stands.
func (s *TerminalSession) input(ctx context.Context) (uint64, error) {
	open, err := terminals(ctx, s.at.machine)
	if err != nil {
		return 0, err
	}
	for _, info := range open {
		if info.Tag == s.tag {
			return info.Input, nil
		}
	}
	return 0, ErrTerminalNotFound
}

// Resize changes the terminal's window. It is recorded with the input.
func (s *TerminalSession) Resize(ctx context.Context, window Window) error {
	if !s.author {
		return ErrTerminalNotAuthor
	}
	if !window.Valid() {
		return errors.New("the terminal window is out of bounds")
	}
	err := s.terminals.fenced(ctx, &s.at, func(fence Fence) error {
		return s.at.machine.Resize(ctx, fence, s.tag, window)
	})
	if err != nil {
		return terminalError(err)
	}
	if s.recorder != nil {
		s.recorder.resize(window)
	}
	return nil
}

// Check rereads the environment row and reports ErrTerminalEnded once the
// machine the session is attached to is no longer the running one.
func (s *TerminalSession) Check(ctx context.Context) error {
	row, err := s.terminals.config.Environments.Get(ctx, s.at.row.AttemptID)
	if err != nil {
		return err
	}
	if row.Desired == model.AttemptEnvironmentDesiredEnded || row.State == model.AttemptEnvironmentEnded ||
		row.Incarnation != s.at.row.Incarnation || row.GrantID != s.at.row.GrantID || row.Epoch != s.at.row.Epoch {
		return ErrTerminalEnded
	}
	return nil
}

// Close stores the input the session recorded and not yet stored.
func (s *TerminalSession) Close(ctx context.Context) {
	if s.recorder != nil {
		s.recorder.close(ctx)
	}
}
