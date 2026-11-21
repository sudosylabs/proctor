// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLAttemptEnvironmentStore implements store.AttemptEnvironmentStore on
// exam_attempt_environments.
type SQLAttemptEnvironmentStore struct{ *SQLStore }

func newSQLAttemptEnvironmentStore(sqlStore *SQLStore) store.AttemptEnvironmentStore {
	return &SQLAttemptEnvironmentStore{SQLStore: sqlStore}
}

const attemptEnvironmentColumns = `exam_attempt_id, exam_sitting_id, environment_profile_id, image, cpu_millis,
	memory_bytes, disk_bytes, network, idle_window_seconds, terminals, record_input, preview_policy, desired, state, generation,
	incarnation, host, grant_id, epoch, control_fence, projection_cursor, observation_cursor, process_cursor,
	projected_versions, evidence_sequence, waiting_since, settle_requested, settle_completed, settle_requested_at,
	sync_paths, replace_before, revision, created_at, updated_at`

type attemptEnvironmentRow struct {
	AttemptID         string         `db:"exam_attempt_id"`
	SittingID         string         `db:"exam_sitting_id"`
	ProfileID         string         `db:"environment_profile_id"`
	Image             string         `db:"image"`
	CPUMillis         int64          `db:"cpu_millis"`
	MemoryBytes       int64          `db:"memory_bytes"`
	DiskBytes         int64          `db:"disk_bytes"`
	Network           string         `db:"network"`
	IdleWindowSeconds int64          `db:"idle_window_seconds"`
	Terminals         int            `db:"terminals"`
	RecordInput       bool           `db:"record_input"`
	PreviewPolicy     []byte         `db:"preview_policy"`
	Desired           string         `db:"desired"`
	State             string         `db:"state"`
	Generation        int64          `db:"generation"`
	Incarnation       int64          `db:"incarnation"`
	Host              sql.NullString `db:"host"`
	GrantID           sql.NullString `db:"grant_id"`
	Epoch             sql.NullString `db:"epoch"`
	ControlFence      int64          `db:"control_fence"`
	ProjectionCursor  int64          `db:"projection_cursor"`
	ObservationCursor int64          `db:"observation_cursor"`
	ProcessCursor     int64          `db:"process_cursor"`
	ProjectedVersions []byte         `db:"projected_versions"`
	EvidenceSequence  int64          `db:"evidence_sequence"`
	SettleRequested   int64          `db:"settle_requested"`
	SettleCompleted   int64          `db:"settle_completed"`
	SettleRequestedAt sql.NullTime   `db:"settle_requested_at"`
	SyncPaths         []byte         `db:"sync_paths"`
	ReplaceBefore     int64          `db:"replace_before"`
	WaitingSince      sql.NullTime   `db:"waiting_since"`
	Revision          int64          `db:"revision"`
	CreatedAt         time.Time      `db:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at"`
}

func (r attemptEnvironmentRow) model() (*model.AttemptEnvironment, error) {
	environment := &model.AttemptEnvironment{
		AttemptID: model.ExamAttemptID(r.AttemptID), SittingID: model.ExamSittingID(r.SittingID),
		ProfileID: model.EnvironmentProfileID(r.ProfileID), Image: r.Image,
		Resources:  model.EnvironmentResources{CPUMillis: r.CPUMillis, MemoryBytes: r.MemoryBytes, DiskBytes: r.DiskBytes},
		Network:    model.EnvironmentNetwork(r.Network),
		IdleWindow: time.Duration(r.IdleWindowSeconds) * time.Second, Terminals: r.Terminals, RecordInput: r.RecordInput,
		Desired: model.AttemptEnvironmentDesired(r.Desired), State: model.AttemptEnvironmentState(r.State),
		Generation: r.Generation, Incarnation: r.Incarnation,
		Host: r.Host.String, GrantID: r.GrantID.String, Epoch: r.Epoch.String,
		ControlFence: r.ControlFence, ProjectionCursor: r.ProjectionCursor, ObservationCursor: r.ObservationCursor,
		ProcessCursor: r.ProcessCursor, EvidenceSequence: r.EvidenceSequence, Revision: r.Revision,
		SettleRequested: r.SettleRequested, SettleCompleted: r.SettleCompleted, ReplaceBefore: r.ReplaceBefore,
		CreatedAt: model.TimeUTC(r.CreatedAt), UpdatedAt: model.TimeUTC(r.UpdatedAt),
	}
	if err := json.Unmarshal(r.PreviewPolicy, &environment.Preview); err != nil {
		return nil, invalidPersistedState("attempt_environment", "preview_policy", err)
	}
	if err := json.Unmarshal(r.ProjectedVersions, &environment.ProjectedVersions); err != nil {
		return nil, invalidPersistedState("attempt_environment", "projected_versions", err)
	}
	if r.WaitingSince.Valid {
		environment.WaitingSince = model.TimeUTC(r.WaitingSince.Time)
	}
	if r.SettleRequestedAt.Valid {
		environment.SettleRequestedAt = model.TimeUTC(r.SettleRequestedAt.Time)
	}
	syncPaths, err := decodeAttemptEnvironmentSyncPaths(r.SyncPaths)
	if err != nil {
		return nil, invalidPersistedState("attempt_environment", "sync_paths", err)
	}
	environment.SyncPaths = syncPaths
	if err := environment.Validate(); err != nil {
		return nil, invalidPersistedState("attempt_environment", "value", err)
	}
	return environment, nil
}

