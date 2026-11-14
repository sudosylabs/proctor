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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/plugin"
)

const (
	// ownerTick is how often the owner checks a flush request and syncs.
	ownerTick = 250 * time.Millisecond
	// grantReread is how often the owner rereads the grant record.
	grantReread = time.Second
	// flushThaw is how long a flushed grant stays frozen unless revoked.
	flushThaw = 30 * time.Second
	// divergedLimit is how many consecutive projection conflicts the owner
	// tolerates before it replaces the grant: execenv cannot bring a guest
	// path back in line with the Workspace in place.
	divergedLimit = 40
	// hostLossLimit is how long a host may stay unreachable before the
	// owner places the grant elsewhere.
	hostLossLimit = 30 * time.Second
	// manifestPage and journalPage bound one Workspace read.
	manifestPage = 200
	journalPage  = 200
	// echoBodyBudget bounds the harvested bytes the owner keeps to project
	// its own changes back; past it they are read from the Workspace.
	echoBodyBudget = 32 << 20
)

// The gap kinds the plugin records. They are evidence, never Flags.
const (
	gapRestarted       = "environment_restarted"
	gapWaitingCapacity = "environment_waiting_capacity"
	gapHarvestRefused  = "harvest_refused"
	evidenceProcess    = "process"
	evidenceInput      = "terminal_input"
)

// errRestart asks the owner to replace the grant with a new incarnation.
type errRestart struct{ reason string }

func (e errRestart) Error() string { return "execution grant must restart: " + e.reason }

// errWait means the owner cannot make progress now and retries later.
var errWait = errors.New("execution grant waits")

// echo is a harvested change the owner confirmed to execenv, kept until
// the journal reaches it: the exact mutation, and the bytes when kept.
type echo struct {
	mutation execenv.ProjectionMutation
	body     []byte
}

// owner runs one grant on the node that holds its lease.
type owner struct {
	plugin    *Plugin
	records   recordStore
	attemptID string
	logger    *slog.Logger

	grant            grantRecord
	readAt           time.Time
	host             execenv.Host
	env              execenv.Env
	control          execenv.ControlState
	fence            execenv.ProjectionFence
	ready            bool // the projection is initialized and the owner knows it
	unreachableSince time.Time

	projected  projected
	ahead      *tree
	current    int64 // the Workspace's cursor as last read
	hostCursor uint64
	echoes     map[int64]echo
	echoBytes  int
	conflicts  int
	applies    int

	pending      []execenv.ExecutionObservation
	observed     uint64
	firstPending time.Time
	lastObserved time.Time
	ignore       ignoreCache

	flushedNonce string
	thawAt       time.Time
}

func newOwner(p *Plugin, attemptID string) *owner {
	return &owner{
		plugin: p, records: p.store(), attemptID: attemptID,
		logger: p.logger.With(slog.String("attempt_id", attemptID)), echoes: map[int64]echo{},
	}
}

// run owns the grant until it is done or ctx, the lease's context, ends.
func (o *owner) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := o.step(ctx)
		var restart errRestart
		switch {
		case err == nil:
			backoff = time.Second
			sleep(ctx, ownerTick)
		case errors.Is(err, errDone):
			return
		case errors.As(err, &restart):
			if restartErr := o.restart(ctx, restart.reason); restartErr != nil && ctx.Err() == nil {
				o.logger.Warn("execution grant restart failed", slog.String("error", errorClass(restartErr)))
				sleep(ctx, backoff)
			}
		case errors.Is(err, errWait):
			sleep(ctx, backoff)
			backoff = min(backoff*2, 10*time.Second)
		default:
			if ctx.Err() == nil {
				o.logger.Warn("execution grant step failed", slog.String("error", errorClass(err)))
			}
			if errors.Is(err, execenv.ErrConnection) {
				o.dropHost()
			}
			sleep(ctx, backoff)
			backoff = min(backoff*2, 10*time.Second)
		}
	}
}

