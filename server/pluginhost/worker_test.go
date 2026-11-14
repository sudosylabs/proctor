// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// workerPlugin registers one worker named "loop", and keeps the Host it was
// activated with.
type workerPlugin struct {
	manifest plugin.Manifest
	log      *callLog
	run      plugin.Worker

	mu   sync.Mutex
	host plugin.Host
}

func (p *workerPlugin) Manifest() plugin.Manifest { return p.manifest }

func (p *workerPlugin) Activate(_ context.Context, host plugin.Host) error {
	p.log.record("activate:" + p.manifest.ID)
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
	return host.AddWorker("loop", p.run)
}

func (p *workerPlugin) Deactivate(context.Context) error {
	p.log.record("deactivate:" + p.manifest.ID)
	return nil
}

func (p *workerPlugin) activeHost() plugin.Host {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.host
}

func newWorkerPlugin(log *callLog, id string, run plugin.Worker) *workerPlugin {
	return &workerPlugin{manifest: plugin.Manifest{ID: id, Name: "Worker " + id, EnabledByDefault: true}, log: log, run: run}
}

func activateWorkers(t *testing.T, deps pluginhost.Dependencies, plugins ...plugin.Plugin) *pluginhost.Host {
	t.Helper()
	deps.Plugins = plugins
	deps.Logger = discardLogger()
	if deps.WorkerRestartDelay == 0 {
		deps.WorkerRestartDelay = time.Millisecond
	}
	host, err := pluginhost.Activate(context.Background(), deps)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	return host
}

// blockingWorker records each run, then waits for its context to end.
func blockingWorker(log *callLog, name string) plugin.Worker {
	return func(ctx context.Context, _ plugin.Leases) error {
		log.record("run:" + name)
		<-ctx.Done()
		log.record("stop:" + name)
		return nil
	}
}

func waitForCalls(t *testing.T, log *callLog, calls ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		seen := map[string]int{}
		for _, call := range log.all() {
			seen[call]++
		}
		want := map[string]int{}
		for _, call := range calls {
			want[call]++
		}
		for call, count := range want {
			if seen[call] < count {
				return false
			}
		}
		return true
	}, 5*time.Second, time.Millisecond, "calls %v, saw %v", calls, log.all())
}

func TestWorkersStartWithTheNodeAndStopBeforeDeactivate(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	host := activateWorkers(t, pluginhost.Dependencies{}, newWorkerPlugin(log, "alpha", blockingWorker(log, "alpha")))
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, []string{"activate:alpha"}, log.all(), "building the node starts no worker")

	require.NoError(t, host.StartWorkers(context.Background()))
	require.NoError(t, host.StartWorkers(context.Background()), "a second start does nothing")
	waitForCalls(t, log, "run:alpha")

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"alpha": {Enable: boolPointer(false)}})
	require.Equal(t, []string{"activate:alpha", "run:alpha", "stop:alpha", "deactivate:alpha"}, log.all(),
		"turning the plugin off ends the worker's context and waits for it before Deactivate")

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"alpha": {Enable: boolPointer(true)}})
	waitForCalls(t, log, "run:alpha", "run:alpha")

	require.NoError(t, host.Close(context.Background()))
	require.Equal(t, []string{
		"activate:alpha", "run:alpha", "stop:alpha", "deactivate:alpha",
		"activate:alpha", "run:alpha", "stop:alpha", "deactivate:alpha",
	}, log.all(), "a plugin turned on after start runs its worker at once, and Close stops it")
}

func TestStartingWorkersSkipsAPluginThatIsOff(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	host := activateWorkers(t, pluginhost.Dependencies{
		Settings: map[string]pluginhost.Settings{"alpha": {Enable: boolPointer(false)}},
	}, newWorkerPlugin(log, "alpha", blockingWorker(log, "alpha")))
	require.NoError(t, host.StartWorkers(context.Background()), "a plugin that is off has no workers to start")
	require.Empty(t, log.all())

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"alpha": {Enable: boolPointer(true)}})
	waitForCalls(t, log, "activate:alpha", "run:alpha")
}

func TestWorkersRestartWhenANewActivationReplacesTheOld(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	host := activateWorkers(t, pluginhost.Dependencies{}, newWorkerPlugin(log, "alpha", blockingWorker(log, "alpha")))
	require.NoError(t, host.StartWorkers(context.Background()))
	waitForCalls(t, log, "run:alpha")

	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"alpha": {Settings: json.RawMessage(`{"v":2}`)}})
	waitForCalls(t, log, "run:alpha", "run:alpha")
	require.Equal(t, []string{"activate:alpha", "run:alpha", "stop:alpha", "deactivate:alpha", "activate:alpha", "run:alpha"},
		log.all(), "new settings without Reconfigure stop the worker, reactivate, and start it again")
}

