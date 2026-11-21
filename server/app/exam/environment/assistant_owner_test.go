// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ownerSessions struct {
	store.AssistantTranscriptStore
	mu      sync.Mutex
	rows    []model.AssistantSession
	ends    []store.AssistantSessionEnd
	listErr error
	failEnd string
}

func (s *ownerSessions) ListSessions(_ context.Context, query store.AssistantSessionQuery) ([]model.AssistantSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if query.EnvironmentOwner == nil {
		return nil, errors.New("missing owner authority")
	}
	return slices.Clone(s.rows), s.listErr
}

func (s *ownerSessions) EndSession(_ context.Context, end *store.AssistantSessionEnd) (*store.AssistantSessionEndResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ends = append(s.ends, *end)
	if end.Tag == s.failEnd {
		return nil, errors.New("first session could not be recorded")
	}
	for i := range s.rows {
		if s.rows[i].Tag == end.Tag {
			s.rows[i].State = "ended"
		}
	}
	return &store.AssistantSessionEndResult{Changed: true, Record: &model.AssistantRecord{}}, nil
}

func (s *ownerSessions) finished() []store.AssistantSessionEnd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ends)
}

func (r *rig) assistantMachine() (environment.Machine, environment.Fence) {
	r.t.Helper()
	row := r.environments.snapshot()
	machine, err := r.hosts.Ensure(r.t.Context(), environment.GrantRef{Host: row.Host, GrantID: row.GrantID}, environment.MachineSpec{
		Image: row.Image, Resources: row.Resources, Network: row.Network, IdleSuspend: row.IdleWindow,
	})
	require.NoError(r.t, err)
	return machine, environment.Fence{GrantID: row.GrantID, Epoch: row.Epoch, Revision: row.ControlFence}
}

func (r *rig) startAssistantProcesses(s *ownerSessions, tags ...string) environment.Machine {
	r.t.Helper()
	machine, fence := r.assistantMachine()
	window := environment.AssistantSessionWindow
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tag := range tags {
		process, err := machine.StartProcess(r.t.Context(), fence, environment.ProcessSpec{Tag: model.AttemptEnvironmentProcessTag(tag),
			Argv: []string{"/bin/sh", "-c", "cat"}, Streams: environment.ProcessStreamsPTY, PTY: &window, Timeout: time.Minute})
		require.NoError(r.t, err)
		require.True(r.t, process.Running)
		s.rows = append(s.rows, model.AssistantSession{Tag: tag, State: "running", StartedAt: time.Now(), ReservedMilliseconds: 60000})
	}
	return machine
}

func waitAssistantProcessesKilled(t *testing.T, machine environment.Machine, tags ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		processes, err := machine.ListProcesses(t.Context())
		if err != nil {
			return false
		}
		for _, tag := range tags {
			index := slices.IndexFunc(processes, func(process environment.ProcessInfo) bool { return string(process.Tag) == tag })
			if index < 0 || processes[index].Running || processes[index].Exit == nil || processes[index].Exit.Signal != "KILL" {
				return false
			}
		}
		return true
	}, eventually, tick)
}

func TestOwnerEndsBothAssistantSessionsWhenTheConnectionClosesBetweenTurns(t *testing.T) {
	t.Parallel()
	sessions := &ownerSessions{}
	r := newPreparedRig(t, environment.OwnerTiming{}, func(r *rig) { r.sessions = sessions })
	r.running()
	machine := r.startAssistantProcesses(sessions, "assistant:1", "assistant:2")
	sessions.mu.Lock()
	for i := range sessions.rows {
		sessions.rows[i].AccessEnded = true
	}
	sessions.mu.Unlock()
	r.poke()
	waitAssistantProcessesKilled(t, machine, "assistant:1", "assistant:2")
	require.Eventually(t, func() bool { return len(sessions.finished()) == 2 }, eventually, tick)
	for _, end := range sessions.finished() {
		require.Equal(t, "ended", end.Outcome)
		require.Equal(t, "KILL", end.Signal)
		require.Nil(t, end.ExitCode)
		require.EqualValues(t, 7, end.EnvironmentOwner.Fence)
	}
	require.Equal(t, model.AttemptEnvironmentRunning, r.environments.snapshot().State)
}