var errDone = errors.New("execution grant is done")

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// step does one round of work.
func (o *owner) step(ctx context.Context) error {
	if o.readAt.IsZero() || time.Since(o.readAt) >= grantReread {
		grant, found, err := o.records.getGrant(ctx, o.attemptID)
		if err != nil {
			return err
		}
		if !found || grant.State == stateDone {
			_ = o.records.remove(ctx, kindLive, attemptKey(o.attemptID))
			return errDone
		}
		o.grant, o.readAt = grant, time.Now()
	}
	if o.grant.Desired == desiredRevoked {
		return o.teardown(ctx)
	}
	if o.env == nil {
		if err := o.establish(ctx); err != nil {
			return err
		}
	}
	flush, flushRevision, err := o.flushRequest(ctx)
	if err != nil {
		return err
	}
	if o.grant.Desired == desiredFrozen {
		if flush != nil {
			o.answerFlush(ctx, flush, flushRevision, false)
		}
		if o.control != execenv.ControlFrozen {
			if _, current := o.currentBinding(); current && o.ready {
				_ = o.sync(ctx, true)
			}
			return o.setControl(ctx, execenv.ControlFrozen)
		}
		return o.alive(ctx)
	}

	// Running: bind the current generation, thaw, initialize, then sync.
	if _, current := o.currentBinding(); !current {
		if err := o.bind(ctx); err != nil {
			return err
		}
	}
	frozenForFlush := !o.thawAt.IsZero() && time.Now().Before(o.thawAt)
	if !frozenForFlush && o.control != execenv.ControlRunning {
		if err := o.setControl(ctx, execenv.ControlRunning); err != nil {
			return err
		}
		o.thawAt = time.Time{}
	}
	if !o.ready {
		if err := o.prepare(ctx); err != nil {
			return err
		}
	}
	if flush != nil {
		return o.flush(ctx, flush, flushRevision)
	}
	if frozenForFlush {
		return o.alive(ctx)
	}
	return o.sync(ctx, false)
}

// alive notices a grant lost while frozen, when no other call reaches it.
func (o *owner) alive(ctx context.Context) error {
	if _, err := o.env.ControlStatus(ctx); err != nil {
		return o.envError(err)
	}
	return nil
}

// currentBinding reports the bound environment and whether it binds the
// newest generation.
func (o *owner) currentBinding() (plugin.BoundEnvironment, bool) {
	environment, ok := o.grant.bound()
	return environment, ok && !o.grant.GenerationEnded && o.grant.BoundGeneration == o.grant.Generation &&
		o.grant.Epoch == o.envEpoch()
}

func (o *owner) envEpoch() string {
	if o.env == nil {
		return ""
	}
	return o.env.Epoch()
}

// establish reaches the grant the record names, or places a new one.
func (o *owner) establish(ctx context.Context) error {
	grant := o.grant
	if grant.GrantID != "" && grant.Host != "" {
		host, err := o.plugin.pool().get(ctx, grant.Host)
		if err != nil {
			return o.unreachable(err)
		}
		env, err := host.Ensure(ctx, execenv.Spec{ID: execenv.ID(grant.GrantID), Image: execenv.Image(grant.Image), Network: execenv.NetworkNone})
		switch {
		case errors.Is(err, execenv.ErrConnection):
			o.plugin.pool().broken(grant.Host, host)
			return o.unreachable(err)
		case errors.Is(err, execenv.ErrLost):
			_ = host.Revoke(ctx, execenv.ID(grant.GrantID))
			return errRestart{reason: "lost"}
		case err != nil:
			return errRestart{reason: errorClass(err)}
		}
		o.unreachableSince = time.Time{}
		if env.Epoch() != grant.Epoch {
			// The host holds another occupancy under the ID: the grant
			// this record names is gone.
			_ = env.Revoke(ctx)
			return errRestart{reason: "replaced"}
		}
		o.host, o.env = host, env
		o.control = ""
		// A new owner takes the next fence before acting, so a stale
		// owner's calls are refused from now on.
		if err := o.setControl(ctx, o.wantedControl()); err != nil {
			o.host, o.env = nil, nil
			return err
		}
		return o.resume(ctx)
	}
	return o.place(ctx)
}