// Get returns one attempt's environment.
func (s SQLAttemptEnvironmentStore) Get(ctx context.Context, attemptID model.ExamAttemptID) (*model.AttemptEnvironment, error) {
	if !attemptID.IsValid() {
		return nil, store.NewErrInvalidInput("attempt_environment", "attempt_id", nil)
	}
	var row attemptEnvironmentRow
	if err := s.GetMaster().Get(ctx, &row, `SELECT `+attemptEnvironmentColumns+`
		FROM exam_attempt_environments WHERE exam_attempt_id = ?`, attemptID.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.NewErrNotFound("attempt_environment", attemptID.String())
		}
		return nil, fmt.Errorf("get attempt environment: %w", err)
	}
	return row.model()
}

// ApplyFact applies one committed attempt or sitting fact.
func (s SQLAttemptEnvironmentStore) ApplyFact(
	ctx context.Context,
	fact model.AttemptEnvironmentFact,
) (*store.AttemptEnvironmentFactResult, error) {
	attemptFact := fact.Kind != model.AttemptEnvironmentFactSittingPaused &&
		fact.Kind != model.AttemptEnvironmentFactSittingResumed && fact.Kind != model.AttemptEnvironmentFactSittingClosing
	if !fact.SittingID.IsValid() || (attemptFact && !fact.AttemptID.IsValid()) {
		return nil, store.NewErrInvalidInput("attempt_environment", "fact", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "apply attempt environment fact", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.AttemptEnvironmentFactResult, error) {
		var now time.Time
		if err := tx.Get(ctx, &now, `SELECT clock_timestamp()`); err != nil {
			return nil, fmt.Errorf("read attempt environment fact time: %w", err)
		}
		now = now.UTC().Truncate(time.Microsecond)
		var sittingState string
		if err := tx.Get(ctx, &sittingState, `SELECT state FROM exam_sittings WHERE id = ?`, fact.SittingID.String()); err != nil {
			return nil, fmt.Errorf("read the fact's Sitting: %w", translateError("exam_sitting", fact.SittingID.String(), err))
		}
		paused := sittingState == "paused"

		if !attemptFact {
			var rows []attemptEnvironmentRow
			if err := tx.Select(ctx, &rows, `SELECT `+attemptEnvironmentColumns+`
				FROM exam_attempt_environments
				WHERE exam_sitting_id = ? AND desired <> 'ended'
				ORDER BY exam_attempt_id
				FOR UPDATE`, fact.SittingID.String()); err != nil {
				return nil, fmt.Errorf("lock the Sitting's attempt environments: %w", err)
			}
			result := &store.AttemptEnvironmentFactResult{}
			for _, row := range rows {
				environment, err := row.model()
				if err != nil {
					return nil, err
				}
				active, err := attemptParticipationActive(ctx, tx, environment.AttemptID)
				if err != nil {
					return nil, err
				}
				if !environment.ApplyFact(fact, active, paused, now) {
					continue
				}
				if err = saveAttemptEnvironmentFact(ctx, tx, environment); err != nil {
					return nil, err
				}
				result.Changed = append(result.Changed, environment.AttemptID)
			}
			return result, nil
		}

		var row attemptEnvironmentRow
		err := tx.Get(ctx, &row, `SELECT `+attemptEnvironmentColumns+`
			FROM exam_attempt_environments WHERE exam_attempt_id = ? FOR UPDATE`, fact.AttemptID.String())
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if fact.Kind != model.AttemptEnvironmentFactParticipationStarted {
				return &store.AttemptEnvironmentFactResult{}, nil
			}
			return createAttemptEnvironment(ctx, tx, fact, paused, now)
		case err != nil:
			return nil, fmt.Errorf("lock attempt environment: %w", err)
		}
		environment, err := row.model()
		if err != nil {
			return nil, err
		}
		active, err := attemptParticipationActive(ctx, tx, environment.AttemptID)
		if err != nil {
			return nil, err
		}
		result := &store.AttemptEnvironmentFactResult{Environment: environment}
		if environment.ApplyFact(fact, active, paused, now) {
			if err = saveAttemptEnvironmentFact(ctx, tx, environment); err != nil {
				return nil, err
			}
			result.Changed = []model.ExamAttemptID{environment.AttemptID}
		}
		return result, nil
	})
}