func TestOwnerFreezesInheritedAssistantSessionsAndContinuesAfterAnEndFailure(t *testing.T) {
	t.Parallel()
	sessions := &ownerSessions{failEnd: "assistant:1"}
	r := newPreparedRig(t, environment.OwnerTiming{}, func(r *rig) { r.sessions = sessions })
	r.running()
	machine := r.startAssistantProcesses(sessions, "assistant:1", "assistant:2")
	r.cancel()
	require.NoError(t, <-r.done)
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.newOwner(r.attemptID).Run(ctx, store.AttemptRuntimeLease{AttemptID: r.attemptID, NodeID: "node-b", Fence: 8,
			ExpiresAt: time.Now().Add(time.Minute)}, r.wake)
	}()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)
	ends := sessions.finished()
	require.Len(t, ends, 2)
	require.Equal(t, "assistant:2", ends[1].Tag)
	require.EqualValues(t, 8, ends[1].EnvironmentOwner.Fence)
	r.environments.desire(model.AttemptEnvironmentDesiredRunning)
	r.poke()
	r.running()
	waitAssistantProcessesKilled(t, machine, "assistant:1", "assistant:2")
}

func TestOwnerConfirmsThawWhenAssistantSessionReconciliationFails(t *testing.T) {
	t.Parallel()
	sessions := &ownerSessions{}
	r := newPreparedRig(t, environment.OwnerTiming{}, func(r *rig) { r.sessions = sessions })
	r.running()
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	r.poke()
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)
	before := r.environments.snapshot().ControlFence
	unavailable := errors.New("assistant sessions unavailable")
	sessions.mu.Lock()
	sessions.listErr = unavailable
	sessions.mu.Unlock()
	r.environments.desire(model.AttemptEnvironmentDesiredRunning)
	r.poke()
	r.running()
	require.Equal(t, before+1, r.environments.snapshot().ControlFence)
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Contains(t, r.reports, unavailable)
}

func TestSealedAssistantSessionKillLeavesCandidateTerminalRunning(t *testing.T) {
	t.Parallel()
	r := newRig(t, environment.OwnerTiming{})
	r.running()
	machine := r.startAssistantProcesses(&ownerSessions{}, "assistant:1", "terminal:1", "assistant:2")
	logger, err := logging.New()
	require.NoError(t, err)
	node, err := environment.NewNode(environment.NodeDependencies{
		Cluster: &transportFake{}, Logger: logger, Environments: r.environments, Hosts: r.hosts,
	})
	require.NoError(t, err)
	require.NoError(t, node.KillSealedAssistantSessions(t.Context(), r.attemptID, []string{"assistant:1", "terminal:1", "assistant:2"}))
	waitAssistantProcessesKilled(t, machine, "assistant:1", "assistant:2")
	processes, err := machine.ListProcesses(t.Context())
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(processes, func(process environment.ProcessInfo) bool {
		return process.Tag == "terminal:1" && process.Running
	}))
}

func TestOwnerThawKillsAnExpiredSessionThatSurvivedFreeze(t *testing.T) {
	t.Parallel()
	sessions := &ownerSessions{}
	r := newPreparedRig(t, environment.OwnerTiming{}, func(r *rig) { r.sessions = sessions })
	r.running()
	machine := r.startAssistantProcesses(sessions, "assistant:1")
	sessions.mu.Lock()
	sessions.listErr = errors.New("unavailable at freeze")
	sessions.mu.Unlock()
	r.environments.desire(model.AttemptEnvironmentDesiredFrozen)
	r.poke()
	require.Eventually(t, func() bool { return r.environments.snapshot().State == model.AttemptEnvironmentFrozen }, eventually, tick)
	require.Empty(t, sessions.finished())

	sessions.mu.Lock()
	sessions.rows[0].StartedAt = time.Now().Add(-2 * time.Minute)
	sessions.listErr = nil
	sessions.mu.Unlock()
	r.environments.desire(model.AttemptEnvironmentDesiredRunning)
	r.poke()
	r.running()
	waitAssistantProcessesKilled(t, machine, "assistant:1")
	ends := sessions.finished()
	require.Len(t, ends, 1)
	require.Equal(t, "timed_out", ends[0].Outcome)
	require.Equal(t, "KILL", ends[0].Signal)
	require.Nil(t, ends[0].ExitCode)
}
