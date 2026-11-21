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
	"strconv"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// The failures a host port reports. An adapter wraps one of them, and only
// names the operation that failed: host detail never reaches the owner.
var (
	// ErrHostCapacity means the host cannot fit the machine now; try another.
	ErrHostCapacity = errors.New("execution host has no capacity")
	// ErrHostUnavailable means the host cannot be reached or will not place a
	// machine now (draining, no such image or network, no isolation).
	ErrHostUnavailable = errors.New("execution host unavailable")
	// ErrMachineLost means the machine ended without a revoke; Revoke
	// acknowledges the loss; a new incarnation follows.
	ErrMachineLost = errors.New("execution machine lost")
	// ErrMachineRevoked means the machine no longer exists.
	ErrMachineRevoked = errors.New("execution machine revoked")
	// ErrMachineConflict means the call disagrees with the machine's state, such
	// as a stale fence, another spec under the same grant, or a projection
	// over a guest change nobody confirmed.
	ErrMachineConflict = errors.New("execution machine conflict")
	// ErrMachineFrozen means the call needs a running machine.
	ErrMachineFrozen = errors.New("execution machine frozen")
	// ErrMachineStale means observed content changed after it was observed; a
	// later observation carries the change.
	ErrMachineStale = errors.New("execution observation stale")
	// ErrMachineLagged means the machine can no longer report guest changes from
	// the position asked for; the projection must start again.
	ErrMachineLagged = errors.New("execution observation lagged")
	// ErrMachineNotFound means there is no such grant, transfer or content.
	ErrMachineNotFound = errors.New("execution machine not found")
	// ErrMachineTooLarge means metadata is over its bound, or the machine's
	// disk is full.
	ErrMachineTooLarge = errors.New("execution machine too large")
	// ErrMachineBusy means the machine runs as many processes as it may.
	ErrMachineBusy = errors.New("execution machine busy")
	// ErrProcessExited means the process has exited and takes no more input,
	// size or signal.
	ErrProcessExited = errors.New("execution process exited")
)

// HostReport is one reachable host's readiness: whether it places new
// machines, what it offers, and what it has free now.
type HostReport struct {
	Name     string
	Usable   bool
	Images   []string
	Networks []model.EnvironmentNetwork
	Free     model.EnvironmentResources
	// GrantMax is the most one machine may ask for.
	GrantMax model.EnvironmentResources
}

// Offers reports whether the host can place a machine of this image,
// network and size now.
func (r HostReport) Offers(image string, network model.EnvironmentNetwork, resources model.EnvironmentResources) bool {
	if !r.Usable {
		return false
	}
	imaged, networked := false, false
	for _, offered := range r.Images {
		imaged = imaged || offered == image
	}
	for _, offered := range r.Networks {
		networked = networked || offered == network
	}
	fits := func(want, free, most int64) bool { return want <= free && (most == 0 || want <= most) }
	return imaged && networked &&
		fits(resources.CPUMillis, r.Free.CPUMillis, r.GrantMax.CPUMillis) &&
		fits(resources.MemoryBytes, r.Free.MemoryBytes, r.GrantMax.MemoryBytes) &&
		fits(resources.DiskBytes, r.Free.DiskBytes, r.GrantMax.DiskBytes)
}

// GrantRef names one machine: the host that holds it and the grant it was
// placed under.
type GrantRef struct {
	Host    string
	GrantID string
}

// MachineSpec is what a host is asked to place. The owner derives it from
// the environment row alone, so a later owner ensures exactly the same one.
type MachineSpec struct {
	Image     string
	Network   model.EnvironmentNetwork
	Resources model.EnvironmentResources
	// IdleSuspend, when set, has the host save the machine to disk once it
	// has been idle that long.
	IdleSuspend time.Duration
}

// GrantState is where a host says a machine stands.
type GrantState string

// The states a host reports for a machine.
const (
	GrantCreating  GrantState = "creating"
	GrantRunning   GrantState = "running"
	GrantFrozen    GrantState = "frozen"
	GrantSuspended GrantState = "suspended"
	GrantRevoked   GrantState = "revoked"
	GrantLost      GrantState = "lost"
)

