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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The owner's default timing.
const (
	// DefaultObserveWait is how long one long-polled observation of a
	// running guest waits for a change before it is asked again.
	DefaultObserveWait = 25 * time.Second
	// DefaultHarvestQuiet is how long the guest stays quiet before its
	// changes are harvested, and DefaultHarvestMax the longest a change
	// waits.
	DefaultHarvestQuiet = 200 * time.Millisecond
	DefaultHarvestMax   = time.Second
	// DefaultIdle is how often an owner with nothing to do rereads its
	// environment, in case a wake was lost.
	DefaultIdle = DefaultBackstop
	// DefaultRetry is the first wait after a failure or a wait for
	// capacity; it doubles up to DefaultRetryMax.
	DefaultRetry    = time.Second
	DefaultRetryMax = 30 * time.Second
	// DefaultHostLossLimit is how long a host may stay unreachable before
	// its machine is replaced on another.
	DefaultHostLossLimit = time.Minute
)

const (
	// manifestPage and journalPage bound one workspace read.
	manifestPage = 200
	journalPage  = model.AttemptWorkspaceJournalReadMaximum
	// journalRound bounds the journal entries one round projects.
	journalRound = 4 * MaxProjectionMutations
	// divergedLimit is how many rounds the projection may stay refused,
	// overwrite included, without new guest activity before the machine is
	// replaced. Only a guest move across the overwritten subtree's boundary,
	// or a removed parent directory, defeats Overwrite in execenv.
	divergedLimit = 3
)

// IgnoreFile is the attempt workspace file that names the guest paths kept
// on the machine. The machine knows it only because the owner names it.
const IgnoreFile = ".proctorignore"

// DefaultIgnorePatterns apply while the workspace has no IgnoreFile, or one
// that does not parse: dependency installs and build output stay on the
// machine.
var DefaultIgnorePatterns = []string{
	"node_modules/", ".venv/", "venv/", "__pycache__/", "*.pyc", ".pytest_cache/", ".mypy_cache/",
	"target/", "build/", "dist/", "out/", ".gradle/", "*.o", "*.obj", "*.a", "*.so", "*.class", "*.jar", ".cache/",
}

// The gaps an owner records, under "gap" in an execution.gap record.
const (
	gapRestarted       = "environment_restarted"
	gapWaitingCapacity = "environment_waiting_capacity"
	gapProcessDropped  = "process_log_dropped"
)

// OwnerTiming tunes an owner. Zero fields take the defaults.
type OwnerTiming struct {
	ObserveWait   time.Duration
	HarvestQuiet  time.Duration
	HarvestMax    time.Duration
	Idle          time.Duration
	Retry         time.Duration
	RetryMax      time.Duration
	HostLossLimit time.Duration
}

func (t OwnerTiming) withDefaults() OwnerTiming {
	or := func(value, fallback time.Duration) time.Duration {
		if value <= 0 {
			return fallback
		}
		return value
	}
	return OwnerTiming{
		ObserveWait: min(or(t.ObserveWait, DefaultObserveWait), MaxObserveWait), HarvestQuiet: or(t.HarvestQuiet, DefaultHarvestQuiet),
		HarvestMax: or(t.HarvestMax, DefaultHarvestMax), Idle: or(t.Idle, DefaultIdle), Retry: or(t.Retry, DefaultRetry),
		RetryMax: or(t.RetryMax, DefaultRetryMax), HostLossLimit: or(t.HostLossLimit, DefaultHostLossLimit),
	}
}

// OwnerDependencies configures NewOwners; Environments, Evidence, Workspace
// and Hosts are required.
type OwnerDependencies struct {
	AssistantSessionEnded func(context.Context, model.ExamSittingID, model.ExamAttemptID, model.UserID, *store.AssistantSessionEndResult) error
	AssistantSessions     store.AssistantTranscriptStore
	Environments          store.AttemptEnvironmentStore
	// Evidence stores attempt evidence through the owner's access.
	Evidence  store.ExamAttemptWorkspaceStore
	Workspace Workspace
	Hosts     Hosts
	// Settled tells the seal waiting on a settle that the owner completed
	// it. Nil tells nobody: the waiter rereads the row when its budget ends.
	Settled *ClusterSettles
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// Report receives failures an owner recovers from by retrying. Nil
	// drops them.
	Report func(attemptID model.ExamAttemptID, err error)
	Timing OwnerTiming
}

