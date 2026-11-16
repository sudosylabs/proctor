// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/memory"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/platform/executionhost"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	eventually = 5 * time.Second
	tick       = 5 * time.Millisecond
)

// rig is one owner over a real in-memory execution host and fakes of the
// environment row, the Workspace and evidence.
type rig struct {
	t            *testing.T
	attemptID    model.ExamAttemptID
	host         *memory.Host
	hosts        *executionhost.Hosts
	environments *environmentsFake
	workspace    *workspaceFake
	evidence     *evidenceFake
	wake         chan struct{}
	done         chan error
	cancel       context.CancelFunc

	mu      sync.Mutex
	reports []error
	settled []model.ExamAttemptID
}

// Settled records the owner's settle signals.
func (r *rig) Settled(attemptID model.ExamAttemptID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled = append(r.settled, attemptID)
}

func (r *rig) settleSignals() []model.ExamAttemptID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.settled)
}

func newRig(t *testing.T, timing environment.OwnerTiming, wrap ...func(environment.Hosts) environment.Hosts) *rig {
	t.Helper()
	return newSeededRig(t, timing, nil, wrap...)
}

// newSeededRig is newRig over a Workspace that seed adds to before the
// owner starts.
func newSeededRig(t *testing.T, timing environment.OwnerTiming, seed func(*workspaceFake),
	wrap ...func(environment.Hosts) environment.Hosts) *rig {
	t.Helper()
	host, err := memory.New(memory.Config{Images: []execenv.Image{"python"}, WorkDir: t.TempDir()})
	require.NoError(t, err)
	hosts, err := executionhost.New(executionhost.Config{
		Hosts: []config.ExecutionHost{{Name: "exec-a"}},
		Dial: func(context.Context, config.ExecutionHost) (execenv.Host, error) {
			return host, nil
		},
		AllowUnisolated: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = hosts.Close() })
	now := time.Now().UTC()
	r := &rig{t: t, attemptID: model.NewExamAttemptID(), host: host, hosts: hosts, workspace: newWorkspaceFake(), evidence: &evidenceFake{},
		wake: make(chan struct{}, 1), done: make(chan error, 1)}
	r.environments = &environmentsFake{row: model.AttemptEnvironment{
		AttemptID: r.attemptID, SittingID: model.NewExamSittingID(), ProfileID: model.NewEnvironmentProfileID(),
		Image: "python", Resources: model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30},
		Network: model.EnvironmentNetworkNone, IdleWindow: 10 * time.Minute, Terminals: 2,
		Desired: model.AttemptEnvironmentDesiredRunning, State: model.AttemptEnvironmentPending, Generation: 1,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}
	if timing == (environment.OwnerTiming{}) {
		timing = environment.OwnerTiming{ObserveWait: time.Second, HarvestQuiet: 10 * time.Millisecond, HarvestMax: 40 * time.Millisecond,
			Idle: 50 * time.Millisecond, Retry: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond}
	}
	var ported environment.Hosts = hosts
	for _, wrapped := range wrap {
		ported = wrapped(ported)
	}
	factory, err := environment.NewOwners(environment.OwnerDependencies{
		Environments: r.environments, Evidence: r.evidence, Workspace: r.workspace, Hosts: ported, Timing: timing, Settled: r,
		Report: func(_ model.ExamAttemptID, err error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reports = append(r.reports, err)
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	t.Cleanup(func() {
		cancel()
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, report := range r.reports {
			t.Logf("reported: %v", report)
		}
	})
	r.workspace.mkdir("src")
	r.workspace.edit("src/main.py", "print(1)\n")
	if seed != nil {
		seed(r.workspace)
	}
	go func() {
		r.done <- factory(r.attemptID).Run(ctx, store.AttemptRuntimeLease{AttemptID: r.attemptID, NodeID: "node-a", Fence: 7,
			ExpiresAt: time.Now().Add(time.Minute)}, r.wake)
	}()
	return r
}

func (r *rig) grant() execenv.ID {
	return execenv.ID(fmt.Sprintf("%s.%d", r.attemptID, r.environments.snapshot().Incarnation))
}

func (r *rig) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// running waits for the machine to hold the whole Workspace.
func (r *rig) running() {
	r.t.Helper()
	require.Eventually(r.t, func() bool {
		row := r.environments.snapshot()
		return row.State == model.AttemptEnvironmentRunning && row.ProjectionCursor == r.workspace.cursor()
	}, eventually, tick, "the machine is running and holds the Workspace")
}

func (r *rig) guest(name string) string {
	body, err := r.host.ReadGuest(context.Background(), r.grant(), name)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(body)
}

func (r *rig) workspaceHolds(name, body string) func() bool {
	return func() bool {
		held, ok := r.workspace.file(name)
		return ok && held == body
	}
}

func TestOwnerSyncsTheWorkspaceBothWays(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	ctx := t.Context()
	r.running()
	require.Equal(t, "print(1)\n", r.guest("src/main.py"), "the Workspace is projected into the machine")
	grant := r.grant()

	// Guest changes reach the Workspace, written by the environment.
	require.NoError(t, r.host.WriteGuest(ctx, grant, "src/out.txt", []byte("hello")))
	require.Eventually(t, r.workspaceHolds("src/out.txt", "hello"), eventually, tick)
	require.Equal(t, model.NewEnvironmentWorkspaceAuthor(0, ""), r.workspace.authors("src/out.txt")[0])
	require.NoError(t, r.host.WriteGuest(ctx, grant, "src/main.py", []byte("print(2)\n")))
	require.Eventually(t, r.workspaceHolds("src/main.py", "print(2)\n"), eventually, tick)
	r.running()

	// Ignored paths stay on the machine.
	require.NoError(t, r.host.MkdirGuest(ctx, grant, "node_modules"))
	require.NoError(t, r.host.WriteGuest(ctx, grant, "node_modules/x.js", []byte("x")))
	require.NoError(t, r.host.MoveGuest(ctx, grant, "src/out.txt", "src/renamed.txt"))
	require.Eventually(t, r.workspaceHolds("src/renamed.txt", "hello"), eventually, tick)
	_, held := r.workspace.file("node_modules/x.js")
	require.False(t, held, "an ignored path is never harvested")
	require.NoError(t, r.host.RemoveGuest(ctx, grant, "src/renamed.txt"))
	require.Eventually(t, func() bool {
		_, held := r.workspace.file("src/renamed.txt")
		return !held
	}, eventually, tick)

	// The editor's changes reach the machine, after the echoes of the
	// guest's own.
	r.workspace.edit("src/main.py", "print(3)\n")
	r.poke()
	require.Eventually(t, func() bool { return r.guest("src/main.py") == "print(3)\n" }, eventually, tick)
	r.running()

	// The command log is stored as evidence.
	env, err := r.host.Ensure(ctx, execenv.Spec{ID: grant, Image: "python", Network: execenv.NetworkNone,
		Resources:   execenv.Resources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30},
		IdleTimeout: 10 * time.Minute, IdleAction: execenv.IdleSuspend})
	require.NoError(t, err, "the owner ensured exactly the spec its row derives")
	row := r.environments.snapshot()
	fence := execenv.ProjectionFence{ExecutionGrantID: grant, EnvironmentEpoch: row.Epoch,
		FenceRevision: uint64(row.ControlFence)} //nolint:gosec // G115: fences are never negative.
	_, err = env.Start(ctx, fence, execenv.ProcessSpec{Tag: "command:1", Argv: []string{"echo", "hi"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(r.evidence.details(model.AttemptEvidenceProcess)) > 0 }, eventually, tick)
	require.Eventually(t, func() bool { return r.environments.snapshot().ProcessCursor > 0 }, eventually, tick)

	// A frozen environment is drained and frozen; an ended one is revoked.
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	r.poke()
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)
	grants, err := r.host.ListGrants(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, execenv.GrantFrozen, grants[0].State)

	r.environments.desire(model.AttemptEnvironmentDesiredEnded)
	r.poke()
	select {
	case runErr := <-r.done:
		require.NoError(t, runErr)
	case <-time.After(eventually):
		t.Fatal("the owner of an ended environment did not finish")
	}
	require.Equal(t, model.AttemptEnvironmentEnded, r.environments.snapshot().State)
	grants, err = r.host.ListGrants(ctx)
	require.NoError(t, err)
	require.Empty(t, grants, "the machine is revoked")
	require.Empty(t, r.evidence.details(model.AttemptEvidenceGap))
}

func TestOwnerReplacesALostMachine(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.running()
	lost := r.grant()
	require.True(t, r.host.Lose(lost))
	require.Eventually(t, func() bool {
		row := r.environments.snapshot()
		return row.Incarnation == 1 && row.State == model.AttemptEnvironmentRunning && row.ProjectionCursor == r.workspace.cursor()
	}, eventually, tick, "a new incarnation is built from the Workspace")
	require.Equal(t, "print(1)\n", r.guest("src/main.py"))
	require.Equal(t, 1, r.evidence.gaps("environment_restarted"))
}

func TestOwnerWaitsForCapacityAndRecordsTheWait(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.host.SetDraining(true)
	require.Eventually(t, func() bool {
		row := r.environments.snapshot()
		return row.State == model.AttemptEnvironmentWaitingCapacity && !row.WaitingSince.IsZero()
	}, eventually, tick)
	r.host.SetDraining(false)
	r.running()
	require.True(t, r.environments.snapshot().WaitingSince.IsZero())
	require.Equal(t, 1, r.evidence.gaps("environment_waiting_capacity"))
}

func TestOwnerLetsTheJournalWinAConflict(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{ObserveWait: time.Second, HarvestQuiet: 300 * time.Millisecond, HarvestMax: time.Second,
		Idle: 50 * time.Millisecond, Retry: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond})
	r.running()
	// The guest and the editor change the same file; the editor's change
	// reaches the journal first.
	require.NoError(t, r.host.WriteGuest(t.Context(), r.grant(), "src/main.py", []byte("guest\n")))
	r.workspace.edit("src/main.py", "editor\n")
	r.poke()
	require.Eventually(t, func() bool { return len(r.evidence.details(model.AttemptEvidenceOverwritten)) == 1 }, eventually, tick)
	overwritten := r.evidence.details(model.AttemptEvidenceOverwritten)[0]
	require.EqualValues(t, 1, overwritten["overwritten"])
	held, _ := r.workspace.file("src/main.py")
	require.Equal(t, "editor\n", held, "journal order wins")
	// The journal's version replaces the guest's in place: the machine is
	// kept.
	require.Eventually(t, func() bool { return r.guest("src/main.py") == "editor\n" }, eventually, tick)
	r.running()
	require.Zero(t, r.environments.snapshot().Incarnation)
	require.Zero(t, r.evidence.gaps("environment_restarted"))
	require.Len(t, r.evidence.details(model.AttemptEvidenceOverwritten), 1, "the lost change is recorded once")
	require.Equal(t, []model.AttemptEnvironmentSyncPath{{Path: "src/main.py", Status: model.AttemptEnvironmentPathRefused,
		Reason: "exam.attempt.workspace.content_conflict"}}, r.environments.snapshot().SyncPaths, "the lost path is refused")

	// The machine goes on syncing both ways.
	require.NoError(t, r.host.WriteGuest(t.Context(), r.grant(), "src/main.py", []byte("guest again\n")))
	require.Eventually(t, r.workspaceHolds("src/main.py", "guest again\n"), eventually, tick)
	require.Eventually(t, func() bool { return len(r.environments.snapshot().SyncPaths) == 0 }, eventually, tick,
		"a later stored change syncs the path again")
}

func TestOwnerGivesANewWorkspaceTheDefaultIgnoreFile(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.running()
	held, ok := r.workspace.file(environment.IgnoreFile)
	require.True(t, ok, "the Workspace has the default ignore file")
	require.Equal(t, string(environment.DefaultIgnoreFile()), held)
	require.Equal(t, []model.AttemptWorkspaceAuthor{model.NewEnvironmentWorkspaceAuthor(0, "")}, r.workspace.authors(environment.IgnoreFile))
	require.Equal(t, held, r.guest(environment.IgnoreFile), "the machine holds it")

	// A new incarnation never writes it again, even once the candidate
	// deleted it.
	r.workspace.mu.Lock()
	item := r.workspace.byPath(environment.IgnoreFile)
	r.workspace.commit(model.AttemptWorkspaceJournalEntry{EntryID: item.id, EntryKind: item.kind,
		Operation: model.AttemptWorkspaceMutationDeleteEntry, OldPath: item.path,
		Author: model.NewCandidateWorkspaceAuthor(model.NewAttemptConnectionID())}, nil)
	r.workspace.mu.Unlock()
	r.poke()
	r.running()
	require.True(t, r.host.Lose(r.grant()))
	require.Eventually(t, func() bool {
		row := r.environments.snapshot()
		return row.Incarnation == 1 && row.State == model.AttemptEnvironmentRunning && row.ProjectionCursor == r.workspace.cursor()
	}, eventually, tick)
	_, ok = r.workspace.file(environment.IgnoreFile)
	require.False(t, ok, "the candidate's deletion stands")
}

func TestOwnerKeepsTheStartersIgnoreFile(t *testing.T) {
	t.Parallel()
	r := newSeededRig(t, environment.OwnerTiming{}, func(workspace *workspaceFake) {
		workspace.edit(environment.IgnoreFile, "secret/\n")
	})
	r.running()
	held, _ := r.workspace.file(environment.IgnoreFile)
	require.Equal(t, "secret/\n", held, "a Workspace's own ignore file is never replaced")
	require.Len(t, r.workspace.authors(environment.IgnoreFile), 1)
}

func TestOwnerSignalsACompletedSettle(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.running()
	require.NoError(t, r.host.WriteGuest(t.Context(), r.grant(), "src/last.txt", []byte("last")))
	r.environments.mu.Lock()
	r.environments.row.SettleRequested++
	r.environments.row.SettleRequestedAt = time.Now()
	r.environments.row.Revision++
	r.environments.mu.Unlock()
	r.poke()
	require.Eventually(t, func() bool { return len(r.settleSignals()) == 1 }, eventually, tick)
	require.Equal(t, []model.ExamAttemptID{r.attemptID}, r.settleSignals())
	row := r.environments.snapshot()
	require.Equal(t, row.SettleRequested, row.SettleCompleted, "the signal follows the recorded completion")
	held, _ := r.workspace.file("src/last.txt")
	require.Equal(t, "last", held, "the settle harvested the guest's last change")
}

func TestOwnerJournalsAChangeByTheProcessThatWroteIt(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	ctx := t.Context()
	r.running()
	grant := r.grant()
	env, err := r.host.Ensure(ctx, execenv.Spec{ID: grant, Image: "python", Network: execenv.NetworkNone,
		Resources:   execenv.Resources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30},
		IdleTimeout: 10 * time.Minute, IdleAction: execenv.IdleSuspend})
	require.NoError(t, err)
	row := r.environments.snapshot()
	fence := execenv.ProjectionFence{ExecutionGrantID: grant, EnvironmentEpoch: row.Epoch,
		FenceRevision: uint64(row.ControlFence)} //nolint:gosec // G115: fences are never negative.
	_, err = env.Start(ctx, fence, execenv.ProcessSpec{Tag: "terminal:1", PTY: &execenv.Window{Cols: 80, Rows: 24}})
	require.NoError(t, err)
	_, err = env.WriteInput(ctx, fence, "terminal:1", 0, []byte("echo hi > src/tagged.txt\n"))
	require.NoError(t, err)
	// A change by no started process is the incarnation's alone, and never
	// shares a batch with the terminal's.
	require.NoError(t, r.host.WriteGuest(ctx, grant, "src/plain.txt", []byte("plain")))
	require.Eventually(t, r.workspaceHolds("src/tagged.txt", "hi\n"), eventually, tick)
	require.Eventually(t, r.workspaceHolds("src/plain.txt", "plain"), eventually, tick)
	require.Equal(t, []model.AttemptWorkspaceAuthor{model.NewEnvironmentWorkspaceAuthor(0, "terminal:1")},
		r.workspace.authors("src/tagged.txt"))
	require.Equal(t, []model.AttemptWorkspaceAuthor{model.NewEnvironmentWorkspaceAuthor(0, "")}, r.workspace.authors("src/plain.txt"))
}

