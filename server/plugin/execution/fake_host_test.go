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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

// fakeHost is the plugin.Host the unit tests activate the plugin on: an
// in-memory record store, leases, an Attempt Workspace with a journal, and
// an evidence log, with the core's fences but none of its persistence.
type fakeHost struct {
	settings json.RawMessage
	secrets  json.RawMessage
	records  *fakeRecords
	leases   *fakeLeases
	work     *fakeWorkspaces
	evidence *fakeEvidence
	sections map[string]json.RawMessage

	section  plugin.ExamSection
	handlers map[plugin.EventTopic]plugin.EventHandler
	workers  map[string]plugin.Worker
	routes   map[string]plugin.Route
}

func newFakeHost(t *testing.T, settings string) *fakeHost {
	t.Helper()
	return &fakeHost{
		settings: json.RawMessage(settings),
		secrets:  json.RawMessage(`{"hosts":[{"name":"alpha","address":"alpha.example.test:7443","server_name":"alpha.example.test","ca_pem":` + mustQuote(testCAPEM(t)) + `,"token":"alpha-token"}]}`),
		records:  &fakeRecords{docs: map[string]plugin.Record{}},
		leases:   &fakeLeases{held: map[string]int64{}},
		work:     newFakeWorkspaces(),
		evidence: &fakeEvidence{},
		sections: map[string]json.RawMessage{},
		handlers: map[plugin.EventTopic]plugin.EventHandler{},
		workers:  map[string]plugin.Worker{},
		routes:   map[string]plugin.Route{},
	}
}

func mustQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func (h *fakeHost) Logger() *slog.Logger                                      { return testLogger() }
func (h *fakeHost) Settings() json.RawMessage                                 { return h.settings }
func (h *fakeHost) Secrets() json.RawMessage                                  { return h.secrets }
func (h *fakeHost) AddIdentityProviderType(plugin.IdentityProviderType) error { return nil }
func (h *fakeHost) AddExportFormat(plugin.ExportFormat) error                 { return nil }
func (h *fakeHost) Records() plugin.Records                                   { return h.records }
func (h *fakeHost) ExamSections() plugin.ExamSections                         { return fakeSections(h.sections) }
func (h *fakeHost) AttemptWorkspaces() plugin.AttemptWorkspaces               { return h.work }
func (h *fakeHost) Evidence() plugin.Evidence                                 { return h.evidence }
func (h *fakeHost) AddEventHandler(topic plugin.EventTopic, handler plugin.EventHandler) error {
	h.handlers[topic] = handler
	return nil
}
func (h *fakeHost) AddWorker(name string, run plugin.Worker) error { h.workers[name] = run; return nil }
func (h *fakeHost) AddExamSection(section plugin.ExamSection) error {
	h.section = section
	return nil
}
func (h *fakeHost) AddRoute(route plugin.Route) error {
	if err := route.Validate(); err != nil {
		return err
	}
	h.routes[route.Method+" "+route.Path] = route
	return nil
}

type fakeSections map[string]json.RawMessage

func (s fakeSections) Get(_ context.Context, revisionID string) (json.RawMessage, error) {
	if section, ok := s[revisionID]; ok {
		return section, nil
	}
	return nil, plugin.ErrExamSectionNotFound
}

// fakeRecords is a record store with the core's revision rules.
type fakeRecords struct {
	mu   sync.Mutex
	docs map[string]plugin.Record
}

func (r *fakeRecords) Get(_ context.Context, kind, key string) (plugin.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.docs[kind+"\x00"+key]
	if !ok {
		return plugin.Record{}, plugin.ErrRecordNotFound
	}
	return record, nil
}

