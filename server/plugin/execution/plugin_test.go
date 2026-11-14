// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/memory"

	"github.com/sudosylabs/proctor/server/plugin"
)

const (
	testAttempt  = "attempt0000000000000000001"
	testSitting  = "sitting0000000000000000001"
	testRevision = "revision000000000000000001"
)

// harness is the plugin activated on a fake host with one memory host.
type harness struct {
	t      *testing.T
	plugin *Plugin
	host   *fakeHost
	guest  *memory.Host
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T, section string) *harness {
	t.Helper()
	guest, err := memory.New(memory.Config{Images: []execenv.Image{"python-3.12"}, Slots: 4})
	require.NoError(t, err)
	host := newFakeHost(t, `{"harvest_quiet_ms":250,"harvest_max_ms":1000}`)
	host.sections[testRevision] = json.RawMessage(section)
	host.evidence.current = func(environment plugin.BoundEnvironment) bool {
		host.work.mu.Lock()
		defer host.work.mu.Unlock()
		return host.work.current(environment)
	}
	p := NewPlugin(Options{Dial: func(context.Context, HostConfig) (execenv.Host, error) { return guest, nil }})
	require.NoError(t, p.Activate(context.Background(), host))
	return &harness{t: t, plugin: p, host: host, guest: guest}
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan struct{})
	go func() {
		defer close(h.done)
		_ = h.host.workers["grants"](ctx, h.host.leases)
	}()
	h.t.Cleanup(h.stop)
}

func (h *harness) stop() {
	if h.cancel != nil {
		h.cancel()
		<-h.done
		h.cancel = nil
	}
}

func (h *harness) event(topic plugin.EventTopic, data any) {
	h.t.Helper()
	document, err := json.Marshal(data)
	require.NoError(h.t, err)
	require.NoError(h.t, h.host.handlers[topic].HandleEvent(context.Background(),
		plugin.Event{ID: "event", Topic: topic, OccurredAt: time.Now(), Data: document}))
}

func (h *harness) started(generation int64) {
	h.event(plugin.TopicExamAttemptParticipationStarted, plugin.ExamAttemptParticipationStarted{
		AttemptID: testAttempt, ParticipationID: "participation", Generation: generation,
		SittingID: testSitting, ExamID: "exam", ExamRevisionID: testRevision,
	})
}

func (h *harness) grant() grantRecord {
	h.t.Helper()
	grant, _, err := h.plugin.store().getGrant(context.Background(), testAttempt)
	require.NoError(h.t, err)
	return grant
}

func (h *harness) eventually(condition func() bool, message string) {
	h.t.Helper()
	require.Eventually(h.t, condition, 15*time.Second, 20*time.Millisecond, message)
}

func (h *harness) guestFile(name string) (string, bool) {
	grant := h.grant()
	if grant.GrantID == "" {
		return "", false
	}
	body, err := h.guest.ReadGuest(context.Background(), execenv.ID(grant.GrantID), name)
	return string(body), err == nil
}

func TestPluginRegistersItsSeams(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	require.Contains(t, h.host.routes, "GET terminal")
	require.Contains(t, h.host.routes, "POST flush")
	require.Contains(t, h.host.workers, "grants")
	require.Len(t, h.host.handlers, len(topics))
	require.False(t, h.plugin.Manifest().EnabledByDefault, "the plugin is off until an administrator turns it on")
	require.NoError(t, h.plugin.Manifest().Validate())
}

func TestSectionValidation(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	ctx := context.Background()
	require.NoError(t, h.host.section.ValidateSection(ctx, json.RawMessage(`{"image":"python-3.12","record_input":true}`)))
	require.Error(t, h.host.section.ValidateSection(ctx, json.RawMessage(`{"image":"rust-1"}`)), "no host offers the image")
	require.Error(t, h.host.section.ValidateSection(ctx, json.RawMessage(`{"image":"bad image"}`)))
	require.Error(t, h.host.section.ValidateSection(ctx, json.RawMessage(`{"image":"python-3.12","network":"open"}`)),
		"the network is not configurable")
	require.Error(t, h.host.section.ValidateSection(ctx, json.RawMessage(`{}`)))

	unusable, err := memory.New(memory.Config{Images: []execenv.Image{"python-3.12"}, Unusable: true})
	require.NoError(t, err)
	h.plugin.hosts.Store(newHostPool([]HostConfig{{Name: "alpha"}}, func(context.Context, HostConfig) (execenv.Host, error) {
		return unusable, nil
	}, h.host.Logger()))
	require.Error(t, h.host.section.ValidateSection(ctx, json.RawMessage(`{"image":"python-3.12"}`)),
		"a host that is not usable does not count")
}