// createAttemptEnvironment creates the environment of an attempt admitted to
// a Revision whose execution policy is enabled, copying the profile it names.
func createAttemptEnvironment(
	ctx context.Context,
	tx *sqlxTxWrapper,
	fact model.AttemptEnvironmentFact,
	paused bool,
	now time.Time,
) (*store.AttemptEnvironmentFactResult, error) {
	var canonical []byte
	if err := tx.Get(ctx, &canonical, `SELECT revision.policy_canonical
		FROM exam_attempts attempt
		JOIN exam_revisions revision ON revision.id = attempt.admission_revision_id
		WHERE attempt.id = ? AND attempt.exam_sitting_id = ?`, fact.AttemptID.String(), fact.SittingID.String()); err != nil {
		return nil, fmt.Errorf("read the Attempt's execution policy: %w", translateError("exam_attempt", fact.AttemptID.String(), err))
	}
	policy, err := model.DecodeExamPolicySet(canonical)
	if err != nil {
		return nil, invalidPersistedState("exam_revision", "policy_canonical", err)
	}
	if !policy.Execution.Enabled {
		return &store.AttemptEnvironmentFactResult{}, nil
	}
	var profileRow environmentProfileRow
	if err = tx.Get(ctx, &profileRow, `SELECT `+environmentProfileColumns+` FROM environment_profiles WHERE id = ?`,
		policy.Execution.Profile.String()); err != nil {
		return nil, fmt.Errorf("read the execution policy's profile: %w", err)
	}
	profile, err := profileRow.model()
	if err != nil {
		return nil, err
	}
	desired := model.AttemptEnvironmentDesiredRunning
	if paused {
		desired = model.AttemptEnvironmentDesiredFrozen
	}
	environment := &model.AttemptEnvironment{
		AttemptID: fact.AttemptID, SittingID: fact.SittingID, ProfileID: profile.ID, Image: profile.Image,
		Resources: profile.Resources, Network: profile.Network, IdleWindow: profile.IdleWindow,
		Terminals: policy.Execution.Terminals, RecordInput: policy.Execution.RecordInput, Preview: policy.Execution.Preview,
		Desired: desired, State: model.AttemptEnvironmentPending, Generation: fact.Generation,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err = environment.Validate(); err != nil {
		return nil, store.NewErrInvalidInput("attempt_environment", "creation", nil).Wrap(err)
	}
	preview, err := json.Marshal(environment.Preview.Clone())
	if err != nil {
		return nil, fmt.Errorf("encode attempt preview policy: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO exam_attempt_environments (`+attemptEnvironmentColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, NULL, NULL, 0, 0, 0, 0, '{}', 0, NULL, 0, 0, NULL, '[]', 0, 1,
			?, ?)`,
		environment.AttemptID.String(), environment.SittingID.String(), environment.ProfileID.String(), environment.Image,
		environment.Resources.CPUMillis, environment.Resources.MemoryBytes, environment.Resources.DiskBytes,
		string(environment.Network), int64(environment.IdleWindow/time.Second), environment.Terminals, environment.RecordInput, preview,
		string(environment.Desired), string(environment.State), environment.Generation, now, now); err != nil {
		return nil, fmt.Errorf("create attempt environment: %w", translateError("attempt_environment", environment.AttemptID.String(), err))
	}
	return &store.AttemptEnvironmentFactResult{Environment: environment, Changed: []model.ExamAttemptID{environment.AttemptID}}, nil
}

func attemptParticipationActive(ctx context.Context, tx *sqlxTxWrapper, attemptID model.ExamAttemptID) (bool, error) {
	var active bool
	if err := tx.Get(ctx, &active, `SELECT EXISTS (
			SELECT 1 FROM exam_attempt_participations WHERE exam_attempt_id = ? AND state = 'active'
		)`, attemptID.String()); err != nil {
		return false, fmt.Errorf("read the Attempt's Participation: %w", err)
	}
	return active, nil
}

func saveAttemptEnvironmentFact(ctx context.Context, tx *sqlxTxWrapper, environment *model.AttemptEnvironment) error {
	if _, err := tx.Exec(ctx, `UPDATE exam_attempt_environments
		SET desired = ?, generation = ?, settle_requested = ?, settle_requested_at = ?, revision = ?, updated_at = ?
		WHERE exam_attempt_id = ?`,
		string(environment.Desired), environment.Generation, environment.SettleRequested,
		nullableEnvironmentTime(environment.SettleRequestedAt), environment.Revision, environment.UpdatedAt,
		environment.AttemptID.String()); err != nil {
		return fmt.Errorf("save attempt environment fact: %w", err)
	}
	return nil
}

// SaveOwner writes an owner's state under its lease and the revision it
// read.
func (s SQLAttemptEnvironmentStore) SaveOwner(
	ctx context.Context,
	input *store.AttemptEnvironmentOwnerSave,
) (*model.AttemptEnvironment, error) {
	if input == nil || !input.Lease.AttemptID.IsValid() || input.Lease.Fence < 1 || input.Lease.NodeID == "" ||
		input.ExpectedRevision < 1 || input.Environment.AttemptID != input.Lease.AttemptID {
		return nil, store.NewErrInvalidInput("attempt_environment", "owner_save", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "save attempt environment owner state", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*model.AttemptEnvironment, error) {
		var held bool
		if err := tx.Get(ctx, &held, `SELECT EXISTS (
				SELECT 1 FROM attempt_runtime_leases
				WHERE exam_attempt_id = ? AND node_id = ? AND fence = ? AND expires_at > clock_timestamp()
				FOR SHARE
			)`, input.Lease.AttemptID.String(), input.Lease.NodeID, input.Lease.Fence); err != nil {
			return nil, fmt.Errorf("check attempt runtime lease: %w", err)
		}
		if !held {
			return nil, store.ErrAttemptRuntimeLeaseLost
		}
		var row attemptEnvironmentRow
		if err := tx.Get(ctx, &row, `SELECT `+attemptEnvironmentColumns+`
			FROM exam_attempt_environments WHERE exam_attempt_id = ? FOR UPDATE`, input.Lease.AttemptID.String()); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, store.NewErrNotFound("attempt_environment", input.Lease.AttemptID.String())
			}
			return nil, fmt.Errorf("lock attempt environment: %w", err)
		}
		current, err := row.model()
		if err != nil {
			return nil, err
		}
		if current.Revision != input.ExpectedRevision {
			return nil, store.NewErrConflict("attempt_environment", "attempt_environment_revision", nil)
		}
		var now time.Time
		if err = tx.Get(ctx, &now, `SELECT clock_timestamp()`); err != nil {
			return nil, fmt.Errorf("read attempt environment save time: %w", err)
		}
		saved := *current
		owner := input.Environment
		saved.State, saved.Incarnation = owner.State, owner.Incarnation
		saved.Host, saved.GrantID, saved.Epoch = owner.Host, owner.GrantID, owner.Epoch
		saved.ControlFence, saved.ProjectionCursor = owner.ControlFence, owner.ProjectionCursor
		saved.ObservationCursor, saved.ProcessCursor = owner.ObservationCursor, owner.ProcessCursor
		saved.ProjectedVersions, saved.EvidenceSequence = maps.Clone(owner.ProjectedVersions), owner.EvidenceSequence
		saved.WaitingSince = owner.WaitingSince
		saved.SyncPaths = slices.Clone(owner.SyncPaths)
		if owner.SettleCompleted < current.SettleCompleted || owner.SettleCompleted > current.SettleRequested {
			return nil, store.NewErrInvalidInput("attempt_environment", "settle", nil)
		}
		saved.SettleCompleted = owner.SettleCompleted
		saved.Revision = current.Revision + 1
		saved.UpdatedAt = transitionInstant(now.UTC().Truncate(time.Microsecond), current.CreatedAt, current.UpdatedAt)
		if saved.State == model.AttemptEnvironmentEnded && saved.Desired != model.AttemptEnvironmentDesiredEnded {
			return nil, store.NewErrInvalidInput("attempt_environment", "state", nil)
		}
		if err = saved.Validate(); err != nil {
			return nil, store.NewErrInvalidInput("attempt_environment", "owner_save", nil).Wrap(err)
		}
		versions, err := encodeProjectedVersions(saved.ProjectedVersions)
		if err != nil {
			return nil, err
		}
		syncPaths, err := encodeAttemptEnvironmentSyncPaths(saved.SyncPaths)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_environments
			SET state = ?, incarnation = ?, host = NULLIF(?, ''), grant_id = NULLIF(?, ''), epoch = NULLIF(?, ''),
				control_fence = ?, projection_cursor = ?, observation_cursor = ?, process_cursor = ?,
				projected_versions = ?, evidence_sequence = ?, waiting_since = ?, settle_completed = ?, sync_paths = ?,
				revision = ?, updated_at = ?
			WHERE exam_attempt_id = ?`,
			string(saved.State), saved.Incarnation, saved.Host, saved.GrantID, saved.Epoch, saved.ControlFence,
			saved.ProjectionCursor, saved.ObservationCursor, saved.ProcessCursor, versions, saved.EvidenceSequence,
			nullableEnvironmentTime(saved.WaitingSince), saved.SettleCompleted, syncPaths,
			saved.Revision, saved.UpdatedAt, saved.AttemptID.String()); err != nil {
			return nil, fmt.Errorf("save attempt environment owner state: %w", err)
		}
		return &saved, nil
	})
}