// NewOwners returns the factory the Runtime runs owners from. It fails when
// the hosts cannot compile the default ignore rules.
func NewOwners(dependencies OwnerDependencies) (OwnerFactory, error) {
	if dependencies.Environments == nil || dependencies.Evidence == nil || dependencies.Workspace == nil ||
		dependencies.Hosts == nil {
		return nil, errors.New("attempt environment owners need the environments, evidence, Workspace and hosts")
	}
	rules, err := dependencies.Hosts.IgnoreRules(IgnoreFile, DefaultIgnorePatterns)
	if err != nil {
		return nil, fmt.Errorf("compile the default ignore rules: %w", err)
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	dependencies.Timing = dependencies.Timing.withDefaults()
	return func(attemptID model.ExamAttemptID) Owner {
		return &owner{deps: dependencies, attemptID: attemptID, rules: rules}
	}, nil
}

// restartError asks the owner to replace the machine with a new
// incarnation.
type restartError struct{ reason string }

func (e restartError) Error() string { return "the execution machine must be replaced: " + e.reason }

var (
	// errDone ends Run: the environment is ended.
	errDone = errors.New("attempt environment is done")
	// errLeaseLost ends Run: another owner may act now.
	errLeaseLost = errors.New("attempt environment lease lost")
	// errWaitCapacity means no host can take the machine now.
	errWaitCapacity = errors.New("no execution host has capacity")
	// errSealed means the attempt is submitted; the fact that ends the
	// environment follows.
	errSealed = errors.New("the Attempt is sealed")
	// errProjectionConflict means a guest change stands in the way of the
	// projection.
	errProjectionConflict = errors.New("the projection is refused by a guest change")
)

// owner acts on one attempt's environment while its node holds the lease.
// It is driven by its environment row and woken by facts, workspace commits
// and the machine's state; everything another owner needs to continue is in
// the row.
type owner struct {
	deps      OwnerDependencies
	attemptID model.ExamAttemptID
	rules     IgnoreRules

	lease store.AttemptRuntimeLease
	row   *model.AttemptEnvironment
	// saved is the owner state as last written.
	saved model.AttemptEnvironment

	machine Machine
	fence   Fence
	control ControlState
	ready   bool // the projection is initialized and the owner knows it
	watch   *grantFollower
	// observer long-polls the guest while the machine runs.
	observer *guestObserver

	unreachableSince time.Time

	proj       projected
	ahead      *tree
	hostCursor uint64
	// journalDue says a workspace commit may wait to be projected.
	journalDue    bool
	assistantWake bool
	journalReadAt time.Time
	echoes        map[int64]echo
	diverged      int
	drainNext     bool
	// overwriteNext has the next projection replace the guest's changes in
	// its way: a plain one was refused.
	overwriteNext bool
	applies       int

	pending      []Observation
	observed     uint64
	harvested    uint64
	firstPending time.Time
	lastObserved time.Time
	ignore       ignoreCache
	processLag   bool
	origins      processOrigins
}

// echo is a harvested change the owner confirmed to the machine, kept until
// the journal projection passes it.
type echo struct {
	mutation    ProjectionMutation
	observation uint64
	content     *ContentRef
}

// Run owns the environment until it is ended or ctx, the lease's context,
// ends.
func (o *owner) Run(ctx context.Context, lease store.AttemptRuntimeLease, wake <-chan struct{}) error {
	o.lease = lease
	defer o.dropMachine()
	row, err := o.deps.Environments.Get(ctx, o.attemptID)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	o.row, o.saved = row, *row
	if row.Done() {
		return nil
	}
	retry := o.deps.Timing.Retry
	var delay time.Duration
	for {
		if delay > 0 && !o.wait(ctx, wake, delay) {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		next, err := o.step(ctx)
		var restart restartError
		switch {
		case err == nil:
			retry, delay = o.deps.Timing.Retry, next
		case errors.Is(err, errDone):
			return nil
		case errors.Is(err, errLeaseLost) || ctx.Err() != nil:
			return nil
		case errors.As(err, &restart):
			if restartErr := o.restart(ctx, restart.reason); restartErr != nil {
				if errors.Is(restartErr, errLeaseLost) || ctx.Err() != nil {
					return nil
				}
				o.report(restartErr)
				delay, retry = retry, min(2*retry, o.deps.Timing.RetryMax)
				continue
			}
			delay = 0
		case errors.Is(err, errWaitCapacity), errors.Is(err, errSealed):
			delay, retry = retry, min(2*retry, o.deps.Timing.RetryMax)
		default:
			o.report(err)
			delay, retry = retry, min(2*retry, o.deps.Timing.RetryMax)
		}
	}
}

// wait sleeps for delay, or until a wake or a machine state change, and
// rereads the environment after a wake. It reports false once ctx ends.
func (o *owner) wait(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var changed, observed <-chan struct{}
	if o.watch != nil {
		changed = o.watch.changed
	}
	if o.observer != nil {
		observed = o.observer.ready
	}
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		o.journalDue = true
		o.assistantWake = true
		if row, err := o.deps.Environments.Get(ctx, o.attemptID); err == nil {
			// The facts come from the store; the owner's fields, saved or
			// not, are this owner's.
			copyOwnerState(row, o.row)
			o.row = row
		} else if ctx.Err() == nil {
			o.report(err)
		}
	case <-changed:
	case <-observed:
	case <-timer.C:
	}
	return ctx.Err() == nil
}

// step does one round of work and says how long to wait before the next.
func (o *owner) step(ctx context.Context) (time.Duration, error) {
	if o.row.Desired == model.AttemptEnvironmentDesiredEnded {
		return 0, o.teardown(ctx)
	}
	if o.row.ReplacePending() {
		// A manager asked for a new machine built from the workspace.
		return 0, restartError{reason: "manager"}
	}
	if o.machine == nil {
		if err := o.establish(ctx); err != nil {
			return 0, err
		}
	}
	if o.watch != nil && o.watch.state() == GrantLost {
		return 0, restartError{reason: "lost"}
	}
	if o.row.SettlePending() {
		if err := o.completeSettle(ctx); err != nil {
			return 0, err
		}
	}
	if o.row.Desired == model.AttemptEnvironmentDesiredFrozen {
		if o.control == ControlRunning && o.ready {
			if err := o.settle(ctx); err != nil {
				return 0, err
			}
		} else if o.control != ControlFrozen {
			if err := o.setControl(ctx, ControlFrozen); err != nil {
				return 0, err
			}
		}
		state := model.AttemptEnvironmentFrozen
		if o.watch != nil && o.watch.state() == GrantSuspended {
			state = model.AttemptEnvironmentSuspended
		}
		if !o.ready && o.row.State == model.AttemptEnvironmentCreating {
			// The projection starts on the next thaw.
			state = model.AttemptEnvironmentCreating
		}
		if err := o.saveState(ctx, state); err != nil {
			return 0, err
		}
		return o.deps.Timing.Idle, nil
	}
	if o.control != ControlRunning {
		if err := o.setControl(ctx, ControlRunning); err != nil {
			return 0, err
		}
	}
	if o.assistantWake {
		o.assistantWake = false
		o.checkAssistantSessions(ctx, o.fence, false)
	}
	if !o.ready {
		if err := o.prepare(ctx); err != nil {
			return 0, err
		}
	}
	if err := o.saveState(ctx, model.AttemptEnvironmentRunning); err != nil {
		return 0, err
	}
	if err := o.sync(ctx, false); err != nil {
		return 0, err
	}
	o.followGuest(ctx)
	return o.nextRound(), nil
}

// nextRound is how long a running owner waits before its next round
// unless something wakes it: a change the guest made, a workspace commit,
// a fact or the machine's state. Only work already in hand shortens it.
func (o *owner) nextRound() time.Duration {
	switch {
	case o.drainNext:
		// A refused projection: the guest's changes are harvested and the
		// journal projected again, over them.
		return o.deps.Timing.HarvestQuiet
	case o.journalDue:
		return 0
	case len(o.pending) > 0:
		now := o.deps.Now()
		quiet := o.lastObserved.Add(o.deps.Timing.HarvestQuiet).Sub(now)
		longest := o.firstPending.Add(o.deps.Timing.HarvestMax).Sub(now)
		return max(min(quiet, longest), time.Millisecond)
	default:
		return o.deps.Timing.Idle
	}
}

// followGuest has the observer long-poll the guest after what the owner
// has read, starting it for a running machine.
func (o *owner) followGuest(ctx context.Context) {
	if o.observer == nil {
		o.observer = observeGuest(ctx, o.machine, o.deps.Timing.ObserveWait)
	}
	o.observer.moveTo(o.fence, o.observed)
}

func (o *owner) stopObserving() {
	if o.observer != nil {
		o.observer.stop()
		o.observer = nil
	}
}

// settle brings everything the guest wrote into the workspace and freezes
// it: one complete observation, a harvest of all of it, its confirmation
// and acknowledgement, then the freeze. The machine serves no observation
// once it is frozen, so the drain comes first.
func (o *owner) settle(ctx context.Context) error {
	if o.control == ControlRunning && o.ready {
		if err := o.sync(ctx, true); err != nil && !errors.Is(err, errProjectionConflict) {
			return err
		}
	}
	return o.setControl(ctx, ControlFrozen)
}

// completeSettle answers every settle asked before a seal. A running
// machine drains: one complete observation is harvested, confirmed and
// acknowledged, so the workspace holds everything the guest wrote. A frozen
// machine drained when it froze, and one not yet projected wrote nothing.
func (o *owner) completeSettle(ctx context.Context) error {
	if o.control == ControlRunning && o.ready {
		if err := o.sync(ctx, true); err != nil && !errors.Is(err, errProjectionConflict) {
			return err
		}
	}
	o.row.SettleCompleted = o.row.SettleRequested
	if err := o.save(ctx); err != nil {
		return err
	}
	if o.deps.Settled != nil {
		o.deps.Settled.Settled(o.attemptID)
	}
	return nil
}

// machineSpec is the machine an environment row names. It is derived from
// the row alone, so every owner, and every node that serves a terminal,
// ensures exactly the same one.
func machineSpec(row *model.AttemptEnvironment) MachineSpec {
	return MachineSpec{Image: row.Image, Network: row.Network, Resources: row.Resources, IdleSuspend: row.IdleWindow}
}

// grantOf is the grant an environment row names.
func grantOf(row *model.AttemptEnvironment) GrantRef {
	return GrantRef{Host: row.Host, GrantID: row.GrantID}
}

// establish reaches the machine the row names, or places a new one.
func (o *owner) establish(ctx context.Context) error {
	if o.row.GrantID == "" {
		return o.place(ctx)
	}
	machine, err := o.deps.Hosts.Ensure(ctx, grantOf(o.row), machineSpec(o.row))
	switch {
	case errors.Is(err, ErrMachineLost):
		_ = o.deps.Hosts.Revoke(ctx, grantOf(o.row))
		return restartError{reason: "lost"}
	case errors.Is(err, ErrHostUnavailable):
		return o.unreachable(err)
	case errors.Is(err, ErrMachineConflict), errors.Is(err, ErrHostCapacity):
		// The host holds another grant under the ID, or has forgotten this
		// one and cannot place it again.
		return restartError{reason: "replaced"}
	case err != nil:
		return err
	}
	o.unreachableSince = time.Time{}
	if machine.Epoch() != o.row.Epoch {
		_ = machine.Revoke(ctx)
		return restartError{reason: "replaced"}
	}
	o.useMachine(ctx, machine)
	o.proj = projected{Cursor: o.row.ProjectionCursor, Versions: maps.Clone(o.row.ProjectedVersions)}
	if o.proj.Versions == nil {
		o.proj.Versions = map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion{}
	}
	o.observed, o.harvested = uint64(o.row.ObservationCursor), uint64(o.row.ObservationCursor) //nolint:gosec // G115: cursors are never negative.
	// A new owner takes the next fence before acting, so the calls of any
	// owner before it are refused from now on. A projection that never
	// started needs a running machine.
	state := ControlRunning
	if o.row.Desired == model.AttemptEnvironmentDesiredFrozen && o.row.State != model.AttemptEnvironmentCreating {
		state = ControlFrozen
	}
	if err := o.setControl(ctx, state); err != nil {
		o.dropMachine()
		return err
	}
	return nil
}

// useMachine starts acting through machine and following its state.
func (o *owner) useMachine(ctx context.Context, machine Machine) {
	o.dropMachine()
	o.machine, o.control, o.ready = machine, "", false
	o.resetSync()
	if watch, err := o.deps.Hosts.WatchGrant(ctx, GrantRef{Host: o.row.Host, GrantID: machine.GrantID()}); err == nil {
		o.watch = followGrant(ctx, watch)
	} else {
		o.report(fmt.Errorf("follow the execution machine: %w", err))
	}
}

func (o *owner) dropMachine() {
	o.stopObserving()
	if o.watch != nil {
		o.watch.stop()
	}
	o.watch, o.machine, o.control, o.ready = nil, nil, "", false
}

func (o *owner) resetSync() {
	o.proj = projected{Versions: map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion{}}
	o.ahead, o.hostCursor, o.echoes, o.diverged, o.drainNext, o.overwriteNext = nil, 0, map[int64]echo{}, 0, false, false
	o.journalDue = true
	o.pending, o.observed, o.harvested, o.firstPending, o.lastObserved = nil, 0, 0, time.Time{}, time.Time{}
	o.ignore, o.processLag = ignoreCache{}, false
}

// unreachable waits for a host that does not answer, and replaces its
// machine once it has been gone too long.
func (o *owner) unreachable(err error) error {
	now := o.deps.Now()
	if o.unreachableSince.IsZero() {
		o.unreachableSince = now
	}
	if now.Sub(o.unreachableSince) > o.deps.Timing.HostLossLimit {
		o.unreachableSince = time.Time{}
		return restartError{reason: "host_unreachable"}
	}
	return err
}

// place puts a new machine on the usable host with the most room for it.
func (o *owner) place(ctx context.Context) error {
	resources := o.row.Resources
	var candidates []HostReport
	for _, report := range o.deps.Hosts.Reports(ctx) {
		if report.Offers(o.row.Image, o.row.Network, resources) {
			candidates = append(candidates, report)
		}
	}
	slices.SortStableFunc(candidates, func(a, b HostReport) int {
		return cmp.Or(cmp.Compare(b.Free.MemoryBytes, a.Free.MemoryBytes), cmp.Compare(b.Free.CPUMillis, a.Free.CPUMillis),
			cmp.Compare(b.Free.DiskBytes, a.Free.DiskBytes), cmp.Compare(a.Name, b.Name))
	})
	grantID := fmt.Sprintf("%s.%d", o.attemptID, o.row.Incarnation)
	spec := machineSpec(o.row)
	for _, candidate := range candidates {
		grant := GrantRef{Host: candidate.Name, GrantID: grantID}
		machine, err := o.deps.Hosts.Ensure(ctx, grant, spec)
		if errors.Is(err, ErrMachineLost) {
			_ = o.deps.Hosts.Revoke(ctx, grant)
			machine, err = o.deps.Hosts.Ensure(ctx, grant, spec)
		}
		if err != nil {
			// Capacity, a draining host or one that stopped answering:
			// the next may take it.
			continue
		}
		if !o.row.WaitingSince.IsZero() {
			waited := o.deps.Now().Sub(o.row.WaitingSince)
			if err := o.gap(ctx, gapWaitingCapacity, o.row.WaitingSince, map[string]any{"waited_ms": waited.Milliseconds()}); err != nil {
				_ = machine.Revoke(ctx)
				return err
			}
		}
		o.row.Host, o.row.GrantID, o.row.Epoch = grant.Host, grant.GrantID, machine.Epoch()
		o.row.State, o.row.WaitingSince = model.AttemptEnvironmentCreating, time.Time{}
		o.row.ControlFence, o.row.ProjectionCursor, o.row.ObservationCursor, o.row.ProcessCursor = 0, 0, 0, 0
		o.row.ProjectedVersions, o.row.SyncPaths = nil, nil
		if err := o.save(ctx); err != nil {
			_ = machine.Revoke(ctx)
			return err
		}
		o.useMachine(ctx, machine)
		// A projection needs a running machine.
		return o.setControl(ctx, ControlRunning)
	}
	if o.row.State != model.AttemptEnvironmentWaitingCapacity {
		if o.row.WaitingSince.IsZero() {
			o.row.WaitingSince = o.deps.Now()
		}
		o.row.State = model.AttemptEnvironmentWaitingCapacity
		if err := o.save(ctx); err != nil {
			return err
		}
	}
	return errWaitCapacity
}

// setControl takes the next fence revision and moves the machine to state.
func (o *owner) setControl(ctx context.Context, state ControlState) error {
	if state == ControlFrozen && o.control != ControlFrozen {
		// A new owner can inherit a running machine before confirming control.
		prior := Fence{GrantID: o.row.GrantID, Epoch: o.row.Epoch, Revision: o.row.ControlFence}
		o.checkAssistantSessions(ctx, prior, true)
	}
	if state != ControlRunning {
		// A frozen or revoked machine serves no observation.
		o.stopObserving()
	}
	o.row.ControlFence++
	if err := o.save(ctx); err != nil {
		return err
	}
	fence := Fence{GrantID: o.row.GrantID, Epoch: o.row.Epoch, Revision: o.row.ControlFence}
	receipt, err := o.machine.Control(ctx, fence, state)
	if err != nil {
		return o.machineError(err)
	}
	if !receipt.Confirmed || receipt.Fence != fence || receipt.State != state {
		return errors.New("the execution machine did not confirm the control")
	}
	o.fence, o.control = fence, state
	if state == ControlRunning {
		o.assistantWake = false
		// A deadline that passed while frozen may still have a process.
		o.checkAssistantSessions(ctx, fence, false)
	}
	return nil
}

func (o *owner) checkAssistantSessions(ctx context.Context, fence Fence, stopping bool) {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := o.reconcileAssistantSessions(checkCtx, fence, stopping); err != nil {
		o.report(err)
	}
}

// machineError turns a machine failure into what the owner does next.
func (o *owner) machineError(err error) error {
	switch {
	case errors.Is(err, ErrMachineLost):
		return restartError{reason: "lost"}
	case errors.Is(err, ErrMachineRevoked):
		return restartError{reason: "revoked"}
	case errors.Is(err, ErrMachineLagged):
		return restartError{reason: "lagged"}
	case errors.Is(err, ErrHostUnavailable):
		o.dropMachine()
		return o.unreachable(err)
	case errors.Is(err, ErrMachineFrozen):
		// The machine froze on its own; the next round thaws it.
		o.stopObserving()
		o.control = ""
		return err
	default:
		return err
	}
}

// restart replaces the machine with a new incarnation, built from the
// workspace, and records the gap.
func (o *owner) restart(ctx context.Context, reason string) error {
	if o.machine != nil {
		_ = o.machine.Revoke(ctx)
	} else if o.row.GrantID != "" {
		_ = o.deps.Hosts.Revoke(ctx, grantOf(o.row))
	}
	o.dropMachine()
	o.resetSync()
	hadMachine := o.row.GrantID != ""
	o.row.Incarnation++
	o.row.Host, o.row.GrantID, o.row.Epoch = "", "", ""
	o.row.State, o.row.ControlFence, o.row.EvidenceSequence = model.AttemptEnvironmentLost, 0, 0
	o.row.ProjectionCursor, o.row.ObservationCursor, o.row.ProcessCursor, o.row.ProjectedVersions = 0, 0, 0, nil
	// A new machine holds only the workspace.
	o.row.SyncPaths = nil
	if err := o.save(ctx); err != nil {
		return err
	}
	if !hadMachine {
		return nil
	}
	return o.gap(ctx, gapRestarted, o.deps.Now(), map[string]any{"reason": reason})
}

// teardown revokes the machine of an ended environment and ends it.
func (o *owner) teardown(ctx context.Context) error {
	if o.row.GrantID != "" {
		var err error
		if o.machine != nil {
			err = o.machine.Revoke(ctx)
		} else {
			err = o.deps.Hosts.Revoke(ctx, grantOf(o.row))
		}
		if err != nil && !errors.Is(err, ErrMachineRevoked) && !errors.Is(err, ErrMachineLost) {
			// A host gone too long leaves nothing to revoke from here.
			if waited := o.unreachable(err); !errors.As(waited, new(restartError)) {
				return waited
			}
			o.report(fmt.Errorf("the ended environment's machine could not be revoked: %w", err))
		}
	}
	o.dropMachine()
	o.row.State, o.row.Host, o.row.GrantID, o.row.Epoch = model.AttemptEnvironmentEnded, "", "", ""
	if err := o.save(ctx); err != nil {
		return err
	}
	return errDone
}

// saveState records the state the machine is in, when it changed.
func (o *owner) saveState(ctx context.Context, state model.AttemptEnvironmentState) error {
	if o.row.State == state {
		return nil
	}
	o.row.State = state
	return o.save(ctx)
}

// save writes the owner's state under the lease. A fact that moved the row
// on since it was read is kept: the owner's fields go onto the newer row.
func (o *owner) save(ctx context.Context) error {
	for range 8 {
		saved, err := o.deps.Environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{
			Lease: o.lease, ExpectedRevision: o.row.Revision, Environment: *o.row,
		})
		if err == nil {
			o.row, o.saved = saved, *saved
			return nil
		}
		if errors.Is(err, store.ErrAttemptRuntimeLeaseLost) {
			return errLeaseLost
		}
		var conflict *store.ErrConflict
		if !errors.As(err, &conflict) || conflict.Constraint != "attempt_environment_revision" {
			return fmt.Errorf("save the attempt environment: %w", err)
		}
		fresh, err := o.deps.Environments.Get(ctx, o.attemptID)
		if err != nil {
			return fmt.Errorf("reread the attempt environment: %w", err)
		}
		copyOwnerState(fresh, o.row)
		o.row = fresh
	}
	return errors.New("the attempt environment kept changing")
}

