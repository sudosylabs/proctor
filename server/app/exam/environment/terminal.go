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

	"github.com/sudosylabs/proctor/server/app/invocation"
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
	// ErrNoEnvironment means the attempt's revision does not enable execution.
	ErrNoEnvironment = errors.New("the Attempt has no execution environment")
	// ErrEnvironmentNotRunning means the environment has no running machine
	// this node can reach now.
	ErrEnvironmentNotRunning = errors.New("the execution environment is not running")
	// ErrTerminalLimit means the candidate has as many terminals open as the
	// policy allows, or the machine opened as many as it may.
	ErrTerminalLimit = errors.New("the terminal limit is reached")
	// ErrTerminalNotFound means no terminal has that tag on the current
	// machine.
	ErrTerminalNotFound = errors.New("no such terminal")
	// ErrTerminalExited means the terminal has exited.
	ErrTerminalExited = errors.New("the terminal has exited")
	// ErrTerminalNotAuthor means the caller is not the terminal's author and
	// may only read it.
	ErrTerminalNotAuthor = errors.New("only the terminal's author writes to it")
	// ErrTerminalEnded means the machine the terminal ran on has ended or been
	// replaced; nothing more comes from it.
	ErrTerminalEnded = errors.New("the terminal's machine has ended")
)

// InputAheadError refuses input written past what the terminal has taken.
// Input is where the terminal's input stands.
type InputAheadError struct{ Input uint64 }

func (e *InputAheadError) Error() string {
	return "terminal input offset is past the terminal's input at " + strconv.FormatUint(e.Input, 10)
}

// IsTerminalTag reports whether tag names a terminal a candidate opened.
func IsTerminalTag(tag model.AttemptEnvironmentProcessTag) bool {
	_, ok := terminalNumber(tag)
	return ok
}