// GrantWatch follows one machine's state. Next returns io.EOF after
// GrantRevoked.
type GrantWatch interface {
	Next(ctx context.Context) (GrantState, error)
	Close() error
}

// Hosts is the owner's port to the configured execution hosts.
type Hosts interface {
	// Reports returns the readiness of every host that answers now.
	Reports(ctx context.Context) []HostReport
	// Ensure places the machine under the grant on its host, or returns the
	// one the host already holds under that grant.
	Ensure(ctx context.Context, grant GrantRef, spec MachineSpec) (Machine, error)
	// Revoke ends a machine, and acknowledges a lost one. Revoking an
	// unknown grant succeeds.
	Revoke(ctx context.Context, grant GrantRef) error
	WatchGrant(ctx context.Context, grant GrantRef) (GrantWatch, error)
	// IgnoreRules compiles the ignore rules every projection starts with:
	// the workspace file that holds them and the patterns that apply while
	// it is missing or does not parse.
	IgnoreRules(file string, fallback []string) (IgnoreRules, error)
}

// IgnoreRules decide which new guest paths stay on the machine.
type IgnoreRules interface {
	// Policy is the decision for the ignore file's current body; present
	// is false while the workspace has no such file.
	Policy(body []byte, present bool) IgnorePolicy
}

// IgnorePolicy is one decision of the ignore rules.
type IgnorePolicy interface {
	// Observed reports whether a path the workspace does not hold is kept.
	Observed(name string, directory bool) bool
	// Fallback reports whether the default patterns decide, because the
	// workspace has no ignore file or one that does not parse.
	Fallback() bool
}

// Fence names one authorized effect on one machine. Revision grows with
// every control the owner issues.
type Fence struct {
	GrantID  string
	Epoch    string
	Revision int64
}

// ControlState is a state the owner asks of a machine.
type ControlState string

// The states the owner asks of a machine.
const (
	ControlRunning ControlState = "running"
	ControlFrozen  ControlState = "frozen"
	ControlRevoked ControlState = "revoked"
)

// ControlReceipt is the newest control a machine accepted.
type ControlReceipt struct {
	Fence     Fence
	State     ControlState
	Confirmed bool
}

// ContentRef names bytes uploaded to, or observed on, one machine.
type ContentRef struct {
	Size       int64
	SHA256     string
	TransferID string
}

// ProjectionOperation is one kind of journal mutation the owner projects.
type ProjectionOperation string

// The journal mutations a machine applies.
const (
	ProjectionCreate ProjectionOperation = "create"
	ProjectionWrite  ProjectionOperation = "write"
	ProjectionMove   ProjectionOperation = "move"
	ProjectionDelete ProjectionOperation = "delete"
)

// ProjectionMutation is one workspace journal entry as the machine applies
// it. Cursor is its journal cursor.
type ProjectionMutation struct {
	Cursor           int64
	EntryID          model.AttemptWorkspaceEntryID
	Operation        ProjectionOperation
	Kind             model.StarterWorkspaceEntryKind
	Path             string
	DestinationPath  string
	ExpectedVersion  string
	ResultingVersion string
	Recursive        *bool
	Content          *ContentRef
	// Overwrite has a create, write or delete replace what the guest left
	// at Path and under it, unconfirmed changes included, instead of being
	// refused: the journal wins in place. The receipt lists the guest
	// changes it discarded. It is still refused when the guest moved a path
	// across Path's boundary or Path's parent is no longer a directory.
	Overwrite bool
}

// ProjectionEntry is one workspace entry a projection starts with.
type ProjectionEntry struct {
	EntryID model.AttemptWorkspaceEntryID
	Kind    model.StarterWorkspaceEntryKind
	Path    string
	Version string
	Content *ContentRef
}

// ProjectionInit installs the workspace into a new machine, once.
type ProjectionInit struct {
	Fence          Fence
	MutationID     string
	Cursor         int64
	Entries        []ProjectionEntry
	IgnoreFile     string
	IgnoreFallback []string
}

// ProjectionApply applies consecutive journal entries From+1..Through.
// ExpectedHostCursor is the host cursor of the last observation page: a
// guest change since refuses the apply.
type ProjectionApply struct {
	Fence              Fence
	MutationID         string
	From               int64
	Through            int64
	ExpectedHostCursor uint64
	Mutations          []ProjectionMutation
}