func (o *owner) wantedControl() execenv.ControlState {
	if o.grant.Desired == desiredFrozen {
		return execenv.ControlFrozen
	}
	return execenv.ControlRunning
}

func (o *owner) unreachable(err error) error {
	if o.unreachableSince.IsZero() {
		o.unreachableSince = time.Now()
	}
	if time.Since(o.unreachableSince) > hostLossLimit {
		o.unreachableSince = time.Time{}
		return errRestart{reason: "host_unreachable"}
	}
	return fmt.Errorf("%w: %w", errWait, err)
}

// place occupies a new grant on the usable host with the most room.
func (o *owner) place(ctx context.Context) error {
	pool := o.plugin.pool()
	if pool == nil {
		return errWait
	}
	exclude := map[string]bool{}
	for range 4 {
		name := pool.place(ctx, execenv.Image(o.grant.Image), exclude)
		if name == "" {
			break
		}
		host, err := pool.get(ctx, name)
		if err != nil {
			exclude[name] = true
			continue
		}
		grantID := fmt.Sprintf("%s.%d", o.attemptID, o.grant.Incarnation)
		env, err := host.Ensure(ctx, execenv.Spec{ID: execenv.ID(grantID), Image: execenv.Image(o.grant.Image), Network: execenv.NetworkNone})
		if errors.Is(err, execenv.ErrLost) {
			_ = host.Revoke(ctx, execenv.ID(grantID))
			env, err = host.Ensure(ctx, execenv.Spec{ID: execenv.ID(grantID), Image: execenv.Image(o.grant.Image), Network: execenv.NetworkNone})
		}
		if err != nil {
			if errors.Is(err, execenv.ErrConnection) {
				pool.broken(name, host)
			}
			exclude[name] = true
			continue
		}
		grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
			if !grant.WaitingSince.IsZero() {
				waited := time.Since(grant.WaitingSince)
				grant.PendingGaps = append(grant.PendingGaps, gapNote{
					Kind: gapWaitingCapacity, ObservedAt: grant.WaitingSince,
					Detail: mustJSON(map[string]any{"waited_ms": waited.Milliseconds()}),
				})
				grant.WaitingSince = time.Time{}
			}
			grant.Host, grant.GrantID, grant.Epoch = name, grantID, env.Epoch()
			grant.FenceRevision, grant.BindingID, grant.BoundGeneration = 0, "", 0
			grant.WorkspaceCursor, grant.ObservationCursor, grant.ProcessCursor = 0, 0, 0
			grant.Sequences, grant.State = nil, stateStarting
			return true, nil
		})
		if err != nil {
			_ = env.Revoke(ctx)
			return err
		}
		o.grant, o.host, o.env, o.control, o.ready = grant, host, env, "", false
		_ = o.records.remove(ctx, kindProjection, attemptKey(o.attemptID))
		_ = o.records.remove(ctx, kindHarvest, attemptKey(o.attemptID))
		o.resetSync()
		// execenv requires a running intent before the first projection.
		return o.setControl(ctx, execenv.ControlRunning)
	}
	if o.grant.State != stateWaitingCapacity {
		grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
			if grant.WaitingSince.IsZero() {
				grant.WaitingSince = time.Now().UTC()
			}
			grant.State = stateWaitingCapacity
			return true, nil
		})
		if err != nil {
			return err
		}
		o.grant = grant
	}
	return errWait
}