func terminalNumber(tag model.AttemptEnvironmentProcessTag) (int, bool) {
	digits, ok := strings.CutPrefix(string(tag), TerminalTagPrefix)
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
func TerminalTag(n int) model.AttemptEnvironmentProcessTag {
	return model.AttemptEnvironmentProcessTag(TerminalTagPrefix + strconv.Itoa(n))
}

// Terminal describes one terminal on the environment's current machine.
// Output is how many bytes it has written and Input how many it has taken.
type Terminal struct {
	Tag        model.AttemptEnvironmentProcessTag
	Running    bool
	Started    time.Time
	ExitCode   int
	ExitSignal int
	Output     uint64
	Input      uint64
}

func terminalOf(info ProcessInfo) Terminal {
	terminal := Terminal{Tag: info.Tag, Running: info.Running, Started: info.Started,
		Output: info.Output[ProcessStreamPTY], Input: info.Input}
	if info.Exit != nil {
		terminal.ExitCode, terminal.ExitSignal = info.Exit.Code, info.Exit.SignalNumber()
	}
	return terminal
}

// TerminalsDependencies configures NewTerminals; Environments, Evidence,
// Hosts and Candidates are required.
type TerminalsDependencies struct {
	Environments store.AttemptEnvironmentStore
	// Evidence stores recorded terminal input.
	Evidence store.ExamAttemptWorkspaceStore
	Hosts    Hosts
	// Candidates proves a candidate's access before every call, and again
	// while a session stays attached.
	Candidates CandidateVerifier
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
// it reaches the machine the environment row names itself and writes under
// the row's current control fence. Terminal bytes never travel over cluster
// peer delivery. A nil Terminals means the installation runs no execution
// environments.
type Terminals struct {
	deps TerminalsDependencies
}

// NewTerminals returns Terminals, defaulting Now and the input bounds.
func NewTerminals(deps TerminalsDependencies) (*Terminals, error) {
	if deps.Environments == nil || deps.Evidence == nil || deps.Hosts == nil || deps.Candidates == nil {
		return nil, errors.New("terminals need the environments, evidence, hosts and candidate verification")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.InputFlush <= 0 {
		deps.InputFlush = DefaultInputFlush
	}
	if deps.InputChunkBytes <= 0 {
		deps.InputChunkBytes = DefaultInputChunkBytes
	}
	return &Terminals{deps: deps}, nil
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
	row, err := t.deps.Environments.Get(ctx, attemptID)
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
	machine, err := t.deps.Hosts.Ensure(ctx, grantOf(row), machineSpec(row))
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
	row, readErr := t.deps.Environments.Get(ctx, at.row.AttemptID)
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

// terminals lists a machine's candidate terminals in tag order.
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

// open starts a terminal running the image's login shell and answers with
// its tag: the number after the highest the machine has opened. It refuses
// once the candidate has the policy's number of terminals running, and when
// the environment is not running.
func (t *Terminals) open(ctx context.Context, candidate CandidateAttempt, window Window) (Terminal, error) {
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
			started, startErr = at.machine.StartProcess(ctx, fence, ProcessSpec{Tag: TerminalTag(highest + 1),
				Streams: ProcessStreamsPTY, PTY: &window})
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

func (t *Terminals) list(ctx context.Context, candidate CandidateAttempt) (TerminalList, error) {
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

// signal sends a terminal a signal; a candidate may only interrupt.
func (t *Terminals) signal(
	ctx context.Context,
	candidate CandidateAttempt,
	tag model.AttemptEnvironmentProcessTag,
	signal ProcessSignal,
) error {
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

// attach opens a session on one terminal, and answers with the terminal as
// it found it. Any number of sessions may read a terminal; an author's
// sessions also write to it, resize it and record what they write. The
// caller closes the session.
func (t *Terminals) attach(
	ctx context.Context,
	candidate CandidateAttempt,
	tag model.AttemptEnvironmentProcessTag,
	author bool,
) (*terminalSession, ProcessInfo, error) {
	if !IsTerminalTag(tag) {
		return nil, ProcessInfo{}, ErrTerminalNotFound
	}
	at, err := t.reach(ctx, candidate.AttemptID)
	if err != nil {
		return nil, ProcessInfo{}, err
	}
	open, err := terminals(ctx, at.machine)
	if err != nil {
		return nil, ProcessInfo{}, err
	}
	index := slices.IndexFunc(open, func(info ProcessInfo) bool { return info.Tag == tag })
	if index < 0 {
		return nil, ProcessInfo{}, ErrTerminalNotFound
	}
	session := &terminalSession{terminals: t, at: at, tag: tag, author: author}
	if author && at.row.RecordInput {
		session.recorder = newInputRecorder(t, at.row.AttemptID, at.row.Incarnation, tag, open[index].Input)
	}
	return session, open[index], nil
}

// TerminalScreen is a terminal's screen as text, for a reader that draws
// nothing: each row trimmed of trailing blanks, the window it fills, and
// whether the terminal still runs. Exit is nil while it runs.
type TerminalScreen struct {
	Text    []string
	Cols    uint16
	Rows    uint16
	Running bool
	Exit    *ProcessExit
}

// view reads a terminal's screen through a session that is not its
// author's, so nothing it does can type, resize, signal or record.
func (t *Terminals) view(
	ctx context.Context,
	candidate CandidateAttempt,
	tag model.AttemptEnvironmentProcessTag,
) (TerminalScreen, error) {
	session, info, err := t.attach(ctx, candidate, tag, false)
	if err != nil {
		return TerminalScreen{}, err
	}
	defer session.Close(ctx)
	screen, err := session.Screen(ctx)
	if err != nil {
		return TerminalScreen{}, err
	}
	// The state is the one attach found, read before the screen: a terminal
	// that exits in between shows its last screen and is reported running.
	return TerminalScreen{Text: screen.Text, Cols: screen.Window.Cols, Rows: screen.Window.Rows,
		Running: info.Running, Exit: info.Exit}, nil
}

// terminalSession is one attachment to one terminal on the machine the
// environment row named when it was opened, by the candidate whose access
// opened it.
type terminalSession struct {
	terminals *Terminals
	at        reached
	tag       model.AttemptEnvironmentProcessTag
	author    bool
	recorder  *inputRecorder
	// inv and access are the candidate's, rechecked by Check.
	inv    invocation.Invocation
	access CandidateAccess
}

// Output reads the terminal's output from offset, replaying what its
// history still holds.
func (s *terminalSession) Output(ctx context.Context, from uint64) (ProcessAttachment, error) {
	attachment, err := s.at.machine.AttachProcess(ctx, s.tag, from)
	if err != nil {
		return nil, terminalError(err)
	}
	return terminalOutput{attachment: attachment}, nil
}

// terminalOutput is a terminal's output, reporting a machine that ended as
// ErrTerminalEnded.
type terminalOutput struct{ attachment ProcessAttachment }

func (a terminalOutput) Next(ctx context.Context) (ProcessOutput, error) {
	output, err := a.attachment.Next(ctx)
	if err != nil && (errors.Is(err, ErrMachineRevoked) || errors.Is(err, ErrMachineLost)) {
		return output, ErrTerminalEnded
	}
	return output, err
}

func (a terminalOutput) Close() error { return a.attachment.Close() }

// Screen is the terminal's screen now, for a client with no screen of its
// own: it draws Data, then reads output from Offset.
func (s *terminalSession) Screen(ctx context.Context) (Screen, error) {
	screen, err := s.at.machine.Screen(ctx, s.tag)
	return screen, terminalError(err)
}

// Write writes input at offset and returns the terminal's input offset
// after it. Bytes the terminal already took are dropped, so a resend after
// a lost connection types nothing twice; an offset past its input is an
// InputAheadError. When the policy records input, what the terminal took
// is recorded.
func (s *terminalSession) Write(ctx context.Context, offset uint64, data []byte) (uint64, error) {
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

func (s *terminalSession) input(ctx context.Context) (uint64, error) {
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
func (s *terminalSession) Resize(ctx context.Context, window Window) error {
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

// ended rereads the environment row and reports ErrTerminalEnded once the
// machine the session is attached to is no longer the running one.
func (s *terminalSession) ended(ctx context.Context) error {
	row, err := s.terminals.deps.Environments.Get(ctx, s.at.row.AttemptID)
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
func (s *terminalSession) Close(ctx context.Context) {
	if s.recorder != nil {
		s.recorder.close(ctx)
	}
}