// ProjectionReceipt is what an initialization or apply left. Discarded
// lists, in order, the host sequences of the guest changes an Overwrite
// replaced; later pages report them with DiscardedBy.
type ProjectionReceipt struct {
	Cursor     int64
	HostCursor uint64
	Discarded  []uint64
}

// ObservationOperation is one kind of guest change.
type ObservationOperation string

// The guest changes a machine reports.
const (
	ObserveCreateFile      ObservationOperation = "create_file"
	ObserveCreateDirectory ObservationOperation = "create_directory"
	ObserveWriteFile       ObservationOperation = "write_file"
	ObserveMove            ObservationOperation = "move"
	ObserveDelete          ObservationOperation = "delete"
)

// Observation is one change the guest made, with the projected version it
// was made against and the content it captured.
type Observation struct {
	HostSequence    uint64
	Operation       ObservationOperation
	NodeIdentity    string
	Kind            model.StarterWorkspaceEntryKind
	Path            string
	DestinationPath string
	ExpectedVersion string
	Recursive       *bool
	Content         *ContentRef
	// ProcessTag is the tag of the started process the writer descends
	// from, such as terminal:1, or empty when the host cannot say.
	ProcessTag model.AttemptEnvironmentProcessTag
	// DiscardedBy is the journal cursor of the Overwrite that replaced this
	// change before it was confirmed, or 0. A discarded change is gone from
	// the machine: it cannot be confirmed and its content is stale.
	DiscardedBy int64
}

// ObserveRequest asks for the guest changes after a host sequence. With a
// Wait, at most MaxObserveWait, the call holds until the guest has a change
// after After or the wait passes, and then returns what there is: an empty
// page is not an error. A freeze, revoke or loss ends the wait with that
// error.
type ObserveRequest struct {
	Fence Fence
	After uint64
	Limit int
	Wait  time.Duration
}

// MaxObserveWait bounds ObserveRequest.Wait.
const MaxObserveWait = time.Minute

// ObservationPage is one page of guest changes after a position.
type ObservationPage struct {
	HostCursor   uint64
	Observations []Observation
}

// ProcessEvent is one entry of the machine's command log, recorded outside
// the candidate's workload. Its JSON form is the evidence detail.
type ProcessEvent struct {
	Sequence  uint64                             `json:"sequence"`
	Kind      string                             `json:"kind"`
	PID       int                                `json:"pid,omitempty"`
	ParentPID int                                `json:"parent_pid,omitempty"`
	Argv      []string                           `json:"argv,omitempty"`
	Truncated bool                               `json:"truncated,omitempty"`
	Unread    bool                               `json:"unread,omitempty"`
	Dir       string                             `json:"dir,omitempty"`
	Tag       model.AttemptEnvironmentProcessTag `json:"tag,omitempty"`
	At        time.Time                          `json:"at"`
	ExitCode  *int                               `json:"exit_code,omitempty"`
	Signal    int                                `json:"signal,omitempty"`
	Dropped   uint64                             `json:"dropped,omitempty"`
	// Origin is the tag of the started process this one descends from,
	// such as the terminal that ran it. The owner sets it; the host never
	// does, and Tag stays the host's.
	Origin model.AttemptEnvironmentProcessTag `json:"origin,omitempty"`
}

// ProcessDropped is the kind of the event that stands in for lost ones.
const ProcessDropped = "dropped"

// ProcessPage is one page of the command log; Next is the position after
// it.
type ProcessPage struct {
	Events []ProcessEvent
	Next   uint64
}

// The machine's page bounds.
const (
	// MaxProjectionMutations bounds one projection apply and one
	// observation page.
	MaxProjectionMutations = 128
	// MaxProcessPage bounds one command log page.
	MaxProcessPage = 256
	// MaxIgnoreFileBytes bounds the ignore file the rules read.
	MaxIgnoreFileBytes = 64 << 10
)