// setControl takes the next fence revision and moves the grant to state.
func (o *owner) setControl(ctx context.Context, state execenv.ControlState) error {
	grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
		if grant.GrantID != o.grant.GrantID || grant.Epoch != o.grant.Epoch {
			return false, errRestart{reason: "record_moved"}
		}
		grant.FenceRevision++
		return true, nil
	})
	if err != nil {
		return err
	}
	o.grant = grant
	fence := execenv.ProjectionFence{
		ExecutionGrantID: execenv.ID(grant.GrantID), EnvironmentEpoch: grant.Epoch, FenceRevision: grant.FenceRevision,
	}
	receipt, err := o.env.Control(ctx, execenv.ControlRequest{Fence: fence, State: state})
	if err != nil {
		return o.envError(err)
	}
	if !receipt.Confirmed || receipt.Fence != fence {
		return fmt.Errorf("%w: control not confirmed", errWait)
	}
	o.fence, o.control = fence, state
	return nil
}

// envError turns an execenv failure into what the owner does next.
func (o *owner) envError(err error) error {
	switch {
	case errors.Is(err, execenv.ErrLost):
		return errRestart{reason: "lost"}
	case errors.Is(err, execenv.ErrRevoked):
		return errRestart{reason: "revoked"}
	case errors.Is(err, execenv.ErrLagged):
		return errRestart{reason: "lagged"}
	default:
		return err
	}
}

// bind binds the grant to the newest generation.
func (o *owner) bind(ctx context.Context) error {
	if o.grant.GenerationEnded || o.grant.Generation < 1 {
		return errWait
	}
	identity := plugin.EnvironmentIdentity{
		GrantID: o.grant.GrantID, Epoch: o.grant.Epoch, Incarnation: o.grant.Incarnation,
	}
	result, err := o.plugin.host.AttemptWorkspaces().BindEnvironment(ctx, plugin.BindEnvironmentRequest{
		AttemptID: o.attemptID, Generation: o.grant.Generation, Identity: identity,
	})
	var refusal *plugin.CapabilityError
	if errors.As(err, &refusal) {
		// The generation is no longer current; the next event says what
		// comes instead.
		o.readAt = time.Time{}
		return fmt.Errorf("%w: bind refused %s", errWait, refusal.Code)
	}
	if err != nil {
		return err
	}
	grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
		if grant.BindingID != result.Binding.ID {
			grant.Sequences = nil
		}
		grant.BindingID, grant.BoundGeneration = result.Binding.ID, result.Binding.Generation
		return true, nil
	})
	if err != nil {
		return err
	}
	o.grant = grant
	return o.writePendingGaps(ctx)
}

// writePendingGaps records the gaps that waited for a binding.
func (o *owner) writePendingGaps(ctx context.Context) error {
	for len(o.grant.PendingGaps) > 0 {
		note := o.grant.PendingGaps[0]
		if err := o.gap(ctx, note.Kind, note.ObservedAt, note.Detail); err != nil {
			return err
		}
		grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
			if len(grant.PendingGaps) == 0 {
				return false, nil
			}
			grant.PendingGaps = grant.PendingGaps[1:]
			return true, nil
		})
		if err != nil {
			return err
		}
		o.grant = grant
	}
	return nil
}

// gap appends one gap record through the current binding.
func (o *owner) gap(ctx context.Context, kind string, at time.Time, detail json.RawMessage) error {
	return o.evidence(ctx, kind, true, at, detail, 0)
}

// evidence appends one record; sequence 0 takes the kind's next number.
func (o *owner) evidence(ctx context.Context, kind string, gap bool, at time.Time, detail json.RawMessage, sequence int64) error {
	environment, ok := o.currentBinding()
	if !ok {
		return errWait
	}
	if sequence == 0 {
		grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
			if grant.Sequences == nil {
				grant.Sequences = map[string]int64{}
			}
			grant.Sequences[kind]++
			return true, nil
		})
		if err != nil {
			return err
		}
		o.grant, sequence = grant, grant.Sequences[kind]
	}
	_, err := o.plugin.host.Evidence().Append(ctx, environment, plugin.EvidenceRecord{
		Kind: kind, Sequence: sequence, Gap: gap, ObservedAt: at, Detail: detail,
	})
	switch {
	case err == nil, errors.Is(err, plugin.ErrEvidenceBudgetExhausted), errors.Is(err, plugin.ErrEvidenceSequenceConflict):
		return nil
	case errors.Is(err, plugin.ErrBindingNotCurrent):
		o.readAt = time.Time{}
		return errWait
	default:
		return err
	}
}

