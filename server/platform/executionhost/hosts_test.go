// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package executionhost

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/memory"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
)

func memoryHosts(t *testing.T, allowUnisolated bool) (*Hosts, *memory.Host, *int) {
	t.Helper()
	host, err := memory.New(memory.Config{Images: []execenv.Image{"python"}, WorkDir: t.TempDir()})
	require.NoError(t, err)
	dials := 0
	hosts, err := New(Config{
		Hosts: []config.ExecutionHost{{Name: "exec-a"}},
		Dial: func(context.Context, config.ExecutionHost) (execenv.Host, error) {
			dials++
			return host, nil
		},
		AllowUnisolated: allowUnisolated,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = hosts.Close() })
	return hosts, host, &dials
}

var testSpec = environment.MachineSpec{
	GrantID: "attempt.0", Image: "python", Network: model.EnvironmentNetworkNone,
	Resources: model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30},
}

func TestHostsReportPlaceAndTranslate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hosts, host, dials := memoryHosts(t, true)
	require.Zero(t, *dials, "nothing is dialed before a host is used")

	reports := hosts.Reports(ctx)
	require.Len(t, reports, 1)
	require.Equal(t, "exec-a", reports[0].Name)
	require.True(t, reports[0].Offers("python", model.EnvironmentNetworkNone, testSpec.Resources))
	require.False(t, reports[0].Offers("node", model.EnvironmentNetworkNone, testSpec.Resources), "an image the host lacks")
	hosts.Reports(ctx)
	require.Equal(t, 1, *dials, "one client per host")

	machine, err := hosts.Ensure(ctx, "exec-a", testSpec)
	require.NoError(t, err)
	require.Equal(t, "attempt.0", machine.GrantID())
	again, err := hosts.Ensure(ctx, "exec-a", testSpec)
	require.NoError(t, err)
	require.Equal(t, machine.Epoch(), again.Epoch(), "ensure is idempotent")
	other := testSpec
	other.Image = "node"
	_, err = hosts.Ensure(ctx, "exec-a", other)
	require.ErrorIs(t, err, environment.ErrMachineConflict, "another spec under the same grant")
	_, err = hosts.Ensure(ctx, "exec-b", testSpec)
	require.ErrorIs(t, err, environment.ErrHostUnavailable, "an unknown host")

	fence := environment.Fence{GrantID: machine.GrantID(), Epoch: machine.Epoch(), Revision: 1}
	receipt, err := machine.Control(ctx, fence, environment.ControlRunning)
	require.NoError(t, err)
	require.True(t, receipt.Confirmed)
	require.Equal(t, fence, receipt.Fence)
	_, err = machine.Observe(ctx, environment.Fence{GrantID: machine.GrantID(), Epoch: machine.Epoch(), Revision: 0}, 0, 1)
	require.Error(t, err, "a call outside the fence is refused")

	content, err := machine.UploadContent(ctx, fence, strings.NewReader("print(1)\n"))
	require.NoError(t, err)
	_, err = machine.InitializeProjection(ctx, environment.ProjectionInit{
		Fence: fence, MutationID: "init", Cursor: 1, IgnoreFile: ".proctorignore", IgnoreFallback: []string{"build/"},
		Entries: []environment.ProjectionEntry{{EntryID: model.NewAttemptWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile, Path: "main.py",
			Version: "v1", Content: &content}},
	})
	require.NoError(t, err)
	body, err := host.ReadGuest(ctx, "attempt.0", "main.py")
	require.NoError(t, err)
	require.Equal(t, "print(1)\n", string(body))

	require.NoError(t, host.WriteGuest(ctx, "attempt.0", "out.txt", []byte("hello")))
	page, err := machine.Observe(ctx, fence, 0, 16)
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	observed := page.Observations[0]
	require.Equal(t, environment.ObserveCreateFile, observed.Operation)
	require.Equal(t, "out.txt", observed.Path)
	reader, err := machine.OpenObservedContent(ctx, fence, *observed.Content)
	require.NoError(t, err)
	read, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, "hello", string(read))
	require.NoError(t, machine.AcknowledgeObservations(ctx, fence, observed.HostSequence))

	processes, err := machine.ObserveProcesses(ctx, fence, 0, environment.MaxProcessPage)
	require.NoError(t, err)
	require.Empty(t, processes.Events)

	watch, err := hosts.WatchGrant(ctx, "exec-a", machine.GrantID())
	require.NoError(t, err)
	state, err := watch.Next(ctx)
	require.NoError(t, err)
	require.Equal(t, environment.GrantRunning, state)

	require.True(t, host.Lose("attempt.0"))
	_, err = machine.Observe(ctx, fence, 0, 16)
	require.ErrorIs(t, err, environment.ErrMachineLost)
	for {
		state, err = watch.Next(ctx)
		require.NoError(t, err)
		if state == environment.GrantLost {
			break
		}
	}
	require.NoError(t, watch.Close())
	_, err = hosts.Ensure(ctx, "exec-a", testSpec)
	require.ErrorIs(t, err, environment.ErrMachineLost, "a lost grant stays lost until revoked")
	require.NoError(t, hosts.Revoke(ctx, "exec-a", "attempt.0"))
	fresh, err := hosts.Ensure(ctx, "exec-a", testSpec)
	require.NoError(t, err)
	require.NotEqual(t, machine.Epoch(), fresh.Epoch())
}

func TestHostsRefuseAnUnisolatedHostInProduction(t *testing.T) {
	t.Parallel()
	hosts, _, _ := memoryHosts(t, false)
	require.Empty(t, hosts.Reports(t.Context()), "a host that shares the kernel is never offered")
	_, err := hosts.Ensure(t.Context(), "exec-a", testSpec)
	require.ErrorIs(t, err, environment.ErrHostUnavailable)
}

func TestHostsKeepExecenvDetailOutOfErrors(t *testing.T) {
	t.Parallel()
	err := translate("ensure", execenv.Error("ensure", &execenv.CapacityError{Resource: execenv.ResourceMemory, Requested: 1, Free: 0}))
	require.ErrorIs(t, err, environment.ErrHostCapacity)
	require.NotContains(t, err.Error(), "memory")
	require.NotErrorIs(t, err, execenv.ErrCapacity, "no execenv error leaves the adapter")
}

func TestIgnoreRulesFollowTheWorkspaceCopy(t *testing.T) {
	t.Parallel()
	hosts, _, _ := memoryHosts(t, true)
	rules, err := hosts.IgnoreRules(".proctorignore", []string{"build/"})
	require.NoError(t, err)
	fallback := rules.Policy(nil, false)
	require.False(t, fallback.Observed("build", true))
	require.True(t, fallback.Observed("main.py", false))
	own := rules.Policy([]byte("*.log\n"), true)
	require.True(t, own.Observed("build", true), "the Workspace's own copy replaces the defaults")
	require.False(t, own.Observed("run.log", false))
	require.True(t, own.Observed(".proctorignore", false), "the ignore file always syncs")
	_, err = New(Config{Hosts: []config.ExecutionHost{{Name: "a"}, {Name: "a"}}})
	require.Error(t, err, "a host name is configured once")
}