// ownerStateChanged reports whether the owner's fields moved since they
// were last written.
func (o *owner) ownerStateChanged() bool {
	row, saved := o.row, o.saved
	return row.State != saved.State || row.Incarnation != saved.Incarnation || row.GrantID != saved.GrantID ||
		row.ControlFence != saved.ControlFence || row.ProjectionCursor != saved.ProjectionCursor ||
		row.ObservationCursor != saved.ObservationCursor || row.ProcessCursor != saved.ProcessCursor ||
		row.EvidenceSequence != saved.EvidenceSequence || !row.WaitingSince.Equal(saved.WaitingSince) ||
		!maps.Equal(row.ProjectedVersions, saved.ProjectedVersions) || row.SettleCompleted != saved.SettleCompleted ||
		!slices.Equal(row.SyncPaths, saved.SyncPaths)
}

// copyOwnerState puts the owner's fields of from onto to.
func copyOwnerState(to, from *model.AttemptEnvironment) {
	to.State, to.Incarnation = from.State, from.Incarnation
	to.Host, to.GrantID, to.Epoch = from.Host, from.GrantID, from.Epoch
	to.ControlFence, to.ProjectionCursor = from.ControlFence, from.ProjectionCursor
	to.ObservationCursor, to.ProcessCursor = from.ObservationCursor, from.ProcessCursor
	to.ProjectedVersions, to.EvidenceSequence = from.ProjectedVersions, from.EvidenceSequence
	to.WaitingSince, to.SyncPaths = from.WaitingSince, from.SyncPaths
	to.SettleCompleted = max(to.SettleCompleted, from.SettleCompleted)
	if to.Desired != model.AttemptEnvironmentDesiredEnded && to.State == model.AttemptEnvironmentEnded {
		to.State = model.AttemptEnvironmentPending
	}
}