func TestAWorkerThatFailsIsStartedAgain(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var runs atomic.Int32
	worker := func(ctx context.Context, _ plugin.Leases) error {
		if runs.Add(1) <= 2 {
			return errors.New("lost the connection")
		}
		log.record("steady")
		<-ctx.Done()
		return nil
	}
	recorder := &recordingRecorder{}
	host := activateWorkers(t, pluginhost.Dependencies{Recorder: recorder}, newWorkerPlugin(log, "alpha", worker))
	require.NoError(t, host.StartWorkers(context.Background()))
	waitForCalls(t, log, "steady")
	require.Equal(t, int32(3), runs.Load(), "each error starts the worker again")
	require.Equal(t, pluginhost.StateActive, host.Statuses()[0].State, "a worker's error does not fail the plugin")
	require.Contains(t, recorder.calls(), "alpha worker error", "every failed run is counted as a worker call")
}

func TestAWorkerThatReturnsNilHasFinished(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var runs atomic.Int32
	host := activateWorkers(t, pluginhost.Dependencies{}, newWorkerPlugin(log, "alpha", func(context.Context, plugin.Leases) error {
		runs.Add(1)
		return nil
	}))
	require.NoError(t, host.StartWorkers(context.Background()))
	require.Eventually(t, func() bool { return runs.Load() == 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), runs.Load(), "a finished worker is not started again")
}

func TestRepeatedWorkerPanicsFailThePlugin(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var runs atomic.Int32
	records := newMemoryRecords()
	alpha := newWorkerPlugin(log, "alpha", func(context.Context, plugin.Leases) error {
		runs.Add(1)
		panic("worker exploded")
	})
	host := activateWorkers(t, pluginhost.Dependencies{Records: records}, alpha)
	require.NoError(t, host.StartWorkers(context.Background()))
	require.Eventually(t, func() bool { return host.Statuses()[0].State == pluginhost.StateFailed }, 5*time.Second, time.Millisecond,
		"the panics are contained and the limit fails the plugin")
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(pluginhost.PanicLimit), runs.Load(), "a failed plugin's worker is not started again")

	_, err := alpha.activeHost().Records().Get(context.Background(), "grant", "one")
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a failed plugin's records are unavailable")
	require.NoError(t, host.Close(context.Background()))
	require.Equal(t, []string{"activate:alpha", "deactivate:alpha"}, log.all(), "the failed plugin is still deactivated")
}

func TestAddWorkerChecksItsNameAndWhen(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var registered plugin.Host
	checker := &hookPlugin{fixturePlugin: newFixture(log, "alpha", true), activate: func(host plugin.Host) error {
		registered = host
		noop := func(context.Context, plugin.Leases) error { return nil }
		for _, name := range []string{"", "Loop", "1loop", "loop/1", string(make([]byte, 65))} {
			if err := host.AddWorker(name, noop); !errors.Is(err, plugin.ErrInvalidArgument) {
				return fmt.Errorf("name %q accepted: %v", name, err)
			}
		}
		if err := host.AddWorker("loop", nil); err == nil {
			return errors.New("nil worker accepted")
		}
		if err := host.AddWorker("loop", noop); err != nil {
			return err
		}
		if err := host.AddWorker("loop", noop); err == nil {
			return errors.New("repeated name accepted")
		}
		return nil
	}}
	host := activateWorkers(t, pluginhost.Dependencies{}, checker)
	require.Equal(t, pluginhost.StateActive, host.Statuses()[0].State)
	require.Error(t, registered.AddWorker("late", func(context.Context, plugin.Leases) error { return nil }),
		"a worker added after Activate is refused")
}

// hookPlugin runs activate during Activate.
type hookPlugin struct {
	*fixturePlugin
	activate func(plugin.Host) error
}

func (p *hookPlugin) Activate(ctx context.Context, host plugin.Host) error {
	if err := p.fixturePlugin.Activate(ctx, host); err != nil {
		return err
	}
	return p.activate(host)
}

