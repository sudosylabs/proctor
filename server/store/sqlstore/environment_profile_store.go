// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLEnvironmentProfileStore implements store.EnvironmentProfileStore on
// environment_profiles.
type SQLEnvironmentProfileStore struct {
	*SQLStore
}

func newSQLEnvironmentProfileStore(sqlStore *SQLStore) store.EnvironmentProfileStore {
	return &SQLEnvironmentProfileStore{SQLStore: sqlStore}
}

type environmentProfileRow struct {
	ID                string       `db:"id"`
	Name              string       `db:"name"`
	Image             string       `db:"image"`
	CPUMillis         int64        `db:"cpu_millis"`
	MemoryBytes       int64        `db:"memory_bytes"`
	DiskBytes         int64        `db:"disk_bytes"`
	Network           string       `db:"network"`
	IdleWindowSeconds int64        `db:"idle_window_seconds"`
	MaxTerminals      int          `db:"max_terminals"`
	CreatedByUserID   string       `db:"created_by_user_id"`
	CreatedAt         time.Time    `db:"created_at"`
	RetiredAt         sql.NullTime `db:"retired_at"`
}

const environmentProfileColumns = `id, name, image, cpu_millis, memory_bytes, disk_bytes, network,
	idle_window_seconds, max_terminals, created_by_user_id, created_at, retired_at`

func (r environmentProfileRow) model() (*model.EnvironmentProfile, error) {
	profile := &model.EnvironmentProfile{
		ID:    model.EnvironmentProfileID(r.ID),
		Name:  r.Name,
		Image: r.Image,
		Resources: model.EnvironmentResources{CPUMillis: r.CPUMillis, MemoryBytes: r.MemoryBytes,
			DiskBytes: r.DiskBytes},
		Network:         model.EnvironmentNetwork(r.Network),
		IdleWindow:      time.Duration(r.IdleWindowSeconds) * time.Second,
		MaxTerminals:    r.MaxTerminals,
		CreatedByUserID: model.UserID(r.CreatedByUserID),
		CreatedAt:       model.TimeUTC(r.CreatedAt),
	}
	if r.RetiredAt.Valid {
		profile.RetiredAt = model.TimeUTC(r.RetiredAt.Time)
	}
	if err := profile.Validate(); err != nil {
		return nil, invalidPersistedState("environment_profile", "value", err)
	}
	return profile, nil
}

// environmentProfileDocument spells a profile inside a persisted outcome.
type environmentProfileDocument struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Image             string    `json:"image"`
	CPUMillis         int64     `json:"cpu_millis"`
	MemoryBytes       int64     `json:"memory_bytes"`
	DiskBytes         int64     `json:"disk_bytes"`
	Network           string    `json:"network"`
	IdleWindowSeconds int64     `json:"idle_window_seconds"`
	MaxTerminals      int       `json:"max_terminals"`
	CreatedByUserID   string    `json:"created_by_user_id"`
	CreatedAt         time.Time `json:"created_at"`
	RetiredAt         time.Time `json:"retired_at,omitzero"`
}

type environmentProfileOutcome struct {
	Profile environmentProfileDocument `json:"profile"`
	Changed bool                       `json:"changed"`
}

func (environmentProfileOutcome) outcomeDocument() {}

func environmentProfileOutcomeFrom(profile *model.EnvironmentProfile, changed bool) environmentProfileOutcome {
	return environmentProfileOutcome{Changed: changed, Profile: environmentProfileDocument{
		ID: profile.ID.String(), Name: profile.Name, Image: profile.Image,
		CPUMillis: profile.Resources.CPUMillis, MemoryBytes: profile.Resources.MemoryBytes, DiskBytes: profile.Resources.DiskBytes,
		Network: string(profile.Network), IdleWindowSeconds: int64(profile.IdleWindow / time.Second),
		MaxTerminals: profile.MaxTerminals, CreatedByUserID: profile.CreatedByUserID.String(),
		CreatedAt: profile.CreatedAt, RetiredAt: profile.RetiredAt,
	}}
}

func (o environmentProfileOutcome) result(replayed bool) (*store.EnvironmentProfileCommandResult, error) {
	row := environmentProfileRow{ID: o.Profile.ID, Name: o.Profile.Name, Image: o.Profile.Image,
		CPUMillis: o.Profile.CPUMillis, MemoryBytes: o.Profile.MemoryBytes, DiskBytes: o.Profile.DiskBytes,
		Network: o.Profile.Network, IdleWindowSeconds: o.Profile.IdleWindowSeconds, MaxTerminals: o.Profile.MaxTerminals,
		CreatedByUserID: o.Profile.CreatedByUserID, CreatedAt: o.Profile.CreatedAt}
	if !o.Profile.RetiredAt.IsZero() {
		row.RetiredAt = sql.NullTime{Time: o.Profile.RetiredAt, Valid: true}
	}
	profile, err := row.model()
	if err != nil {
		return nil, err
	}
	return &store.EnvironmentProfileCommandResult{Profile: profile, Changed: o.Changed, Replayed: replayed}, nil
}