// encodeProjectedVersions writes the versions as a JSON object, never null.
func encodeProjectedVersions(versions map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion) ([]byte, error) {
	if versions == nil {
		versions = map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion{}
	}
	encoded, err := json.Marshal(versions)
	if err != nil {
		return nil, fmt.Errorf("encode projected versions: %w", err)
	}
	return encoded, nil
}

// attemptEnvironmentSyncPathDocument is the persisted spelling of one
// guest path's sync status.
type attemptEnvironmentSyncPathDocument struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// encodeAttemptEnvironmentSyncPaths writes the statuses as a JSON array,
// never null.
func encodeAttemptEnvironmentSyncPaths(paths []model.AttemptEnvironmentSyncPath) ([]byte, error) {
	documents := make([]attemptEnvironmentSyncPathDocument, len(paths))
	for index, path := range paths {
		documents[index] = attemptEnvironmentSyncPathDocument{Path: path.Path, Status: string(path.Status), Reason: path.Reason}
	}
	encoded, err := json.Marshal(documents)
	if err != nil {
		return nil, fmt.Errorf("encode attempt environment sync paths: %w", err)
	}
	return encoded, nil
}

func decodeAttemptEnvironmentSyncPaths(encoded []byte) ([]model.AttemptEnvironmentSyncPath, error) {
	var documents []attemptEnvironmentSyncPathDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&documents); err != nil {
		return nil, err
	}
	if len(documents) == 0 {
		return nil, nil
	}
	paths := make([]model.AttemptEnvironmentSyncPath, len(documents))
	for index, document := range documents {
		paths[index] = model.AttemptEnvironmentSyncPath{Path: document.Path,
			Status: model.AttemptEnvironmentSyncStatus(document.Status), Reason: document.Reason}
	}
	return paths, nil
}

