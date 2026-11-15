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
}

func newRig(t *testing.T, timing environment.OwnerTiming) *rig {
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
		timing = environment.OwnerTiming{Poll: tick, HarvestQuiet: 10 * time.Millisecond, HarvestMax: 40 * time.Millisecond,
			Idle: 50 * time.Millisecond, Retry: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond}
	}
	factory, err := environment.NewOwners(environment.OwnerDependencies{
		Environments: r.environments, Evidence: r.evidence, Workspace: r.workspace, Hosts: hosts, Timing: timing,
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
	r := newRig(t, environment.OwnerTiming{Poll: tick, HarvestQuiet: 300 * time.Millisecond, HarvestMax: time.Second,
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
	// The machine cannot take the journal's version over its own in place,
	// so it is replaced from the Workspace.
	require.Eventually(t, func() bool {
		row := r.environments.snapshot()
		return row.Incarnation == 1 && row.State == model.AttemptEnvironmentRunning
	}, eventually, tick)
	require.Eventually(t, func() bool { return r.guest("src/main.py") == "editor\n" }, eventually, tick)
	require.Equal(t, 1, r.evidence.gaps("environment_restarted"))
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