func decodeEnvironmentProfileOutcome(encoded []byte) (environmentProfileOutcome, error) {
	var outcome environmentProfileOutcome
	if err := decodeOutcome(encoded, &outcome); err != nil {
		return environmentProfileOutcome{}, err
	}
	if _, err := outcome.result(false); err != nil {
		return environmentProfileOutcome{}, invalidPersistedState("command_outcome", "outcome",
			errors.New("invalid environment profile outcome"))
	}
	return outcome, nil
}

// Create saves a new profile for an active system administrator.
func (s SQLEnvironmentProfileStore) Create(
	ctx context.Context,
	input *store.EnvironmentProfileCreation,
	command *store.CommandIdempotency,
) (*store.EnvironmentProfileCommandResult, error) {
	if input == nil || input.Profile == nil || input.Profile.Validate() != nil || input.Profile.Retired() ||
		!input.AuditEventID.IsValid() || input.AuditAt.IsZero() || command == nil ||
		command.UserID != input.Profile.CreatedByUserID || command.Operation != store.EnvironmentProfileCreateOperation {
		return nil, store.NewErrInvalidInput("environment_profile", "creation", nil)
	}
	mutation, err := runIdempotentMutation(ctx, s.SQLStore, "environment profile creation",
		idempotentMutation[environmentProfileOutcome]{
			command:      command,
			auditEventID: input.AuditEventID,
			execute: func(ctx context.Context, tx *sqlxTxWrapper) (environmentProfileOutcome, error) {
				return createEnvironmentProfile(ctx, tx, input)
			},
			encode: func(outcome environmentProfileOutcome) ([]byte, error) { return encodeOutcome(outcome) },
			decode: decodeEnvironmentProfileOutcome,
			completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, outcome environmentProfileOutcome, originalAuditID string) error {
				return completeEnvironmentProfileAudit(ctx, tx, "create", outcome, input.AuditEventID, input.AuditAt, true, originalAuditID)
			},
		})
	if err != nil {
		return nil, err
	}
	return mutation.Value.result(mutation.Replayed)
}

func createEnvironmentProfile(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.EnvironmentProfileCreation,
) (environmentProfileOutcome, error) {
	databaseNow, err := requireSystemAdministrator(ctx, tx, "environment_profile", input.Profile.CreatedByUserID)
	if err != nil {
		return environmentProfileOutcome{}, err
	}
	var live int
	if err = tx.Get(ctx, &live, `SELECT COUNT(*) FROM environment_profiles WHERE retired_at IS NULL`); err != nil {
		return environmentProfileOutcome{}, fmt.Errorf("count environment profiles: %w", err)
	}
	if live >= model.EnvironmentProfileListMaximum {
		return environmentProfileOutcome{}, store.NewErrConflict("environment_profile", "environment_profile_limit", nil)
	}
	profile := *input.Profile
	profile.CreatedAt = databaseNow
	if _, err = tx.Exec(ctx, `INSERT INTO environment_profiles (`+environmentProfileColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		profile.ID.String(), profile.Name, profile.Image, profile.Resources.CPUMillis, profile.Resources.MemoryBytes,
		profile.Resources.DiskBytes, string(profile.Network), int64(profile.IdleWindow/time.Second), profile.MaxTerminals,
		profile.CreatedByUserID.String(), profile.CreatedAt); err != nil {
		translated := translateError("environment_profile", profile.ID.String(), err)
		var conflict *store.ErrConflict
		if errors.As(translated, &conflict) && conflict.Constraint == "environment_profiles_live_name_key" {
			return environmentProfileOutcome{}, store.NewErrConflict("environment_profile", "environment_profile_name", err)
		}
		return environmentProfileOutcome{}, fmt.Errorf("insert environment profile: %w", translated)
	}
	outcome := environmentProfileOutcomeFrom(&profile, true)
	if err = completeEnvironmentProfileAudit(ctx, tx, "create", outcome, input.AuditEventID, input.AuditAt, false, ""); err != nil {
		return environmentProfileOutcome{}, err
	}
	return outcome, nil
}