func nullableEnvironmentTime(at time.Time) sql.NullTime {
	return sql.NullTime{Time: at, Valid: !at.IsZero()}
}

// RequestSettle asks the attempt's environment for one more settle.
func (s SQLAttemptEnvironmentStore) RequestSettle(ctx context.Context, attemptID model.ExamAttemptID) (int64, error) {
	if !attemptID.IsValid() {
		return 0, store.NewErrInvalidInput("attempt_environment", "attempt_id", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "request attempt environment settle", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (int64, error) {
		var row attemptEnvironmentRow
		err := tx.Get(ctx, &row, `SELECT `+attemptEnvironmentColumns+`
			FROM exam_attempt_environments WHERE exam_attempt_id = ? FOR UPDATE`, attemptID.String())
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return 0, nil
		case err != nil:
			return 0, fmt.Errorf("lock attempt environment: %w", err)
		}
		environment, err := row.model()
		if err != nil {
			return 0, err
		}
		var now time.Time
		if err = tx.Get(ctx, &now, `SELECT clock_timestamp()`); err != nil {
			return 0, fmt.Errorf("read attempt environment settle time: %w", err)
		}
		settle := environment.RequestSettle(now.UTC().Truncate(time.Microsecond))
		if settle == 0 {
			return 0, nil
		}
		if err = saveAttemptEnvironmentFact(ctx, tx, environment); err != nil {
			return 0, err
		}
		return settle, nil
	})
}