// access is the owner's proof to the workspace and evidence.
func (o *owner) access() WorkspaceAccess {
	return WorkspaceAccess{AttemptID: o.attemptID, Incarnation: o.row.Incarnation, Fence: o.lease.Fence}
}

// gap records one execution.gap; detail names it under "gap".
func (o *owner) gap(ctx context.Context, gap string, at time.Time, detail map[string]any) error {
	fields := maps.Clone(detail)
	if fields == nil {
		fields = map[string]any{}
	}
	fields["gap"] = gap
	return o.evidence(ctx, model.AttemptEvidenceGap, 0, at, fields)
}

// evidence appends one record of the current incarnation. A sequence of 0
// takes the incarnation's next own number, past any a previous owner used
// without saving it; a fixed sequence, such as a command log position, that
// is already taken was stored before.
func (o *owner) evidence(ctx context.Context, kind model.AttemptEvidenceKind, sequence int64, at time.Time, detail any) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode %s evidence: %w", kind, err)
	}
	own := sequence == 0
	access := o.access()
	for range 64 {
		if own {
			sequence = o.row.EvidenceSequence + 1
		}
		_, appendErr := o.deps.Evidence.AppendAttemptEvidence(ctx, &store.ExamAttemptEvidenceAppend{
			Access: store.ExamAttemptWorkspaceEnvironmentAccess{
				AttemptID: access.AttemptID, Incarnation: access.Incarnation, Fence: access.Fence,
			},
			EvidenceID: model.NewIntegrityEvidenceID(), Kind: kind, Sequence: sequence, Detail: encoded,
			ObservedAt: model.TimeUTC(at),
		})
		var conflict *store.ErrConflict
		switch {
		case appendErr == nil:
			// A refusal over the attempt's bounds is counted by the store.
			if own {
				o.row.EvidenceSequence = sequence
			}
			return nil
		case errors.As(appendErr, &conflict) && conflict.Constraint == "attempt_evidence_sequence":
			if !own {
				return nil
			}
			o.row.EvidenceSequence = sequence
		case errors.As(appendErr, &conflict) && conflict.Constraint == "exam_attempt_state":
			// The attempt is sealed: nothing more is recorded.
			return nil
		case errors.As(appendErr, &conflict) && conflict.Constraint == "attempt_environment_owner":
			return errLeaseLost
		default:
			return fmt.Errorf("append %s evidence: %w", kind, appendErr)
		}
	}
	return fmt.Errorf("append %s evidence: no free sequence", kind)
}