func TestSettingsAndSecretsValidation(t *testing.T) {
	ctx := context.Background()
	p := NewPlugin(Options{})
	require.NoError(t, p.ValidateSettings(ctx, nil))
	require.NoError(t, p.ValidateSettings(ctx, json.RawMessage(`{"harvest_quiet_ms":500,"harvest_max_ms":5000}`)))
	require.Error(t, p.ValidateSettings(ctx, json.RawMessage(`{"harvest_quiet_ms":100}`)))
	require.Error(t, p.ValidateSettings(ctx, json.RawMessage(`{"harvest_quiet_ms":5000,"harvest_max_ms":2000}`)))
	require.Error(t, p.ValidateSettings(ctx, json.RawMessage(`{"harvest_quiet_ms":500,"other":1}`)))

	_, err := parseSecrets(json.RawMessage(`{"hosts":[{"name":"alpha","address":"a:1","server_name":"a","ca_pem":"nope","token":"secret-token"}]}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-token", "errors never carry a secret")
	_, err = parseSecrets(json.RawMessage(`{"hosts":[{"name":"Alpha"}]}`))
	require.Error(t, err)
}

func TestCoalesceKeepsTheLastWriteAndCancelsShortLivedFiles(t *testing.T) {
	content := &execenv.ContentReference{Size: 1, Digest: strings.Repeat("a", 64), TransferID: "t"}
	observations := []execenv.ExecutionObservation{
		{HostSequence: 1, Operation: execenv.ObservationCreateFile, NodeIdentity: "a", Kind: execenv.ProjectionFile, Path: "a", Content: content},
		{HostSequence: 2, Operation: execenv.ObservationWriteFile, NodeIdentity: "a", Kind: execenv.ProjectionFile, Path: "a", Content: content},
		{HostSequence: 3, Operation: execenv.ObservationWriteFile, NodeIdentity: "a", Kind: execenv.ProjectionFile, Path: "a", Content: content},
		{HostSequence: 4, Operation: execenv.ObservationCreateFile, NodeIdentity: "tmp", Kind: execenv.ProjectionFile, Path: "tmp", Content: content},
		{HostSequence: 5, Operation: execenv.ObservationWriteFile, NodeIdentity: "tmp", Kind: execenv.ProjectionFile, Path: "tmp", Content: content},
		{HostSequence: 6, Operation: execenv.ObservationDelete, NodeIdentity: "tmp", Kind: execenv.ProjectionFile, Path: "tmp"},
		{HostSequence: 7, Operation: execenv.ObservationWriteFile, NodeIdentity: "b", Kind: execenv.ProjectionFile, Path: "b", Content: content},
		{HostSequence: 8, Operation: execenv.ObservationMove, NodeIdentity: "b", Kind: execenv.ProjectionFile, Path: "b", DestinationPath: "c"},
		{HostSequence: 9, Operation: execenv.ObservationWriteFile, NodeIdentity: "b", Kind: execenv.ProjectionFile, Path: "c", Content: content},
	}
	var kept []uint64
	for _, observation := range coalesce(observations) {
		kept = append(kept, observation.HostSequence)
	}
	require.Equal(t, []uint64{1, 3, 7, 8, 9}, kept)
}

// TestGrantLifecycleOnAMemoryHost runs the owner end to end: the starter
// reaches the guest, a guest file is harvested with origin environment, an
// editor save reaches the guest, ignored paths stay on the grant, the
// process log becomes evidence, a flush answers with the cursor, a lost
// host gives a new incarnation and a gap, and a submission revokes the
// grant.
func TestGrantLifecycleOnAMemoryHost(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	h.host.work.seed(testAttempt, 1, map[string]string{"src/": "", "src/main.py": "print('start')\n"})
	h.started(1)
	h.start()
	ctx := context.Background()

	h.eventually(func() bool { body, ok := h.guestFile("src/main.py"); return ok && body == "print('start')\n" },
		"the starter file reaches the guest")
	grant := h.grant()
	require.Equal(t, stateLive, grant.State)
	require.Equal(t, int64(0), grant.Incarnation)
	id := execenv.ID(grant.GrantID)

	// The guest writes a file: it is harvested as the candidate's.
	require.NoError(t, h.guest.WriteGuest(ctx, id, "notes.txt", []byte("from the shell\n")))
	h.eventually(func() bool {
		body, ok := h.host.work.file(testAttempt, "notes.txt")
		return ok && body == "from the shell\n"
	},
		"the guest file is harvested")
	require.Equal(t, "environment", h.host.work.origin(testAttempt, "notes.txt"))

	// The guest rewrites a projected file.
	require.NoError(t, h.guest.WriteGuest(ctx, id, "src/main.py", []byte("print('guest')\n")))
	h.eventually(func() bool {
		body, _ := h.host.work.file(testAttempt, "src/main.py")
		return body == "print('guest')\n"
	},
		"the rewrite of a projected file is harvested")

	// The editor saves a file: it reaches the guest.
	main := h.host.work.entryAt(testAttempt, "src/main.py")
	h.eventually(func() bool { return h.grant().WorkspaceCursor == h.host.work.attempts[testAttempt].cursor },
		"the harvested changes are projected back")
	h.host.work.edit(t, testAttempt, []plugin.WorkspaceMutation{{
		Operation: plugin.WorkspaceReplaceFile, EntryID: main.id, ExpectedPath: main.path, ExpectedContentVersion: main.version,
	}}, "print('editor')\n")
	h.eventually(func() bool { body, _ := h.guestFile("src/main.py"); return body == "print('editor')\n" },
		"the editor's save reaches the guest")

	// Default ignore patterns keep dependency installs on the grant.
	require.NoError(t, h.guest.MkdirGuest(ctx, id, "node_modules"))
	require.NoError(t, h.guest.WriteGuest(ctx, id, "node_modules/left.js", []byte("x")))
	require.NoError(t, h.guest.WriteGuest(ctx, id, "kept.txt", []byte("kept")))
	h.eventually(func() bool { _, ok := h.host.work.file(testAttempt, "kept.txt"); return ok }, "a later file is harvested")
	_, ignored := h.host.work.file(testAttempt, "node_modules/left.js")
	require.False(t, ignored, "a default-ignored path stays on the grant")

	// A .proctorignore the candidate writes replaces the defaults, and the
	// git rule keeps a held path syncing.
	require.NoError(t, h.guest.WriteGuest(ctx, id, IgnoreFile, []byte("*.log\nnotes.txt\n")))
	h.eventually(func() bool { _, ok := h.host.work.file(testAttempt, IgnoreFile); return ok }, "the ignore file always syncs")
	require.NoError(t, h.guest.WriteGuest(ctx, id, "run.log", []byte("log")))
	require.NoError(t, h.guest.WriteGuest(ctx, id, "notes.txt", []byte("still synced\n")))
	h.eventually(func() bool { body, _ := h.host.work.file(testAttempt, "notes.txt"); return body == "still synced\n" },
		"an ignored pattern does not stop a held path")
	_, logged := h.host.work.file(testAttempt, "run.log")
	require.False(t, logged, "a new path the ignore file names is not harvested")

	// A command in the terminal reaches the process log.
	env, err := h.guest.Ensure(ctx, execenv.Spec{ID: id, Image: "python-3.12", Network: execenv.NetworkNone})
	require.NoError(t, err)
	terminal, err := env.Attach(ctx, execenv.AttachRequest{})
	require.NoError(t, err)
	_, err = terminal.Write([]byte("echo hello\n"))
	require.NoError(t, err)
	h.eventually(func() bool {
		for _, record := range h.host.evidence.records(evidenceProcess) {
			if bytes.Contains(record.Detail, []byte(`"echo"`)) {
				return true
			}
		}
		return false
	}, "the command is recorded as process evidence")
	require.NoError(t, terminal.Close())

	// A flush freezes the grant and answers with the current cursor.
	response, err := h.plugin.flushRoute(ctx, plugin.CandidateAttemptAccess{AttemptID: testAttempt, Generation: 1}, plugin.Request{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.Status)
	var flushed flushResponse
	require.NoError(t, json.Unmarshal(response.Body, &flushed))
	require.Equal(t, h.host.work.attempts[testAttempt].cursor, flushed.WorkspaceCursor)
	_, err = env.Attach(ctx, execenv.AttachRequest{Takeover: true})
	require.ErrorIs(t, err, execenv.ErrFrozen, "the flushed grant is frozen")

	// The host loses the grant: the owner starts a new incarnation and
	// records the restart once it is bound again.
	require.True(t, h.guest.Lose(id))
	h.eventually(func() bool { g := h.grant(); return g.Incarnation == 1 && g.State == stateLive }, "a new incarnation is live")
	h.eventually(func() bool { return len(h.host.evidence.records(gapRestarted)) == 1 }, "the restart is a gap")
	require.True(t, h.host.evidence.records(gapRestarted)[0].Gap)
	h.eventually(func() bool { body, _ := h.guestFile("notes.txt"); return body == "still synced\n" },
		"the new grant is projected from the Attempt Workspace")

	// Submission revokes the grant.
	h.host.work.endGeneration(testAttempt, 2)
	h.event(plugin.TopicExamAttemptParticipationEnded, plugin.ExamAttemptParticipationEnded{
		AttemptID: testAttempt, Generation: 1, SittingID: testSitting, Reason: plugin.ParticipationEndSubmitted,
	})
	h.eventually(func() bool { return h.grant().State == stateDone }, "the grant is done")
	report, err := h.guest.Ready(ctx)
	require.NoError(t, err)
	require.Zero(t, report.Grants, "the host holds no grant")
	_, live, err := h.plugin.store().get(ctx, kindLive, attemptKey(testAttempt), &struct{}{})
	require.NoError(t, err)
	require.False(t, live)
}

// TestInterruptedGenerationKeepsTheShell freezes the grant when the
// generation ends and thaws the same grant for the next one.
func TestInterruptedGenerationKeepsTheShell(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	h.host.work.seed(testAttempt, 1, map[string]string{"a.txt": "a"})
	h.started(1)
	h.start()
	h.eventually(func() bool { return h.grant().State == stateLive }, "the grant is live")
	first := h.grant()

	h.host.work.endGeneration(testAttempt, 2)
	h.event(plugin.TopicExamAttemptParticipationEnded, plugin.ExamAttemptParticipationEnded{
		AttemptID: testAttempt, Generation: 1, Reason: plugin.ParticipationEndInterrupted,
	})
	h.eventually(func() bool { return h.grant().Desired == desiredFrozen }, "the grant should freeze")
	h.started(2)
	h.eventually(func() bool { g := h.grant(); return g.BoundGeneration == 2 && g.Desired == desiredRunning }, "rebound")
	second := h.grant()
	require.Equal(t, first.GrantID, second.GrantID, "the same grant serves the next generation")
	require.Equal(t, first.Epoch, second.Epoch)
	require.NotEqual(t, first.BindingID, second.BindingID)

	require.NoError(t, h.guest.WriteGuest(context.Background(), execenv.ID(second.GrantID), "b.txt", []byte("b")))
	h.eventually(func() bool { _, ok := h.host.work.file(testAttempt, "b.txt"); return ok }, "harvest continues")
}

// TestOwnerTakeoverContinuesTheEpoch stops one owner and starts another,
// as when a node dies: the second continues the same grant.
func TestOwnerTakeoverContinuesTheEpoch(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	h.host.work.seed(testAttempt, 1, map[string]string{"a.txt": "a"})
	h.started(1)
	h.start()
	h.eventually(func() bool { return h.grant().State == stateLive }, "the grant is live")
	before := h.grant()
	h.stop()

	entry := h.host.work.entryAt(testAttempt, "a.txt")
	h.host.work.edit(t, testAttempt, []plugin.WorkspaceMutation{{
		Operation: plugin.WorkspaceReplaceFile, EntryID: entry.id, ExpectedPath: "a.txt", ExpectedContentVersion: entry.version,
	}}, "edited while nobody owned it")
	h.start()
	h.eventually(func() bool { body, _ := h.guestFile("a.txt"); return body == "edited while nobody owned it" },
		"the new owner continues the projection")
	after := h.grant()
	require.Equal(t, before.Epoch, after.Epoch, "the shell's grant survives the owner change")
	require.Greater(t, after.FenceRevision, before.FenceRevision, "the new owner fences out the old one")
	require.Empty(t, h.host.evidence.records(gapRestarted))
}

// TestCapacityWaitIsAGap waits for a slot and records the wait once the
// grant is bound.
func TestCapacityWaitIsAGap(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	full, err := memory.New(memory.Config{Images: []execenv.Image{"python-3.12"}, Slots: 1})
	require.NoError(t, err)
	_, err = full.Ensure(context.Background(), execenv.Spec{ID: "someone-else", Image: "python-3.12"})
	require.NoError(t, err)
	h.guest = full
	h.plugin.hosts.Store(newHostPool([]HostConfig{{Name: "alpha"}}, func(context.Context, HostConfig) (execenv.Host, error) {
		return full, nil
	}, h.host.Logger()))
	h.host.work.seed(testAttempt, 1, map[string]string{"a.txt": "a"})
	h.started(1)
	h.start()
	h.eventually(func() bool { return h.grant().State == stateWaitingCapacity }, "the grant waits for capacity")
	require.NoError(t, full.Revoke(context.Background(), "someone-else"))
	h.eventually(func() bool { return h.grant().State == stateLive }, "the grant is placed once a slot frees")
	h.eventually(func() bool { return len(h.host.evidence.records(gapWaitingCapacity)) == 1 }, "the wait is a gap")
}

func TestEventsAreIdempotentAndOrdered(t *testing.T) {
	h := newHarness(t, `{"image":"python-3.12"}`)
	h.started(2)
	h.started(1)
	require.Equal(t, int64(2), h.grant().Generation, "an older generation's start is ignored")
	h.event(plugin.TopicExamAttemptParticipationEnded, plugin.ExamAttemptParticipationEnded{
		AttemptID: testAttempt, Generation: 1, Reason: plugin.ParticipationEndInterrupted,
	})
	require.Equal(t, desiredRunning, h.grant().Desired, "an older generation's end is ignored")
	at := time.Now()
	pause := func(topic plugin.EventTopic, when time.Time) {
		document, _ := json.Marshal(plugin.ExamSittingChanged{SittingID: testSitting})
		require.NoError(t, h.host.handlers[topic].HandleEvent(context.Background(), plugin.Event{Topic: topic, OccurredAt: when, Data: document}))
	}
	pause(plugin.TopicExamSittingPaused, at)
	require.Equal(t, desiredFrozen, h.grant().Desired)
	pause(plugin.TopicExamSittingResumed, at.Add(-time.Second))
	require.Equal(t, desiredFrozen, h.grant().Desired, "a resume older than the pause is ignored")
	pause(plugin.TopicExamSittingResumed, at.Add(time.Second))
	require.Equal(t, desiredRunning, h.grant().Desired)
	h.event(plugin.TopicExamAttemptSubmitted, plugin.ExamAttemptSubmitted{AttemptID: testAttempt})
	require.Equal(t, desiredRevoked, h.grant().Desired)
	h.started(3)
	require.Equal(t, desiredRevoked, h.grant().Desired, "revocation is terminal")

	h.host.sections = map[string]json.RawMessage{}
	h.event(plugin.TopicExamAttemptParticipationStarted, plugin.ExamAttemptParticipationStarted{
		AttemptID: "attempt-without-terminal", Generation: 1, SittingID: testSitting, ExamRevisionID: "no-section",
	})
	_, found, err := h.plugin.store().getGrant(context.Background(), "attempt-without-terminal")
	require.NoError(t, err)
	require.False(t, found, "an Exam without the section gets no grant")
}
