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
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLPluginRecordStore implements store.PluginRecordStore on plugin_records.
type SQLPluginRecordStore struct{ *SQLStore }

type pluginRecordRow struct {
	PluginID  string    `db:"plugin_id"`
	Kind      string    `db:"kind"`
	Key       string    `db:"key"`
	Revision  int64     `db:"revision"`
	Document  jsonValue `db:"document"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

const pluginRecordColumns = `plugin_id, kind, key, revision, document, created_at, updated_at`

func newSQLPluginRecordStore(sqlStore *SQLStore) store.PluginRecordStore {
	return &SQLPluginRecordStore{SQLStore: sqlStore}
}

// Get returns one record, or store.ErrNotFound.
func (s SQLPluginRecordStore) Get(ctx context.Context, pluginID model.PluginID, kind, key string) (*store.PluginRecord, error) {
	if !pluginID.IsValid() || !store.ValidPluginRecordKind(kind) || !store.ValidPluginRecordKey(key) {
		return nil, store.NewErrInvalidInput("plugin_record", "get", nil)
	}
	var row pluginRecordRow
	if err := s.GetMaster().Get(ctx, &row, `SELECT `+pluginRecordColumns+` FROM plugin_records
		WHERE plugin_id = ? AND kind = ? AND key = ?`, pluginID.String(), kind, key); err != nil {
		return nil, translateError("plugin_record", key, err)
	}
	return row.model()
}

// Put creates a record when 0 is expected, or replaces one at the expected
// revision, in one statement.
func (s SQLPluginRecordStore) Put(ctx context.Context, put *store.PluginRecordPut) (*store.PluginRecord, error) {
	if put == nil || !put.PluginID.IsValid() || !store.ValidPluginRecordKind(put.Kind) ||
		!store.ValidPluginRecordKey(put.Key) || put.ExpectedRevision < 0 {
		return nil, store.NewErrInvalidInput("plugin_record", "put", nil)
	}
	document, err := store.CompactPluginRecordDocument(put.Document)
	if err != nil {
		return nil, err
	}

	var row pluginRecordRow
	if put.ExpectedRevision == 0 {
		err = s.GetMaster().Get(ctx, &row, `
			INSERT INTO plugin_records (plugin_id, kind, key, revision, document, created_at, updated_at)
			SELECT ?, ?, ?, 1, ?::jsonb, now.at, now.at FROM (SELECT clock_timestamp() AS at) AS now
			ON CONFLICT (plugin_id, kind, key) DO NOTHING
			RETURNING `+pluginRecordColumns,
			put.PluginID.String(), put.Kind, put.Key, string(document))
	} else {
		err = s.GetMaster().Get(ctx, &row, `
			UPDATE plugin_records
			SET document = ?::jsonb, revision = revision + 1, updated_at = GREATEST(updated_at, clock_timestamp())
			WHERE plugin_id = ? AND kind = ? AND key = ? AND revision = ?
			RETURNING `+pluginRecordColumns,
			string(document), put.PluginID.String(), put.Kind, put.Key, put.ExpectedRevision)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, s.revisionConflict(ctx, put.PluginID, put.Kind, put.Key)
	}
	if err := translateError("plugin_record", put.Key, err); err != nil {
		// Everything but the document's printed size was checked above.
		if store.IsConstraintViolation(err) {
			return nil, store.ErrPluginRecordTooLarge
		}
		return nil, fmt.Errorf("put plugin record: %w", err)
	}
	return row.model()
}

// Delete removes a record at the expected revision.
func (s SQLPluginRecordStore) Delete(ctx context.Context, deletion *store.PluginRecordDeletion) error {
	if deletion == nil || !deletion.PluginID.IsValid() || !store.ValidPluginRecordKind(deletion.Kind) ||
		!store.ValidPluginRecordKey(deletion.Key) || deletion.ExpectedRevision < 1 {
		return store.NewErrInvalidInput("plugin_record", "delete", nil)
	}
	result, err := s.GetMaster().Exec(ctx, `DELETE FROM plugin_records
		WHERE plugin_id = ? AND kind = ? AND key = ? AND revision = ?`,
		deletion.PluginID.String(), deletion.Kind, deletion.Key, deletion.ExpectedRevision)
	if err != nil {
		return fmt.Errorf("delete plugin record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete plugin record: %w", err)
	}
	if affected == 0 {
		return s.revisionConflict(ctx, deletion.PluginID, deletion.Kind, deletion.Key)
	}
	return nil
}

// List returns one page of a kind's records in key order. Keys are compared
// bytewise (the column's collation is C), so a prefix bounds an index range.
func (s SQLPluginRecordStore) List(ctx context.Context, options store.PluginRecordListOptions) ([]*store.PluginRecord, error) {
	if !options.PluginID.IsValid() || !store.ValidPluginRecordKind(options.Kind) ||
		!store.ValidPluginRecordKeyPrefix(options.Prefix) ||
		(options.After != "" && !store.ValidPluginRecordKey(options.After)) ||
		options.Limit < 1 || options.Limit > store.PluginRecordListMaxLimit {
		return nil, store.NewErrInvalidInput("plugin_record", "list", nil)
	}
	var rows []pluginRecordRow
	if err := s.GetMaster().Select(ctx, &rows, `SELECT `+pluginRecordColumns+` FROM plugin_records
		WHERE plugin_id = ? AND kind = ? AND key >= ? AND starts_with(key, ?) AND key > ?
		ORDER BY key
		LIMIT ?`,
		options.PluginID.String(), options.Kind, options.Prefix, options.Prefix, options.After, options.Limit); err != nil {
		return nil, fmt.Errorf("list plugin records: %w", err)
	}
	records := make([]*store.PluginRecord, 0, len(rows))
	for _, row := range rows {
		record, err := row.model()
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// revisionConflict reports the record's revision after a refused write. The
// read is informative: a write that races it may already have moved on.
func (s SQLPluginRecordStore) revisionConflict(ctx context.Context, pluginID model.PluginID, kind, key string) error {
	var current int64
	err := s.GetMaster().Get(ctx, &current, `SELECT revision FROM plugin_records
		WHERE plugin_id = ? AND kind = ? AND key = ?`, pluginID.String(), kind, key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read plugin record revision: %w", err)
	}
	return &store.ErrPluginRecordRevisionConflict{CurrentRevision: current}
}

func (row pluginRecordRow) model() (*store.PluginRecord, error) {
	var document bytes.Buffer
	if err := json.Compact(&document, row.Document); err != nil {
		return nil, fmt.Errorf("plugin record persisted state: %w", store.ErrInvalidState)
	}
	record := &store.PluginRecord{
		PluginID:  model.PluginID(row.PluginID),
		Kind:      row.Kind,
		Key:       row.Key,
		Revision:  row.Revision,
		Document:  document.Bytes(),
		CreatedAt: model.TimeUTC(row.CreatedAt),
		UpdatedAt: model.TimeUTC(row.UpdatedAt),
	}
	if err := record.Validate(); err != nil {
		return nil, fmt.Errorf("plugin record persisted state: %w", err)
	}
	return record, nil
}

var _ store.PluginRecordStore = (*SQLPluginRecordStore)(nil)