func (o *owner) report(err error) {
	if o.deps.Report != nil && err != nil && !errors.Is(err, context.Canceled) {
		o.deps.Report(o.attemptID, err)
	}
}

// grantFollower keeps a machine's newest state from the host's watch and
// signals each change.
type grantFollower struct {
	cancel  context.CancelFunc
	changed chan struct{}
	done    chan struct{}

	mu     sync.Mutex
	latest GrantState
}

func followGrant(parent context.Context, watch GrantWatch) *grantFollower {
	ctx, cancel := context.WithCancel(parent)
	follower := &grantFollower{cancel: cancel, changed: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(follower.done)
		defer func() { _ = watch.Close() }()
		for {
			state, err := watch.Next(ctx)
			if err != nil {
				return
			}
			follower.mu.Lock()
			follower.latest = state
			follower.mu.Unlock()
			select {
			case follower.changed <- struct{}{}:
			default:
			}
		}
	}()
	return follower
}

func (f *grantFollower) state() GrantState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latest
}

func (f *grantFollower) stop() {
	f.cancel()
	<-f.done
}

// guestObserver long-polls one machine's observation and signals the owner
// when the guest has a change after the owner's position. It only wakes the
// owner: the owner reads the page itself, so the cursors stay its own. Once
// it has signaled, or a call failed, it waits for the owner to move it on,
// so a lasting failure never spins.
type guestObserver struct {
	cancel context.CancelFunc
	done   chan struct{}
	// ready signals the owner; resume says the owner moved the position.
	ready  chan struct{}
	resume chan struct{}

	mu    sync.Mutex
	fence Fence
	after uint64
}

func observeGuest(parent context.Context, machine Machine, wait time.Duration) *guestObserver {
	ctx, cancel := context.WithCancel(parent)
	g := &guestObserver{cancel: cancel, done: make(chan struct{}), ready: make(chan struct{}, 1),
		resume: make(chan struct{}, 1)}
	go func() {
		defer close(g.done)
		// The first position comes from the owner.
		select {
		case <-ctx.Done():
			return
		case <-g.resume:
		}
		for {
			fence, after := g.position()
			page, err := machine.Observe(ctx, ObserveRequest{Fence: fence, After: after, Limit: 1, Wait: wait})
			if ctx.Err() != nil {
				return
			}
			if err == nil && len(page.Observations) == 0 {
				continue
			}
			select {
			case g.ready <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return
			case <-g.resume:
			}
		}
	}()
	return g
}

func (g *guestObserver) position() (Fence, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fence, g.after
}

// moveTo has the next wait start after the owner's position.
func (g *guestObserver) moveTo(fence Fence, after uint64) {
	g.mu.Lock()
	g.fence, g.after = fence, after
	g.mu.Unlock()
	select {
	case g.resume <- struct{}{}:
	default:
	}
}

func (g *guestObserver) stop() {
	g.cancel()
	<-g.done
}