func (r *fakeRecords) Put(_ context.Context, kind, key string, document json.RawMessage, expected int64) (int64, error) {
	if len(document) > plugin.MaxRecordDocumentBytes {
		return 0, plugin.ErrRecordTooLarge
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.docs[kind+"\x00"+key]
	if (!ok && expected != 0) || (ok && record.Revision != expected) {
		return 0, plugin.ErrRevisionConflict
	}
	next := plugin.Record{Key: key, Document: append(json.RawMessage(nil), document...), Revision: expected + 1}
	r.docs[kind+"\x00"+key] = next
	return next.Revision, nil
}

func (r *fakeRecords) Delete(_ context.Context, kind, key string, expected int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.docs[kind+"\x00"+key]
	if !ok || record.Revision != expected {
		return plugin.ErrRevisionConflict
	}
	delete(r.docs, kind+"\x00"+key)
	return nil
}

func (r *fakeRecords) List(_ context.Context, kind, prefix, after string, limit int) ([]plugin.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []plugin.Record
	for name, record := range r.docs {
		if strings.HasPrefix(name, kind+"\x00") && strings.HasPrefix(record.Key, prefix) && record.Key > after {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// fakeLeases holds leases for the one node of the test.
type fakeLeases struct {
	mu    sync.Mutex
	held  map[string]int64
	fence int64
}

func (l *fakeLeases) Claim(context.Context, string) (plugin.Lease, error) { panic("unused") }
func (l *fakeLeases) ClaimAvailable(context.Context, []string, int) ([]plugin.Lease, error) {
	panic("unused")
}
func (l *fakeLeases) Renew(_ context.Context, lease plugin.Lease) (plugin.Lease, error) {
	return lease, nil
}
func (l *fakeLeases) Release(_ context.Context, lease plugin.Lease) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[lease.Key] == lease.Fence {
		delete(l.held, lease.Key)
	}
	return nil
}

func (l *fakeLeases) Hold(ctx context.Context, key string) (context.Context, plugin.Lease, error) {
	l.mu.Lock()
	if _, ok := l.held[key]; ok {
		l.mu.Unlock()
		return nil, plugin.Lease{}, plugin.ErrLeaseHeld
	}
	l.fence++
	lease := plugin.Lease{Key: key, Fence: l.fence, ExpiresAt: time.Now().Add(time.Minute)}
	l.held[key] = lease.Fence
	l.mu.Unlock()
	held, cancel := context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		cancel()
		_ = l.Release(context.Background(), lease)
	}()
	return held, lease, nil
}

// fakeEvidence keeps appended records, numbered per binding and kind.
type fakeEvidence struct {
	mu      sync.Mutex
	stored  []plugin.EvidenceRecord
	seen    map[string]bool
	current func(plugin.BoundEnvironment) bool
}

func (e *fakeEvidence) Append(_ context.Context, environment plugin.BoundEnvironment, record plugin.EvidenceRecord) (plugin.EvidenceReceipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil && !e.current(environment) {
		return plugin.EvidenceReceipt{}, plugin.ErrBindingNotCurrent
	}
	if record.Sequence < 1 || len(record.Detail) > plugin.MaxEvidenceDetailBytes || !json.Valid(record.Detail) {
		return plugin.EvidenceReceipt{}, plugin.ErrEvidenceInvalid
	}
	if e.seen == nil {
		e.seen = map[string]bool{}
	}
	key := fmt.Sprintf("%s/%s/%d", environment.BindingID, record.Kind, record.Sequence)
	if e.seen[key] {
		return plugin.EvidenceReceipt{Repeated: true}, nil
	}
	e.seen[key] = true
	e.stored = append(e.stored, record)
	return plugin.EvidenceReceipt{ID: key, RecordedAt: time.Now()}, nil
}

func (e *fakeEvidence) records(kind string) []plugin.EvidenceRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	var result []plugin.EvidenceRecord
	for _, record := range e.stored {
		if record.Kind == kind {
			result = append(result, record)
		}
	}
	return result
}

// fakeWorkspaces is one Attempt Workspace per Attempt, with the batch
// fences and journal of the core's use case.
type fakeWorkspaces struct {
	mu         sync.Mutex
	attempts   map[string]*fakeWorkspace
	bindings   map[string]plugin.EnvironmentBinding
	live       map[string]string // attempt -> binding
	generation map[string]int64
	ids        int
}

type fakeWorkspace struct {
	cursor  int64
	entries map[string]*fakeEntry
	journal []plugin.WorkspaceChange
	keys    map[string]plugin.WorkspaceBatchResult
}

type fakeEntry struct {
	id, kind, path, version string
	body                    []byte
}

func newFakeWorkspaces() *fakeWorkspaces {
	return &fakeWorkspaces{attempts: map[string]*fakeWorkspace{}, bindings: map[string]plugin.EnvironmentBinding{},
		live: map[string]string{}, generation: map[string]int64{}}
}

func (w *fakeWorkspaces) newID(prefix string) string {
	w.ids++
	return fmt.Sprintf("%s%024d", prefix, w.ids)
}

// seed creates the Attempt's Workspace with the given files (directories
// end in a slash) as the starter, outside the journal.
func (w *fakeWorkspaces) seed(attemptID string, generation int64, files map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	workspace := &fakeWorkspace{entries: map[string]*fakeEntry{}, keys: map[string]plugin.WorkspaceBatchResult{}}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasSuffix(name, "/") {
			id := w.newID("d")
			workspace.entries[id] = &fakeEntry{id: id, kind: kindDirectory, path: strings.TrimSuffix(name, "/")}
			continue
		}
		id := w.newID("f")
		workspace.entries[id] = &fakeEntry{id: id, kind: kindFile, path: name, version: w.newID("v"), body: []byte(files[name])}
	}
	w.attempts[attemptID] = workspace
	w.generation[attemptID] = generation
}