// Retire retires one profile for an active system administrator.
func (s SQLEnvironmentProfileStore) Retire(
	ctx context.Context,
	input *store.EnvironmentProfileRetirement,
	command *store.CommandIdempotency,
) (*store.EnvironmentProfileCommandResult, error) {
	if input == nil || !input.ActorID.IsValid() || !input.ProfileID.IsValid() || !input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() || command == nil || command.UserID != input.ActorID ||
		command.Operation != store.EnvironmentProfileRetireOperation {
		return nil, store.NewErrInvalidInput("environment_profile", "retirement", nil)
	}
	mutation, err := runIdempotentMutation(ctx, s.SQLStore, "environment profile retirement",
		idempotentMutation[environmentProfileOutcome]{
			command:      command,
			auditEventID: input.AuditEventID,
			execute: func(ctx context.Context, tx *sqlxTxWrapper) (environmentProfileOutcome, error) {
				return retireEnvironmentProfile(ctx, tx, input)
			},
			encode: func(outcome environmentProfileOutcome) ([]byte, error) { return encodeOutcome(outcome) },
			decode: decodeEnvironmentProfileOutcome,
			completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, outcome environmentProfileOutcome, originalAuditID string) error {
				return completeEnvironmentProfileAudit(ctx, tx, "retire", outcome, input.AuditEventID, input.AuditAt, true, originalAuditID)
			},
		})
	if err != nil {
		return nil, err
	}
	return mutation.Value.result(mutation.Replayed)
}

func retireEnvironmentProfile(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.EnvironmentProfileRetirement,
) (environmentProfileOutcome, error) {
	databaseNow, err := requireSystemAdministrator(ctx, tx, "environment_profile", input.ActorID)
	if err != nil {
		return environmentProfileOutcome{}, err
	}
	var row environmentProfileRow
	if err = tx.Get(ctx, &row, `SELECT `+environmentProfileColumns+` FROM environment_profiles WHERE id = ? FOR UPDATE`,
		input.ProfileID.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return environmentProfileOutcome{}, store.NewErrNotFound("environment_profile", input.ProfileID.String())
		}
		return environmentProfileOutcome{}, fmt.Errorf("lock environment profile: %w", err)
	}
	profile, err := row.model()
	if err != nil {
		return environmentProfileOutcome{}, err
	}
	changed := !profile.Retired()
	if changed {
		profile.RetiredAt = transitionInstant(databaseNow, profile.CreatedAt, profile.CreatedAt)
		if _, err = tx.Exec(ctx, `UPDATE environment_profiles SET retired_at = ? WHERE id = ? AND retired_at IS NULL`,
			profile.RetiredAt, profile.ID.String()); err != nil {
			return environmentProfileOutcome{}, fmt.Errorf("retire environment profile: %w", err)
		}
	}
	outcome := environmentProfileOutcomeFrom(profile, changed)
	if err = completeEnvironmentProfileAudit(ctx, tx, "retire", outcome, input.AuditEventID, input.AuditAt, false, ""); err != nil {
		return environmentProfileOutcome{}, err
	}
	return outcome, nil
}

func completeEnvironmentProfileAudit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	operation string,
	outcome environmentProfileOutcome,
	auditEventID model.AuditEventID,
	auditAt time.Time,
	replayed bool,
	originalAuditID string,
) error {
	data := map[string]any{
		"operation":              operation,
		"environment_profile_id": outcome.Profile.ID,
		"name":                   outcome.Profile.Name,
		"image":                  outcome.Profile.Image,
		"changed":                outcome.Changed,
	}
	if replayed {
		data["idempotency_replayed"] = true
		data["original_audit_event_id"] = originalAuditID
	}
	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return store.NewErrInvalidInput("environment_profile", "audit", nil).Wrap(err)
	}
	if _, err = completeAuditEvent(ctx, tx, auditEventID, model.AuditStatusSuccess, "", encoded, auditAt); err != nil {
		return fmt.Errorf("complete environment profile audit: %w", err)
	}
	return nil
}

// Get returns one profile, retired or not.
func (s SQLEnvironmentProfileStore) Get(ctx context.Context, id model.EnvironmentProfileID) (*model.EnvironmentProfile, error) {
	if !id.IsValid() {
		return nil, store.NewErrInvalidInput("environment_profile", "id", nil)
	}
	var row environmentProfileRow
	if err := s.GetMaster().Get(ctx, &row, `SELECT `+environmentProfileColumns+` FROM environment_profiles WHERE id = ?`,
		id.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.NewErrNotFound("environment_profile", id.String())
		}
		return nil, fmt.Errorf("get environment profile: %w", err)
	}
	return row.model()
}

// List returns the profiles that are not retired by name, then, when asked,
// the retired ones, newest retirement first.
func (s SQLEnvironmentProfileStore) List(ctx context.Context, includeRetired bool) ([]*model.EnvironmentProfile, error) {
	query := `SELECT ` + environmentProfileColumns + ` FROM environment_profiles WHERE retired_at IS NULL ORDER BY name`
	if includeRetired {
		query = `SELECT ` + environmentProfileColumns + ` FROM environment_profiles
			ORDER BY retired_at IS NOT NULL, CASE WHEN retired_at IS NULL THEN name END, retired_at DESC, id`
	}
	var rows []environmentProfileRow
	if err := s.GetMaster().Select(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("list environment profiles: %w", err)
	}
	profiles := make([]*model.EnvironmentProfile, 0, len(rows))
	for _, row := range rows {
		profile, err := row.model()
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

var _ store.EnvironmentProfileStore = (*SQLEnvironmentProfileStore)(nil)