// Machine is one placed machine for one epoch. Every call but Revoke names
// the current fence; the projection and observation calls need it running.
type Machine interface {
	GrantID() string
	Epoch() string
	Control(ctx context.Context, fence Fence, state ControlState) (ControlReceipt, error)
	UploadContent(ctx context.Context, fence Fence, body io.Reader) (ContentRef, error)
	InitializeProjection(ctx context.Context, request ProjectionInit) (ProjectionReceipt, error)
	ApplyProjection(ctx context.Context, request ProjectionApply) (ProjectionReceipt, error)
	Observe(ctx context.Context, request ObserveRequest) (ObservationPage, error)
	OpenObservedContent(ctx context.Context, fence Fence, content ContentRef) (io.ReadCloser, error)
	// ConfirmObservation binds an accepted guest change to the journal
	// mutation that stored it, so its projection is known as an echo.
	ConfirmObservation(ctx context.Context, fence Fence, hostSequence uint64, mutation ProjectionMutation) error
	AcknowledgeObservations(ctx context.Context, fence Fence, through uint64) error
	ObserveProcesses(ctx context.Context, fence Fence, after uint64, limit int) (ProcessPage, error)
	AcknowledgeProcesses(ctx context.Context, fence Fence, through uint64) error
	Processes
	Revoke(ctx context.Context) error
}

// Window is a terminal's size in character cells.
type Window struct {
	Cols uint16
	Rows uint16
}

// MaxWindowCells bounds a window's columns and rows.
const MaxWindowCells = 1000

// Valid reports whether the window has a size within its bound.
func (w Window) Valid() bool {
	return w.Cols > 0 && w.Rows > 0 && w.Cols <= MaxWindowCells && w.Rows <= MaxWindowCells
}

// ProcessStreams is how a process's input and output are wired.
type ProcessStreams string

// The wirings a process may have.
const (
	// ProcessStreamsPTY gives the process a terminal of the spec's PTY
	// window: one output stream, and input to the terminal.
	ProcessStreamsPTY ProcessStreams = "pty"
	// ProcessStreamsPipes gives the process separate stdout and stderr, and
	// closes its input at once unless the spec keeps Stdin open.
	ProcessStreamsPipes ProcessStreams = "pipes"
)

// ProcessSpec starts one process. A terminal has the PTY streams of its
// window and, with no Argv, runs the image's login shell.
type ProcessSpec struct {
	Tag  model.AttemptEnvironmentProcessTag
	Argv []string
	// Env adds NAME=value entries to the workload's environment.
	Env []string
	// Dir is the working directory relative to the workspace root; empty
	// is the root itself. The host refuses one that leaves the workspace.
	Dir     string
	Streams ProcessStreams
	// PTY is the terminal's window, set exactly when Streams is PTY.
	PTY *Window
	// Stdin keeps a piped process's input open for WriteInput.
	Stdin bool
	// Timeout, when set, ends the process that long after it started: the
	// host sends it TERM, then KILL a few seconds later.
	Timeout time.Duration
}

// Valid reports whether the spec's wiring is whole: a PTY process has a
// valid window and no separate input switch, and a piped process has a
// command and no window.
func (s ProcessSpec) Valid() bool {
	if s.Timeout < 0 {
		return false
	}
	switch s.Streams {
	case ProcessStreamsPTY:
		return s.PTY != nil && s.PTY.Valid() && !s.Stdin
	case ProcessStreamsPipes:
		return s.PTY == nil && len(s.Argv) > 0
	}
	return false
}

// ProcessStream is one output of a process.
type ProcessStream string

// The outputs a process has: a PTY process has only ProcessStreamPTY, and a
// piped one ProcessStreamStdout and ProcessStreamStderr.
const (
	ProcessStreamPTY    ProcessStream = "pty"
	ProcessStreamStdout ProcessStream = "stdout"
	ProcessStreamStderr ProcessStream = "stderr"
)

// ProcessExit is how a process ended: its exit code, or the name of the
// signal that killed it, such as KILL, with Code 0.
type ProcessExit struct {
	Code   int
	Signal string
}

// The guest is Linux, so a host reports a signal by Linux's number. Only
// the ones a workload commonly dies of are named; any other keeps its
// number as its name.
var signalNames = map[int]string{
	1: "HUP", 2: "INT", 3: "QUIT", 4: "ILL", 5: "TRAP", 6: "ABRT", 7: "BUS", 8: "FPE",
	9: "KILL", 10: "USR1", 11: "SEGV", 12: "USR2", 13: "PIPE", 14: "ALRM", 15: "TERM",
}