func TestRecordsAreScopedToThePluginAndItsActivation(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	records := newMemoryRecords()
	alpha := newWorkerPlugin(log, "alpha", blockingWorker(log, "alpha"))
	beta := newWorkerPlugin(log, "beta", blockingWorker(log, "beta"))
	host := activateWorkers(t, pluginhost.Dependencies{Records: records}, alpha, beta)
	ctx := context.Background()

	revision, err := alpha.activeHost().Records().Put(ctx, "grant", "attempt/one", json.RawMessage(`{"owner":"alpha"}`), 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), revision)
	_, err = beta.activeHost().Records().Get(ctx, "grant", "attempt/one")
	require.ErrorIs(t, err, plugin.ErrRecordNotFound, "one plugin never sees another's record")
	require.Equal(t, []string{"alpha"}, records.pluginIDs(), "the host names the calling plugin on every write")

	stale := alpha.activeHost().Records()
	host.Reconcile(ctx, map[string]pluginhost.Settings{"alpha": {Enable: boolPointer(false)}})
	_, err = stale.Get(ctx, "grant", "attempt/one")
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a deactivated plugin's records are unavailable")

	host.Reconcile(ctx, map[string]pluginhost.Settings{"alpha": {Enable: boolPointer(true)}})
	record, err := alpha.activeHost().Records().Get(ctx, "grant", "attempt/one")
	require.NoError(t, err, "records are kept while the plugin is off")
	require.JSONEq(t, `{"owner":"alpha"}`, string(record.Document))

	unbacked := newWorkerPlugin(log, "gamma", blockingWorker(log, "gamma"))
	activateWorkers(t, pluginhost.Dependencies{}, unbacked)
	_, err = unbacked.activeHost().Records().Get(ctx, "grant", "one")
	require.ErrorIs(t, err, plugin.ErrUnavailable, "without a record store, records are unavailable")
}

func TestLeasesAreScopedToThePluginAndNode(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	leases := newMemoryLeases()
	claimed := make(chan plugin.Lease, 2)
	claimer := func(id string) plugin.Worker {
		return func(ctx context.Context, leases plugin.Leases) error {
			lease, err := leases.Claim(ctx, "grant/one")
			if err != nil {
				return err
			}
			claimed <- lease
			<-ctx.Done()
			return nil
		}
	}
	host := activateWorkers(t, pluginhost.Dependencies{Leases: leases, NodeID: "node-a"},
		newWorkerPlugin(log, "alpha", claimer("alpha")), newWorkerPlugin(log, "beta", claimer("beta")))
	require.NoError(t, host.StartWorkers(context.Background()))
	first, second := <-claimed, <-claimed
	require.NotEqual(t, first.Fence, second.Fence)
	require.ElementsMatch(t, []string{"alpha/node-a/grant/one", "beta/node-a/grant/one"}, leases.held(),
		"each plugin claims the same key in its own namespace, as this node")
}

func TestHoldRenewsReleasesAndReportsLoss(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	leases := newMemoryLeases()
	type held struct {
		ctx   context.Context
		lease plugin.Lease
	}
	holds := make(chan held, 1)
	release := make(chan struct{})
	worker := func(ctx context.Context, leases plugin.Leases) error {
		heldCtx, lease, err := leases.Hold(ctx, "grant/one")
		if err != nil {
			return err
		}
		holds <- held{heldCtx, lease}
		select {
		case <-release:
			return errors.New("give the lease back by returning")
		case <-ctx.Done():
			return nil
		}
	}
	host := activateWorkers(t, pluginhost.Dependencies{
		Leases: leases, NodeID: "node-a", LeaseLifetime: 30 * time.Millisecond, WorkerRestartDelay: time.Hour,
	}, newWorkerPlugin(log, "alpha", worker))
	require.NoError(t, host.StartWorkers(context.Background()))
	first := <-holds
	require.Eventually(t, func() bool { return leases.renewals() >= 3 }, 5*time.Second, time.Millisecond,
		"Hold keeps renewing the lease")
	require.NoError(t, first.ctx.Err(), "a renewed lease's context stays live")

	close(release)
	require.Eventually(t, func() bool { return len(leases.held()) == 0 }, 5*time.Second, time.Millisecond,
		"the lease is released when the worker's run returns")
	require.Error(t, first.ctx.Err())

	lossLeases := newMemoryLeases()
	lossHolds := make(chan held, 1)
	lossWorker := func(ctx context.Context, leases plugin.Leases) error {
		heldCtx, lease, err := leases.Hold(ctx, "grant/two")
		if err != nil {
			return err
		}
		lossHolds <- held{heldCtx, lease}
		<-ctx.Done()
		return nil
	}
	lossHost := activateWorkers(t, pluginhost.Dependencies{Leases: lossLeases, NodeID: "node-a", LeaseLifetime: 30 * time.Millisecond},
		newWorkerPlugin(log, "beta", lossWorker))
	require.NoError(t, lossHost.StartWorkers(context.Background()))
	lost := <-lossHolds
	lossLeases.steal("beta", "grant/two")
	select {
	case <-lost.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a lost lease's context did not end")
	}

	failing := newMemoryLeases()
	failingHolds := make(chan held, 1)
	failingWorker := func(ctx context.Context, leases plugin.Leases) error {
		heldCtx, lease, err := leases.Hold(ctx, "grant/three")
		if err != nil {
			return err
		}
		failingHolds <- held{heldCtx, lease}
		<-ctx.Done()
		return nil
	}
	failingHost := activateWorkers(t, pluginhost.Dependencies{Leases: failing, NodeID: "node-a", LeaseLifetime: 60 * time.Millisecond},
		newWorkerPlugin(log, "gamma", failingWorker))
	require.NoError(t, failingHost.StartWorkers(context.Background()))
	unproven := <-failingHolds
	failing.failRenewals()
	select {
	case <-unproven.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a lease that cannot be renewed in time kept its context")
	}
}