// endGeneration ends the binding with the generation, as the core's
// trigger does.
func (w *fakeWorkspaces) endGeneration(attemptID string, next int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.generation[attemptID] = next
	delete(w.live, attemptID)
}

func (w *fakeWorkspaces) refused(code string) error { return &plugin.CapabilityError{Code: code} }

func (w *fakeWorkspaces) current(environment plugin.BoundEnvironment) bool {
	binding, ok := w.bindings[environment.BindingID]
	return ok && w.live[environment.AttemptID] == binding.ID && binding.Generation == environment.Generation &&
		binding.Identity == environment.Identity && w.generation[environment.AttemptID] == binding.Generation
}

func (w *fakeWorkspaces) BindEnvironment(_ context.Context, request plugin.BindEnvironmentRequest) (plugin.EnvironmentBindResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.generation[request.AttemptID] != request.Generation {
		return plugin.EnvironmentBindResult{}, w.refused("exam.attempt.unavailable")
	}
	if id, ok := w.live[request.AttemptID]; ok {
		binding := w.bindings[id]
		if binding.Identity == request.Identity && binding.Generation == request.Generation {
			return plugin.EnvironmentBindResult{Binding: binding}, nil
		}
	}
	// As the core: a grant belongs to one Attempt, a grant and epoch keep
	// their first incarnation, and they are bound at most once per
	// generation.
	for _, binding := range w.bindings {
		if binding.Identity.GrantID != request.Identity.GrantID {
			continue
		}
		sameEpoch := binding.Identity.Epoch == request.Identity.Epoch
		if binding.AttemptID != request.AttemptID ||
			(sameEpoch && binding.Identity.Incarnation != request.Identity.Incarnation) ||
			(sameEpoch && binding.Generation == request.Generation) {
			return plugin.EnvironmentBindResult{}, w.refused("exam.attempt.environment.identity_conflict")
		}
	}
	binding := plugin.EnvironmentBinding{ID: w.newID("b"), AttemptID: request.AttemptID, Generation: request.Generation,
		Identity: request.Identity, CreatedAt: time.Now()}
	w.bindings[binding.ID] = binding
	w.live[request.AttemptID] = binding.ID
	return plugin.EnvironmentBindResult{Binding: binding}, nil
}

func (w *fakeWorkspaces) EndEnvironmentBinding(_ context.Context, attemptID, bindingID string) (plugin.EnvironmentBinding, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live[attemptID] == bindingID {
		delete(w.live, attemptID)
	}
	return w.bindings[bindingID], nil
}

func (w *fakeWorkspaces) List(_ context.Context, request plugin.WorkspaceListRequest) (plugin.WorkspacePage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.current(request.Environment) {
		return plugin.WorkspacePage{}, w.refused("exam.attempt.unavailable")
	}
	workspace := w.attempts[request.Environment.AttemptID]
	page := plugin.WorkspacePage{WorkspaceID: "workspace", Cursor: workspace.cursor}
	for _, item := range workspace.entries {
		listed := plugin.WorkspaceItem{EntryID: item.id, Kind: item.kind, Path: item.path, ContentVersion: item.version}
		if item.kind == kindFile {
			sum := sha256.Sum256(item.body)
			listed.SHA256, listed.SizeBytes, listed.MediaType = hex.EncodeToString(sum[:]), int64(len(item.body)), "text/plain"
		}
		page.Items = append(page.Items, listed)
	}
	return page, nil
}