func TestNewOwnersRefusesMissingDependencies(t *testing.T) {
	t.Parallel()
	_, err := environment.NewOwners(environment.OwnerDependencies{})
	require.Error(t, err)
}

func TestOwnerReplacesTheMachineAManagerNamed(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.running()
	old := r.grant()
	r.environments.mu.Lock()
	r.environments.row.ReplaceBefore = r.environments.row.Incarnation + 1
	r.environments.row.Revision++
	r.environments.mu.Unlock()
	r.poke()
	require.Eventually(t, func() bool {
		row := r.environments.snapshot()
		return row.Incarnation == 1 && !row.ReplacePending() && row.State == model.AttemptEnvironmentRunning &&
			row.ProjectionCursor == r.workspace.cursor()
	}, eventually, tick, "the owner builds a new machine from the Workspace")
	require.NotEqual(t, old, r.grant())
	require.Equal(t, "print(1)\n", r.guest("src/main.py"))
	require.Equal(t, 1, r.evidence.gaps("environment_restarted"), "the replacement is recorded as a restart")
}

func TestOwnerWakesOnGuestChangesWithoutPolling(t *testing.T) {
	t.Parallel()
	// No backstop fires within the test: only the long-polled observation
	// can bring the guest's change in.
	r := newRig(t, environment.OwnerTiming{HarvestQuiet: 10 * time.Millisecond, HarvestMax: 40 * time.Millisecond,
		Idle: time.Hour, Retry: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond})
	r.running()
	require.NoError(t, r.host.WriteGuest(t.Context(), r.grant(), "src/woke.txt", []byte("woke")))
	require.Eventually(t, r.workspaceHolds("src/woke.txt", "woke"), eventually, tick)
	r.workspace.edit("src/main.py", "print(4)\n")
	r.poke()
	require.Eventually(t, func() bool { return r.guest("src/main.py") == "print(4)\n" }, eventually, tick)
}

