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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLPluginSettingsStore implements store.PluginSettingsStore on
// plugin_settings, one row per plugin an administrator configured, fenced by
// its revision.
type SQLPluginSettingsStore struct {
	*SQLStore
}

type pluginSettingsRow struct {
	PluginID  string    `db:"plugin_id"`
	Revision  int64     `db:"revision"`
	Enabled   bool      `db:"enabled"`
	Settings  jsonValue `db:"settings"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// pluginSettingsReplacementOutcome is the committed result of a plugin
// settings replacement.
type pluginSettingsReplacementOutcome struct {
	Settings *model.PluginSettings
	Changed  bool
}

// pluginSettingsReplacementDocument is its persisted outcome.
type pluginSettingsReplacementDocument struct {
	Settings pluginSettingsDocument `json:"settings"`
	Changed  bool                   `json:"changed"`
}

func (pluginSettingsReplacementDocument) outcomeDocument() {}

// pluginSettingsDocument spells model.PluginSettings inside a persisted
// outcome; the two convert directly.
type pluginSettingsDocument struct {
	PluginID  model.PluginID  `json:"plugin_id"`
	Revision  int64           `json:"revision"`
	Enabled   bool            `json:"enabled"`
	Settings  json.RawMessage `json:"settings"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func newSQLPluginSettingsStore(sqlStore *SQLStore) store.PluginSettingsStore {
	return &SQLPluginSettingsStore{SQLStore: sqlStore}
}

// List returns every saved plugin's settings, ordered by plugin ID.
func (s SQLPluginSettingsStore) List(ctx context.Context) ([]*model.PluginSettings, error) {
	var rows []pluginSettingsRow
	if err := s.GetMaster().Select(ctx, &rows, `SELECT plugin_id, revision, enabled, settings, created_at, updated_at
		FROM plugin_settings ORDER BY plugin_id`); err != nil {
		return nil, fmt.Errorf("list plugin settings: %w", err)
	}
	settings := make([]*model.PluginSettings, 0, len(rows))
	for _, row := range rows {
		value, err := row.model()
		if err != nil {
			return nil, err
		}
		settings = append(settings, value)
	}
	return settings, nil
}

// ChangeCount is the sum of every row's revision. Rows are never deleted
// and each change raises one revision, so the sum grows with every change.
func (s SQLPluginSettingsStore) ChangeCount(ctx context.Context) (int64, error) {
	var count int64
	if err := s.GetMaster().Get(ctx, &count, `SELECT COALESCE(SUM(revision), 0)::bigint FROM plugin_settings`); err != nil {
		return 0, fmt.Errorf("count plugin settings changes: %w", err)
	}
	return count, nil
}

// Replace saves one plugin's settings for an active System Administrator.
func (s SQLPluginSettingsStore) Replace(
	ctx context.Context,
	input *store.PluginSettingsReplacement,
	command *store.CommandIdempotency,
) (*store.PluginSettingsReplacementResult, error) {
	if err := validatePluginSettingsReplacement(input, command); err != nil {
		return nil, err
	}

	mutation, err := runIdempotentMutation(
		ctx,
		s.SQLStore,
		"plugin settings replacement",
		idempotentMutation[pluginSettingsReplacementOutcome]{
			command:      command,
			auditEventID: input.AuditEventID,
			execute: func(ctx context.Context, tx *sqlxTxWrapper) (pluginSettingsReplacementOutcome, error) {
				return replacePluginSettings(ctx, tx, input)
			},
			encode: func(outcome pluginSettingsReplacementOutcome) ([]byte, error) {
				return encodeOutcome(pluginSettingsReplacementDocument{
					Settings: pluginSettingsDocument(*outcome.Settings),
					Changed:  outcome.Changed,
				})
			},
			decode: decodePluginSettingsReplacementOutcome,
			completeReplay: func(
				ctx context.Context,
				tx *sqlxTxWrapper,
				outcome pluginSettingsReplacementOutcome,
				originalAuditID string,
			) error {
				return completePluginSettingsAudit(ctx, tx, input, outcome, true, originalAuditID)
			},
		},
	)
	if err != nil {
		return nil, err
	}

	return &store.PluginSettingsReplacementResult{
		Settings: mutation.Value.Settings,
		Changed:  mutation.Value.Changed,
		Replayed: mutation.Replayed,
	}, nil
}

// replacePluginSettings locks the plugin's row, requires an active System
// Administrator and the expected revision, saves the change at PostgreSQL
// time, and completes the audit.
func replacePluginSettings(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.PluginSettingsReplacement,
) (pluginSettingsReplacementOutcome, error) {
	if err := lockSystemAdministratorAuthenticationPaths(ctx, tx); err != nil {
		return pluginSettingsReplacementOutcome{}, err
	}

	current, saved, err := getPluginSettingsForUpdate(ctx, tx, input.PluginID)
	if err != nil {
		return pluginSettingsReplacementOutcome{}, err
	}

	var databaseNow time.Time
	if err = tx.Get(ctx, &databaseNow, `SELECT clock_timestamp()`); err != nil {
		return pluginSettingsReplacementOutcome{}, fmt.Errorf("read plugin settings mutation time: %w", err)
	}

	administrator, err := isActiveSystemAdministrator(ctx, tx, input.ActorID.String(), databaseNow)
	if err != nil {
		return pluginSettingsReplacementOutcome{}, err
	}
	if !administrator {
		return pluginSettingsReplacementOutcome{}, store.NewErrConflict("plugin_settings", "actor_not_system_administrator", nil)
	}

	var currentRevision int64
	if saved {
		currentRevision = current.Revision
	}
	if currentRevision != input.ExpectedRevision {
		return pluginSettingsReplacementOutcome{}, &store.ErrPluginSettingsRevisionConflict{CurrentRevision: currentRevision}
	}

	var outcome pluginSettingsReplacementOutcome
	if !saved {
		created, createErr := model.NewPluginSettings(input.PluginID, input.Change, databaseNow)
		if createErr != nil {
			return pluginSettingsReplacementOutcome{}, store.NewErrInvalidInput("plugin_settings", "replacement", nil).Wrap(createErr)
		}
		if err = insertPluginSettings(ctx, tx, created); err != nil {
			return pluginSettingsReplacementOutcome{}, err
		}
		outcome = pluginSettingsReplacementOutcome{Settings: created, Changed: true}
	} else {
		candidate := current.Clone()
		at := transitionInstant(databaseNow, current.CreatedAt, current.UpdatedAt)
		if err = candidate.Replace(input.ExpectedRevision, input.Change, at); err != nil {
			return pluginSettingsReplacementOutcome{}, store.NewErrInvalidInput("plugin_settings", "replacement", nil).Wrap(err)
		}
		changed := candidate.Revision != current.Revision
		if changed {
			if err = updatePluginSettings(ctx, tx, candidate, current.Revision); err != nil {
				return pluginSettingsReplacementOutcome{}, err
			}
		}
		outcome = pluginSettingsReplacementOutcome{Settings: candidate, Changed: changed}
	}

	if err = completePluginSettingsAudit(ctx, tx, input, outcome, false, ""); err != nil {
		return pluginSettingsReplacementOutcome{}, err
	}
	return outcome, nil
}

// getPluginSettingsForUpdate locks and returns a plugin's saved settings.
// saved is false when none are saved. A first save is serialized by the
// System Administrator authentication lock taken before it.
func getPluginSettingsForUpdate(
	ctx context.Context,
	tx *sqlxTxWrapper,
	id model.PluginID,
) (settings *model.PluginSettings, saved bool, err error) {
	var row pluginSettingsRow
	err = tx.Get(ctx, &row, `SELECT plugin_id, revision, enabled, settings, created_at, updated_at
		FROM plugin_settings WHERE plugin_id = ? FOR UPDATE`, id.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock plugin settings: %w", err)
	}
	settings, err = row.model()
	if err != nil {
		return nil, false, err
	}
	return settings, true, nil
}

func insertPluginSettings(ctx context.Context, tx *sqlxTxWrapper, settings *model.PluginSettings) error {
	if _, err := tx.Exec(ctx, `INSERT INTO plugin_settings (plugin_id, revision, enabled, settings, created_at, updated_at)
		VALUES (?, ?, ?, ?::jsonb, ?, ?)`,
		settings.PluginID.String(), settings.Revision, settings.Enabled, []byte(settings.Settings),
		settings.CreatedAt, settings.UpdatedAt); err != nil {
		return fmt.Errorf("insert plugin settings: %w", translateError("plugin_settings", settings.PluginID.String(), err))
	}
	return nil
}

func updatePluginSettings(ctx context.Context, tx *sqlxTxWrapper, candidate *model.PluginSettings, previousRevision int64) error {
	result, err := tx.Exec(ctx, `UPDATE plugin_settings
		   SET revision = ?, enabled = ?, settings = ?::jsonb, updated_at = ?
		 WHERE plugin_id = ? AND revision = ?`,
		candidate.Revision, candidate.Enabled, []byte(candidate.Settings), candidate.UpdatedAt,
		candidate.PluginID.String(), previousRevision)
	if err != nil {
		return fmt.Errorf("replace plugin settings: %w", translateError("plugin_settings", candidate.PluginID.String(), err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read plugin settings affected rows: %w", err)
	}
	if affected != 1 {
		return store.NewErrConflict("plugin_settings", "plugin_settings_changed", nil)
	}
	return nil
}

func (r pluginSettingsRow) model() (*model.PluginSettings, error) {
	// PostgreSQL re-spells jsonb on read; normalize it back.
	normalized, err := model.NormalizePluginSettingsDocument(json.RawMessage(r.Settings))
	if err != nil {
		return nil, invalidPersistedState("plugin_settings", "settings", err)
	}
	settings := &model.PluginSettings{
		PluginID:  model.PluginID(r.PluginID),
		Revision:  r.Revision,
		Enabled:   r.Enabled,
		Settings:  normalized,
		CreatedAt: model.TimeUTC(r.CreatedAt),
		UpdatedAt: model.TimeUTC(r.UpdatedAt),
	}
	if err = settings.Validate(); err != nil {
		return nil, invalidPersistedState("plugin_settings", "value", err)
	}
	return settings, nil
}

func validatePluginSettingsReplacement(input *store.PluginSettingsReplacement, command *store.CommandIdempotency) error {
	if input == nil ||
		!input.ActorID.IsValid() ||
		!input.PluginID.IsValid() ||
		input.ExpectedRevision < 0 ||
		input.Change.Validate() != nil ||
		!input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() ||
		command == nil ||
		command.UserID != input.ActorID ||
		command.Operation != store.PluginSettingsReplaceOperation ||
		command.Retention <= 0 ||
		command.Wait <= 0 {
		return store.NewErrInvalidInput("plugin_settings", "replacement", nil)
	}
	return nil
}

func decodePluginSettingsReplacementOutcome(encoded []byte) (pluginSettingsReplacementOutcome, error) {
	var document pluginSettingsReplacementDocument
	err := decodeOutcome(encoded, &document)
	settings := model.PluginSettings(document.Settings)
	if err == nil {
		// The outcome is stored as jsonb, which re-spells the nested document.
		settings.Settings, err = model.NormalizePluginSettingsDocument(settings.Settings)
	}
	outcome := pluginSettingsReplacementOutcome{Settings: &settings, Changed: document.Changed}
	if err != nil || outcome.Settings.Validate() != nil {
		return pluginSettingsReplacementOutcome{}, invalidPersistedState(
			"command_outcome", "outcome", errors.New("invalid plugin settings outcome"))
	}
	return outcome, nil
}

func completePluginSettingsAudit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.PluginSettingsReplacement,
	outcome pluginSettingsReplacementOutcome,
	replayed bool,
	originalAuditID string,
) error {
	data := map[string]any{
		"operation":          "replace",
		"expected_revision":  input.ExpectedRevision,
		"resulting_revision": outcome.Settings.Revision,
		"changed":            outcome.Changed,
	}
	for key, value := range outcome.Settings.Auditable() {
		data[key] = value
	}
	if replayed {
		data["idempotency_replayed"] = true
		data["original_audit_event_id"] = originalAuditID
	}
	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return store.NewErrInvalidInput("plugin_settings", "audit", nil).Wrap(err)
	}
	if _, err := completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", encoded, input.AuditAt); err != nil {
		return fmt.Errorf("complete plugin settings audit: %w", err)
	}
	return nil
}

var _ store.PluginSettingsStore = (*SQLPluginSettingsStore)(nil)