func (w *fakeWorkspaces) ListJournal(_ context.Context, request plugin.WorkspaceJournalRequest) (plugin.WorkspaceJournalPage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.current(request.Environment) {
		return plugin.WorkspaceJournalPage{}, w.refused("exam.attempt.unavailable")
	}
	workspace := w.attempts[request.Environment.AttemptID]
	page := plugin.WorkspaceJournalPage{WorkspaceID: "workspace", CurrentCursor: workspace.cursor}
	for _, change := range workspace.journal {
		if change.Cursor > request.AfterCursor && len(page.Entries) < request.Limit {
			page.Entries = append(page.Entries, change)
		}
	}
	page.HasMore = len(page.Entries) == request.Limit && page.Entries[len(page.Entries)-1].Cursor < workspace.cursor
	return page, nil
}

func (w *fakeWorkspaces) OpenFile(_ context.Context, environment plugin.BoundEnvironment, entryID string) (plugin.WorkspaceFile, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.current(environment) {
		return plugin.WorkspaceFile{}, w.refused("exam.attempt.unavailable")
	}
	item := w.attempts[environment.AttemptID].entries[entryID]
	if item == nil || item.kind != kindFile {
		return plugin.WorkspaceFile{}, w.refused("resource.not_found")
	}
	sum := sha256.Sum256(item.body)
	return plugin.WorkspaceFile{Body: io.NopCloser(bytes.NewReader(item.body)), SizeBytes: int64(len(item.body)),
		SHA256: hex.EncodeToString(sum[:]), ContentVersion: item.version, MediaType: "text/plain"}, nil
}

func (w *fakeWorkspaces) ApplyBatch(ctx context.Context, batch plugin.WorkspaceBatch) (plugin.WorkspaceBatchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.current(batch.Environment) {
		return plugin.WorkspaceBatchResult{}, w.refused("exam.attempt.unavailable")
	}
	return w.apply(ctx, batch.Environment.AttemptID, batch.Mutations, batch.Content, batch.IdempotencyKey, "environment")
}

// edit applies a batch as the candidate's editor.
func (w *fakeWorkspaces) edit(t *testing.T, attemptID string, mutations []plugin.WorkspaceMutation, bodies ...string) []plugin.WorkspaceChange {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	contents := make([][]byte, len(bodies))
	for index, body := range bodies {
		contents[index] = []byte(body)
	}
	next := 0
	for index := range mutations {
		if mutations[index].Operation == plugin.WorkspaceCreateFile || mutations[index].Operation == plugin.WorkspaceReplaceFile {
			body := contents[next]
			next++
			sum := sha256.Sum256(body)
			mutations[index].SHA256, mutations[index].Size, mutations[index].MediaType = hex.EncodeToString(sum[:]), int64(len(body)), "text/plain"
		}
	}
	result, err := w.apply(context.Background(), attemptID, mutations, &byteContents{bodies: contents}, w.newID("k"), "candidate")
	require.NoError(t, err)
	return result.Changes
}