// toldHosts reports the machine states a test tells it in place of the
// host's own watch: the in-memory host suspends only after an idle window
// longer than a test.
type toldHosts struct {
	environment.Hosts
	states chan environment.GrantState
}

func (h toldHosts) WatchGrant(context.Context, string, string) (environment.GrantWatch, error) {
	return toldWatch(h.states), nil
}

type toldWatch chan environment.GrantState

func (w toldWatch) Next(ctx context.Context) (environment.GrantState, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case state := <-w:
		return state, nil
	}
}

func (toldWatch) Close() error { return nil }

func TestOwnerRecordsASuspendedMachineAndThawsIt(t *testing.T) {
	t.Parallel()
	states := make(chan environment.GrantState)
	r := newRig(t, environment.OwnerTiming{}, func(hosts environment.Hosts) environment.Hosts {
		return toldHosts{Hosts: hosts, states: states}
	})
	ctx := t.Context()
	r.running()
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	r.poke()
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)

	// The host saves the frozen machine once it has been idle.
	states <- environment.GrantSuspended
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentSuspended }, eventually, tick)

	// A thaw resumes the same machine where it was.
	r.environments.desire(model.AttemptEnvironmentDesiredRunning)
	r.poke()
	states <- environment.GrantRunning
	r.running()
	grants, err := r.host.ListGrants(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, execenv.GrantRunning, grants[0].State)
	require.Zero(t, r.environments.snapshot().Incarnation)
	require.NoError(t, r.host.WriteGuest(ctx, r.grant(), "src/after.txt", []byte("after")))
	require.Eventually(t, r.workspaceHolds("src/after.txt", "after"), eventually, tick)
}