// resume continues an epoch another owner started.
func (o *owner) resume(ctx context.Context) error {
	o.resetSync()
	var record projectionRecord
	_, found, err := o.records.get(ctx, kindProjection, attemptKey(o.attemptID), &record)
	if err != nil {
		return err
	}
	if !found || record.GrantID != o.grant.GrantID || record.Epoch != o.grant.Epoch {
		// The projection was never recorded for this epoch, so whether it
		// was initialized is unknown; execenv initializes once per epoch.
		return errRestart{reason: "unrecorded_projection"}
	}
	o.projected = projected{Cursor: record.Cursor, Versions: record.Versions}
	if o.projected.Versions == nil {
		o.projected.Versions = map[string]string{}
	}
	o.observed = o.grant.ObservationCursor
	o.ready = false // the manifest is read once a binding allows it
	return nil
}

func (o *owner) resetSync() {
	o.projected = projected{Versions: map[string]string{}}
	o.ahead, o.current, o.hostCursor = nil, 0, 0
	o.echoes, o.echoBytes, o.conflicts = map[int64]echo{}, 0, 0
	o.pending, o.observed, o.firstPending, o.lastObserved = nil, 0, time.Time{}, time.Time{}
	o.ignore = ignoreCache{}
}

// prepare initializes the projection of a new epoch, or reads the manifest
// to continue one.
func (o *owner) prepare(ctx context.Context) error {
	environment, ok := o.currentBinding()
	if !ok {
		return errWait
	}
	manifest, err := o.listManifest(ctx, environment)
	if err != nil {
		return err
	}
	if o.grant.State == stateStarting {
		if err := o.initialize(ctx, environment, manifest); err != nil {
			return err
		}
	} else {
		o.ahead, o.current = manifest, manifest.Cursor
		if err := o.retryHarvest(ctx); err != nil {
			return err
		}
	}
	o.ready = true
	return nil
}

func (o *owner) listManifest(ctx context.Context, environment plugin.BoundEnvironment) (*tree, error) {
	workspaces := o.plugin.host.AttemptWorkspaces()
	for range 8 {
		result := newTree(0)
		request := plugin.WorkspaceListRequest{Environment: environment, ExpectedCursor: -1, Limit: manifestPage}
		restart := false
		for {
			page, err := workspaces.List(ctx, request)
			if err != nil {
				return nil, o.workspaceError(err)
			}
			if page.RefreshRequired {
				restart = true
				break
			}
			result.Cursor = page.Cursor
			for _, item := range page.Items {
				result.add(entry{ID: item.EntryID, Kind: item.Kind, Path: item.Path, Version: item.ContentVersion,
					SHA256: item.SHA256, Size: item.SizeBytes})
			}
			if !page.HasMore || len(page.Items) == 0 {
				break
			}
			request.ExpectedCursor, request.AfterEntryID = page.Cursor, page.Items[len(page.Items)-1].EntryID
		}
		if !restart {
			return result, nil
		}
	}
	return nil, fmt.Errorf("%w: the Workspace kept changing", errWait)
}

// workspaceError maps a refused Workspace call: a binding that is no
// longer current waits for the next generation.
func (o *owner) workspaceError(err error) error {
	var refusal *plugin.CapabilityError
	if errors.As(err, &refusal) {
		o.readAt = time.Time{}
		return fmt.Errorf("%w: workspace refused %s", errWait, refusal.Code)
	}
	return err
}