func (w *fakeWorkspaces) apply(ctx context.Context, attemptID string, mutations []plugin.WorkspaceMutation, content plugin.WorkspaceContent, key, authorKind string) (plugin.WorkspaceBatchResult, error) {
	workspace := w.attempts[attemptID]
	if stored, ok := workspace.keys[key]; ok {
		stored.Replayed = true
		return stored, nil
	}
	entries := maps.Clone(workspace.entries)
	for id, item := range entries {
		duplicate := *item
		entries[id] = &duplicate
	}
	byPath := func(name string) *fakeEntry {
		for _, item := range entries {
			if item.path == name {
				return item
			}
		}
		return nil
	}
	cursor := workspace.cursor
	var changes []plugin.WorkspaceChange
	for index, mutation := range mutations {
		refuse := func(code string) (plugin.WorkspaceBatchResult, error) {
			return plugin.WorkspaceBatchResult{}, &plugin.CapabilityError{Code: code, Fields: map[string]string{"item": fmt.Sprint(index)}}
		}
		cursor++
		change := plugin.WorkspaceChange{Cursor: cursor, Operation: mutation.Operation, ChangedAt: time.Now(), AuthorKind: authorKind}
		switch mutation.Operation {
		case plugin.WorkspaceCreateFile, plugin.WorkspaceReplaceFile:
			reader, err := content.Next(ctx)
			if err != nil {
				return refuse("exam.attempt.invalid")
			}
			body, _ := io.ReadAll(reader)
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != mutation.SHA256 {
				return refuse("exam.attempt.invalid")
			}
			if mutation.Operation == plugin.WorkspaceCreateFile {
				if byPath(mutation.DestinationPath) != nil {
					return refuse("exam.attempt.workspace.path_conflict")
				}
				item := &fakeEntry{id: w.newID("f"), kind: kindFile, path: mutation.DestinationPath, version: w.newID("v"), body: body}
				entries[item.id] = item
				change.EntryID, change.EntryKind, change.NewPath, change.ContentVersion = item.id, kindFile, item.path, item.version
			} else {
				item := entries[mutation.EntryID]
				if item == nil || item.path != mutation.ExpectedPath || item.version != mutation.ExpectedContentVersion {
					return refuse("exam.attempt.workspace.content_conflict")
				}
				item.body, item.version = body, w.newID("v")
				change.EntryID, change.EntryKind, change.OldPath, change.NewPath, change.ContentVersion = item.id, kindFile, item.path, item.path, item.version
			}
		case plugin.WorkspaceCreateDirectory:
			if byPath(mutation.DestinationPath) != nil {
				return refuse("exam.attempt.workspace.path_conflict")
			}
			item := &fakeEntry{id: w.newID("d"), kind: kindDirectory, path: mutation.DestinationPath}
			entries[item.id] = item
			change.EntryID, change.EntryKind, change.NewPath = item.id, kindDirectory, item.path
		case plugin.WorkspaceMoveEntry:
			item := entries[mutation.EntryID]
			if item == nil || item.path != mutation.ExpectedPath || byPath(mutation.DestinationPath) != nil {
				return refuse("exam.attempt.workspace.entry_conflict")
			}
			from := item.path
			for _, other := range entries {
				if other.path == from || strings.HasPrefix(other.path, from+"/") {
					other.path = mutation.DestinationPath + strings.TrimPrefix(other.path, from)
				}
			}
			change.EntryID, change.EntryKind, change.OldPath, change.NewPath, change.ContentVersion = item.id, item.kind, from, item.path, item.version
		case plugin.WorkspaceDeleteEntry:
			item := entries[mutation.EntryID]
			if item == nil || item.path != mutation.ExpectedPath || (item.kind == kindFile && item.version != mutation.ExpectedContentVersion) {
				return refuse("exam.attempt.workspace.entry_conflict")
			}
			for id, other := range entries {
				if other.path == item.path || strings.HasPrefix(other.path, item.path+"/") {
					delete(entries, id)
				}
			}
			change.EntryID, change.EntryKind, change.OldPath, change.Recursive = item.id, item.kind, item.path, mutation.Recursive
		}
		changes = append(changes, change)
	}
	workspace.entries, workspace.cursor = entries, cursor
	workspace.journal = append(workspace.journal, changes...)
	result := plugin.WorkspaceBatchResult{WorkspaceID: "workspace", Changes: changes}
	workspace.keys[key] = result
	return result, nil
}

// file returns the Workspace's file at name, and whether it exists.
func (w *fakeWorkspaces) file(attemptID, name string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, item := range w.attempts[attemptID].entries {
		if item.path == name && item.kind == kindFile {
			return string(item.body), true
		}
	}
	return "", false
}

func (w *fakeWorkspaces) entryAt(attemptID, name string) fakeEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, item := range w.attempts[attemptID].entries {
		if item.path == name {
			return *item
		}
	}
	return fakeEntry{}
}

func (w *fakeWorkspaces) authorKind(attemptID, name string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	kind := ""
	for _, change := range w.attempts[attemptID].journal {
		if change.NewPath == name {
			kind = change.AuthorKind
		}
	}
	return kind
}

// testLogger discards unless EXECUTION_TEST_LOG is set.
func testLogger() *slog.Logger {
	if os.Getenv("EXECUTION_TEST_LOG") == "" {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