func TestOwnerReportsThePathsItDidNotStore(t *testing.T) {
	t.Parallel()
	r := newSeededRig(t, environment.OwnerTiming{ObserveWait: time.Second, HarvestQuiet: 300 * time.Millisecond, HarvestMax: time.Second,
		Idle: 50 * time.Millisecond, Retry: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond}, func(workspace *workspaceFake) {
		workspace.edit(environment.IgnoreFile, "")
	})
	ctx := t.Context()
	r.running()
	// The guest writes a file; before it is harvested, the candidate's
	// ignore file comes to name it.
	require.NoError(t, r.host.WriteGuest(ctx, r.grant(), "a.o", []byte("object")))
	r.workspace.edit(environment.IgnoreFile, "*.o\n")
	r.poke()
	ignored := []model.AttemptEnvironmentSyncPath{{Path: "a.o", Status: model.AttemptEnvironmentPathIgnored,
		Reason: model.AttemptEnvironmentIgnoredByFile}}
	require.Eventually(t, func() bool { return slices.Equal(ignored, r.environments.snapshot().SyncPaths) }, eventually, tick,
		"the path is ignored by the Workspace's ignore file")
	_, held := r.workspace.file("a.o")
	require.False(t, held)

	// A path the guest removes has no status left.
	require.NoError(t, r.host.RemoveGuest(ctx, r.grant(), "a.o"))
	require.Eventually(t, func() bool { return len(r.environments.snapshot().SyncPaths) == 0 }, eventually, tick)
}