// initialize projects the Attempt Workspace into a fresh epoch.
func (o *owner) initialize(ctx context.Context, environment plugin.BoundEnvironment, manifest *tree) error {
	items := make([]*entry, 0, len(manifest.byID))
	for _, item := range manifest.byID {
		items = append(items, item)
	}
	// Parents precede descendants.
	sort.Slice(items, func(i, j int) bool {
		di, dj := strings.Count(items[i].Path, "/"), strings.Count(items[j].Path, "/")
		if di != dj {
			return di < dj
		}
		return items[i].Path < items[j].Path
	})
	entries := make([]execenv.ProjectionEntry, 0, len(items))
	for _, item := range items {
		projection := execenv.ProjectionEntry{EntryID: item.ID, Kind: execenv.ProjectionDirectory, Path: item.Path}
		if item.Kind == kindFile {
			reference, sha, size, err := o.uploadEntry(ctx, environment, item.ID)
			if err != nil {
				return err
			}
			item.SHA256, item.Size = sha, size
			projection.Kind, projection.ContentVersion, projection.Content = execenv.ProjectionFile, execenv.Version(item.Version), reference
		}
		entries = append(entries, projection)
	}
	receipt, err := o.env.InitializeProjection(ctx, execenv.ProjectionInitialization{
		Fence: o.fence, MutationID: "init", WorkspaceCursor: uint64(manifest.Cursor), Entries: entries, Ignore: ignoreRules(),
	})
	if err != nil {
		if errors.Is(err, execenv.ErrTooLarge) {
			o.logger.Warn("the Attempt Workspace is too large to project")
			return fmt.Errorf("%w: workspace too large", errWait)
		}
		return o.envError(err)
	}
	o.projected = projected{Cursor: manifest.Cursor, Versions: map[string]string{}}
	for _, item := range items {
		if item.Kind == kindFile {
			o.projected.Versions[item.ID] = item.Version
		}
	}
	o.ahead, o.current, o.hostCursor = manifest, manifest.Cursor, receipt.HostCursor
	if err := o.saveProjection(ctx); err != nil {
		return err
	}
	grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
		grant.WorkspaceCursor, grant.State = manifest.Cursor, stateLive
		return true, nil
	})
	if err != nil {
		return err
	}
	o.grant = grant
	return nil
}

// uploadEntry uploads a Workspace file's current content into the grant.
// A file that no longer exists uploads as empty: a later journal entry
// removes it.
func (o *owner) uploadEntry(ctx context.Context, environment plugin.BoundEnvironment, entryID string) (*execenv.ContentReference, string, int64, error) {
	file, err := o.plugin.host.AttemptWorkspaces().OpenFile(ctx, environment, entryID)
	var body io.Reader = bytes.NewReader(nil)
	sha, size := "", int64(0)
	if err == nil {
		defer file.Body.Close()
		body, sha, size = file.Body, file.SHA256, file.SizeBytes
	} else {
		var refusal *plugin.CapabilityError
		if !errors.As(err, &refusal) {
			return nil, "", 0, err
		}
		if !strings.HasSuffix(refusal.Code, "not_found") {
			return nil, "", 0, o.workspaceError(err)
		}
	}
	reference, err := o.env.UploadProjectionContent(ctx, o.fence, body)
	if err != nil {
		return nil, "", 0, o.envError(err)
	}
	if sha == "" {
		sha, size = reference.Digest, reference.Size
	}
	return &reference, sha, size, nil
}

func (o *owner) saveProjection(ctx context.Context) error {
	versions := o.projected.Versions
	if o.ahead != nil && o.ahead.Cursor >= o.projected.Cursor {
		pruned := make(map[string]string, len(versions))
		for id, version := range versions {
			if _, live := o.ahead.byID[id]; live {
				pruned[id] = version
			}
		}
		o.projected.Versions, versions = pruned, pruned
	}
	record := projectionRecord{GrantID: o.grant.GrantID, Epoch: o.grant.Epoch, Cursor: o.projected.Cursor, Versions: versions}
	for range maxUpdateAttempts {
		var existing projectionRecord
		revision, _, err := o.records.get(ctx, kindProjection, attemptKey(o.attemptID), &existing)
		if err != nil {
			return err
		}
		if _, err = o.records.put(ctx, kindProjection, attemptKey(o.attemptID), record, revision); err == nil {
			return nil
		} else if !errors.Is(err, plugin.ErrRevisionConflict) {
			return err
		}
	}
	return plugin.ErrRevisionConflict
}