// SignalName names a signal number a host reports; zero is no signal.
func SignalName(number int) string {
	if number == 0 {
		return ""
	}
	if name, ok := signalNames[number]; ok {
		return name
	}
	return strconv.Itoa(number)
}

// SignalNumber is the number of the signal that killed the process, or 0.
func (e ProcessExit) SignalNumber() int {
	if e.Signal == "" {
		return 0
	}
	for number, name := range signalNames {
		if name == e.Signal {
			return number
		}
	}
	number, _ := strconv.Atoi(e.Signal)
	return number
}

// ProcessInfo describes one process a Start created, running or exited.
// Output is the end offset of each of its streams and Input how many bytes
// of input it took. Ended is zero and Exit is nil while it runs.
type ProcessInfo struct {
	Tag     model.AttemptEnvironmentProcessTag
	Streams ProcessStreams
	Started time.Time
	Ended   time.Time
	Running bool
	Exit    *ProcessExit
	Output  map[ProcessStream]uint64
	Input   uint64
}

// OutputKind is what one ProcessOutput carries.
type OutputKind uint8

// The events of an attachment.
const (
	// OutputData is bytes of Stream at Offset.
	OutputData OutputKind = iota + 1
	// OutputTruncated says bytes of Stream from Offset to To left the
	// history before this reader got them.
	OutputTruncated
	// OutputExit is the process's exit, always the last event.
	OutputExit
)

// ProcessOutput is one event of an attachment. Offsets count within
// Stream; an exit names no stream.
type ProcessOutput struct {
	Kind       OutputKind
	Stream     ProcessStream
	Offset     uint64
	To         uint64
	Data       []byte
	ExitCode   int
	ExitSignal int
}

// ProcessAttachment reads one process's output from an offset. Next
// returns io.EOF after OutputExit, and ErrMachineRevoked or ErrMachineLost
// once the machine has ended.
type ProcessAttachment interface {
	Next(ctx context.Context) (ProcessOutput, error)
	Close() error
}

// Screen is a terminal's screen as of output Offset: Data redraws it on a
// blank terminal of Window's size, and attaching from Offset continues it.
type Screen struct {
	Data         []byte
	Offset       uint64
	Window       Window
	AltScreen    bool
	CursorRow    int
	CursorCol    int
	CursorHidden bool
	// Text is each row as plain text, trailing blanks trimmed.
	Text []string
}

// ProcessSignal is a signal the core may send a process.
type ProcessSignal string

// The signals the core sends. A candidate only interrupts; terminate and
// kill end a command the core started, such as one past its deadline.
const (
	SignalInterrupt ProcessSignal = "interrupt"
	SignalTerminate ProcessSignal = "terminate"
	SignalKill      ProcessSignal = "kill"
)

// Processes runs a machine's processes by tag. Starting, input, size and
// signals carry the current fence and need a running machine; reading does
// not. A Start under a running tag is ErrMachineConflict; one under an
// exited tag replaces it.
type Processes interface {
	StartProcess(ctx context.Context, fence Fence, spec ProcessSpec) (ProcessInfo, error)
	ListProcesses(ctx context.Context) ([]ProcessInfo, error)
	// AttachProcess reads a process's output from offset in each of its
	// streams; output older than its history is reported as truncated. A
	// piped process's streams count apart, so its readers start at 0.
	AttachProcess(ctx context.Context, tag model.AttemptEnvironmentProcessTag, from uint64) (ProcessAttachment, error)
	// WriteInput appends data at offset and returns the process's input
	// offset after it. Bytes it already took are dropped, so a resend never
	// types twice; an offset past what it took is ErrMachineConflict.
	WriteInput(ctx context.Context, fence Fence, tag model.AttemptEnvironmentProcessTag, offset uint64, data []byte) (uint64, error)
	Resize(ctx context.Context, fence Fence, tag model.AttemptEnvironmentProcessTag, window Window) error
	SignalProcess(ctx context.Context, fence Fence, tag model.AttemptEnvironmentProcessTag, signal ProcessSignal) error
	Screen(ctx context.Context, tag model.AttemptEnvironmentProcessTag) (Screen, error)
}
