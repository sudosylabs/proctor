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
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// The failures a host port reports. An adapter wraps one of them, and only
// names the operation that failed: host detail never reaches the owner.
var (
	// ErrHostCapacity: the host cannot fit the machine now; try another.
	ErrHostCapacity = errors.New("execution host has no capacity")
	// ErrHostUnavailable: the host cannot be reached or will not place a
	// machine now (draining, no such image or network, no isolation).
	ErrHostUnavailable = errors.New("execution host unavailable")
	// ErrMachineLost: the machine ended without a revoke. Revoke
	// acknowledges the loss; a new incarnation follows.
	ErrMachineLost = errors.New("execution machine lost")
	// ErrMachineRevoked: the machine no longer exists.
	ErrMachineRevoked = errors.New("execution machine revoked")
	// ErrMachineConflict: the call disagrees with the machine's state, such
	// as a stale fence, another spec under the same grant, or a projection
	// over a guest change nobody confirmed.
	ErrMachineConflict = errors.New("execution machine conflict")
	// ErrMachineFrozen: the call needs a running machine.
	ErrMachineFrozen = errors.New("execution machine frozen")
	// ErrMachineStale: observed content changed after it was observed; a
	// later observation carries the change.
	ErrMachineStale = errors.New("execution observation stale")
	// ErrMachineLagged: the machine can no longer report guest changes from
	// the position asked for; the projection must start again.
	ErrMachineLagged = errors.New("execution observation lagged")
	// ErrMachineNotFound: no such grant, transfer or content.
	ErrMachineNotFound = errors.New("execution machine not found")
	// ErrMachineTooLarge: metadata over its bound, or the machine's disk is
	// full.
	ErrMachineTooLarge = errors.New("execution machine too large")
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

// MachineSpec is what a host is asked to place. The owner derives it from
// the environment row alone, so a later owner ensures exactly the same one.
type MachineSpec struct {
	GrantID   string
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
	// Ensure places the machine on the named host, or returns the one it
	// already holds under spec.GrantID.
	Ensure(ctx context.Context, host string, spec MachineSpec) (Machine, error)
	// Revoke ends a machine, and acknowledges a lost one. Revoking an
	// unknown grant succeeds.
	Revoke(ctx context.Context, host, grantID string) error
	// WatchGrant follows one machine.
	WatchGrant(ctx context.Context, host, grantID string) (GrantWatch, error)
	// IgnoreRules compiles the ignore rules every projection starts with:
	// the Workspace file that holds them and the patterns that apply while
	// it is missing or does not parse.
	IgnoreRules(file string, fallback []string) (IgnoreRules, error)
}

// IgnoreRules decide which new guest paths stay on the machine.
type IgnoreRules interface {
	// Policy is the decision for the ignore file's current body; present
	// is false while the Workspace has no such file.
	Policy(body []byte, present bool) IgnorePolicy
}

// IgnorePolicy is one decision of the ignore rules.
type IgnorePolicy interface {
	// Observed reports whether a path the Workspace does not hold is kept.
	Observed(name string, directory bool) bool
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

// ProjectionMutation is one Workspace journal entry as the machine applies
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
}

// ProjectionEntry is one Workspace entry a projection starts with.
type ProjectionEntry struct {
	EntryID model.AttemptWorkspaceEntryID
	Kind    model.StarterWorkspaceEntryKind
	Path    string
	Version string
	Content *ContentRef
}

// ProjectionInit installs the Workspace into a new machine, once.
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

// ProjectionReceipt is what an initialization or apply left.
type ProjectionReceipt struct {
	Cursor     int64
	HostCursor uint64
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
}

// ObservationPage is one page of guest changes after a position.
type ObservationPage struct {
	HostCursor   uint64
	Observations []Observation
}

// ProcessEvent is one entry of the machine's command log, recorded outside
// the candidate's workload. Its JSON form is the evidence detail.
type ProcessEvent struct {
	Sequence  uint64    `json:"sequence"`
	Kind      string    `json:"kind"`
	PID       int       `json:"pid,omitempty"`
	ParentPID int       `json:"parent_pid,omitempty"`
	Argv      []string  `json:"argv,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Unread    bool      `json:"unread,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Tag       string    `json:"tag,omitempty"`
	At        time.Time `json:"at"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Signal    int       `json:"signal,omitempty"`
	Dropped   uint64    `json:"dropped,omitempty"`
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
// the current fence; the projection and observation
// calls need it running.
type Machine interface {
	GrantID() string
	Epoch() string
	Control(ctx context.Context, fence Fence, state ControlState) (ControlReceipt, error)
	UploadContent(ctx context.Context, fence Fence, body io.Reader) (ContentRef, error)
	InitializeProjection(ctx context.Context, request ProjectionInit) (ProjectionReceipt, error)
	ApplyProjection(ctx context.Context, request ProjectionApply) (ProjectionReceipt, error)
	Observe(ctx context.Context, fence Fence, after uint64, limit int) (ObservationPage, error)
	OpenObservedContent(ctx context.Context, fence Fence, content ContentRef) (io.ReadCloser, error)
	// ConfirmObservation binds an accepted guest change to the journal
	// mutation that stored it, so its projection is known as an echo.
	ConfirmObservation(ctx context.Context, fence Fence, hostSequence uint64, mutation ProjectionMutation) error
	AcknowledgeObservations(ctx context.Context, fence Fence, through uint64) error
	ObserveProcesses(ctx context.Context, fence Fence, after uint64, limit int) (ProcessPage, error)
	AcknowledgeProcesses(ctx context.Context, fence Fence, through uint64) error
	Revoke(ctx context.Context) error
}