// restart replaces the grant with a new incarnation and records the gap.
func (o *owner) restart(ctx context.Context, reason string) error {
	o.logger.Info("restarting execution environment", slog.String("reason", reason))
	if o.env != nil {
		_ = o.env.Revoke(ctx)
	} else if o.grant.GrantID != "" && o.grant.Host != "" {
		if host, err := o.plugin.pool().get(ctx, o.grant.Host); err == nil {
			_ = host.Revoke(ctx, execenv.ID(o.grant.GrantID))
		}
	}
	if environment, ok := o.grant.bound(); ok {
		_, _ = o.plugin.host.AttemptWorkspaces().EndEnvironmentBinding(ctx, environment.AttemptID, environment.BindingID)
	}
	o.host, o.env, o.control, o.ready = nil, nil, "", false
	o.resetSync()
	grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
		if grant.GrantID != "" {
			grant.PendingGaps = append(grant.PendingGaps, gapNote{
				Kind: gapRestarted, ObservedAt: time.Now().UTC(), Detail: mustJSON(map[string]any{"reason": reason}),
			})
		}
		grant.Incarnation++
		grant.Host, grant.GrantID, grant.Epoch, grant.BindingID, grant.BoundGeneration = "", "", "", "", 0
		grant.FenceRevision, grant.State = 0, statePending
		return true, nil
	})
	if err != nil {
		return err
	}
	o.grant = grant
	_ = o.records.remove(ctx, kindProjection, attemptKey(o.attemptID))
	_ = o.records.remove(ctx, kindHarvest, attemptKey(o.attemptID))
	return nil
}

func (o *owner) dropHost() {
	if o.host != nil {
		o.plugin.pool().broken(o.grant.Host, o.host)
	}
	o.host, o.env, o.control, o.ready = nil, nil, "", false
}

// teardown destroys a revoked grant and finishes it.
func (o *owner) teardown(ctx context.Context) error {
	if o.env == nil && o.grant.GrantID != "" && o.grant.Host != "" {
		if host, err := o.plugin.pool().get(ctx, o.grant.Host); err == nil {
			o.host = host
		}
	}
	if o.host != nil && o.grant.GrantID != "" {
		if o.env != nil {
			grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
				grant.FenceRevision++
				return true, nil
			})
			if err == nil {
				o.grant = grant
				_, _ = o.env.Control(ctx, execenv.ControlRequest{State: execenv.ControlRevoked, Fence: execenv.ProjectionFence{
					ExecutionGrantID: execenv.ID(grant.GrantID), EnvironmentEpoch: grant.Epoch, FenceRevision: grant.FenceRevision,
				}})
			}
		}
		if err := o.host.Revoke(ctx, execenv.ID(o.grant.GrantID)); err != nil && !errors.Is(err, execenv.ErrRevoked) {
			o.logger.Warn("execution grant revoke failed", slog.String("error", errorClass(err)))
		}
	}
	if environment, ok := o.grant.bound(); ok {
		_, _ = o.plugin.host.AttemptWorkspaces().EndEnvironmentBinding(ctx, environment.AttemptID, environment.BindingID)
	}
	if _, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
		grant.State = stateDone
		return true, nil
	}); err != nil {
		return err
	}
	key := attemptKey(o.attemptID)
	for _, kind := range []string{kindProjection, kindHarvest, kindFlush, kindInput, kindLive} {
		_ = o.records.remove(ctx, kind, key)
	}
	_ = o.records.remove(ctx, kindSitting, sittingKey(o.grant.SittingID, o.attemptID))
	o.host, o.env = nil, nil
	return errDone
}

func mustJSON(value any) json.RawMessage {
	document, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return document
}