// RecordSettleIncomplete records a settle the seal stopped waiting for.
func (s SQLAttemptEnvironmentStore) RecordSettleIncomplete(ctx context.Context, input *store.AttemptEnvironmentSettleGap) error {
	if input == nil || !input.AttemptID.IsValid() || input.Settle < 1 || !input.EvidenceID.IsValid() || input.ObservedAt.IsZero() {
		return store.NewErrInvalidInput("attempt_environment", "settle_gap", nil)
	}
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "record incomplete attempt environment settle", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (struct{}, error) {
		// The attempt row first, as the seal and the environment's writes
		// take it, so no seal interleaves.
		var attemptState string
		if err := tx.Get(ctx, &attemptState, `SELECT state FROM exam_attempts WHERE id = ? FOR UPDATE`,
			input.AttemptID.String()); err != nil {
			return struct{}{}, translateError("exam_attempt", input.AttemptID.String(), err)
		}
		var row attemptEnvironmentRow
		if err := tx.Get(ctx, &row, `SELECT `+attemptEnvironmentColumns+`
			FROM exam_attempt_environments WHERE exam_attempt_id = ? FOR UPDATE`, input.AttemptID.String()); err != nil {
			return struct{}{}, translateError("attempt_environment", input.AttemptID.String(), err)
		}
		environment, err := row.model()
		if err != nil {
			return struct{}{}, err
		}
		if input.Settle > environment.SettleRequested {
			return struct{}{}, store.NewErrInvalidInput("attempt_environment", "settle", nil)
		}
		if environment.SettleCompleted >= input.Settle || attemptState == string(model.ExamAttemptSubmitted) {
			return struct{}{}, nil
		}
		detail, err := json.Marshal(map[string]any{"gap": "settle_incomplete", "settle": input.Settle})
		if err != nil {
			return struct{}{}, err
		}
		author := model.NewEnvironmentWorkspaceAuthor(environment.Incarnation, "settle")
		if _, err = appendAttemptEvidence(ctx, tx, input.AttemptID, input.EvidenceID, model.AttemptEvidenceGap, author,
			input.Settle, detail, input.ObservedAt); err != nil {
			return struct{}{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_environments
			SET settle_completed = ?, revision = revision + 1, updated_at = GREATEST(updated_at, clock_timestamp())
			WHERE exam_attempt_id = ?`, input.Settle, input.AttemptID.String()); err != nil {
			return struct{}{}, fmt.Errorf("complete attempt environment settle: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

var _ store.AttemptEnvironmentStore = (*SQLAttemptEnvironmentStore)(nil)