// memoryRecords is a RecordStore keyed by plugin, kind and key.
type memoryRecords struct {
	mu      sync.Mutex
	records map[[3]string]plugin.Record
	writers map[string]bool
}

func newMemoryRecords() *memoryRecords {
	return &memoryRecords{records: map[[3]string]plugin.Record{}, writers: map[string]bool{}}
}

func (m *memoryRecords) Get(_ context.Context, pluginID, kind, key string) (plugin.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[[3]string{pluginID, kind, key}]
	if !ok {
		return plugin.Record{}, plugin.ErrRecordNotFound
	}
	return record, nil
}

func (m *memoryRecords) Put(_ context.Context, pluginID, kind, key string, document json.RawMessage, expected int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := [3]string{pluginID, kind, key}
	if m.records[name].Revision != expected {
		return 0, plugin.ErrRevisionConflict
	}
	m.writers[pluginID] = true
	m.records[name] = plugin.Record{Key: key, Document: document, Revision: expected + 1}
	return expected + 1, nil
}

func (m *memoryRecords) Delete(_ context.Context, pluginID, kind, key string, expected int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := [3]string{pluginID, kind, key}
	if m.records[name].Revision != expected {
		return plugin.ErrRevisionConflict
	}
	delete(m.records, name)
	return nil
}

func (m *memoryRecords) List(context.Context, string, string, string, string, int) ([]plugin.Record, error) {
	return nil, nil
}

func (m *memoryRecords) pluginIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.writers {
		ids = append(ids, id)
	}
	return ids
}

// memoryLeases is a LeaseStore that never expires a lease on its own.
type memoryLeases struct {
	mu        sync.Mutex
	leases    map[[2]string]memoryLease
	fence     int64
	renewed   int
	renewFail bool
}

type memoryLease struct {
	nodeID string
	fence  int64
}

func newMemoryLeases() *memoryLeases { return &memoryLeases{leases: map[[2]string]memoryLease{}} }

func (m *memoryLeases) Claim(_ context.Context, pluginID, nodeID, key string, lifetime time.Duration) (plugin.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := [2]string{pluginID, key}
	if _, held := m.leases[name]; held {
		return plugin.Lease{}, plugin.ErrLeaseHeld
	}
	m.fence++
	m.leases[name] = memoryLease{nodeID: nodeID, fence: m.fence}
	return plugin.Lease{Key: key, Fence: m.fence, ExpiresAt: time.Now().Add(lifetime)}, nil
}

func (m *memoryLeases) ClaimAvailable(ctx context.Context, pluginID, nodeID string, keys []string, lifetime time.Duration, limit int) ([]plugin.Lease, error) {
	var taken []plugin.Lease
	for _, key := range keys {
		if len(taken) == limit {
			break
		}
		if lease, err := m.Claim(ctx, pluginID, nodeID, key, lifetime); err == nil {
			taken = append(taken, lease)
		}
	}
	return taken, nil
}

func (m *memoryLeases) Renew(_ context.Context, pluginID, nodeID string, lease plugin.Lease, lifetime time.Duration) (plugin.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.renewFail {
		return plugin.Lease{}, errors.New("database unreachable")
	}
	current, ok := m.leases[[2]string{pluginID, lease.Key}]
	if !ok || current.nodeID != nodeID || current.fence != lease.Fence {
		return plugin.Lease{}, plugin.ErrLeaseLost
	}
	m.renewed++
	lease.ExpiresAt = time.Now().Add(lifetime)
	return lease, nil
}

func (m *memoryLeases) Release(_ context.Context, pluginID, nodeID string, lease plugin.Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := [2]string{pluginID, lease.Key}
	if current, ok := m.leases[name]; ok && current.nodeID == nodeID && current.fence == lease.Fence {
		delete(m.leases, name)
	}
	return nil
}

func (m *memoryLeases) held() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var held []string
	for name, lease := range m.leases {
		held = append(held, name[0]+"/"+lease.nodeID+"/"+name[1])
	}
	return held
}

func (m *memoryLeases) renewals() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.renewed
}

// steal gives the key to another node, as a claim after expiry would.
func (m *memoryLeases) steal(pluginID, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fence++
	m.leases[[2]string{pluginID, key}] = memoryLease{nodeID: "node-b", fence: m.fence}
}

func (m *memoryLeases) failRenewals() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renewFail = true
}
