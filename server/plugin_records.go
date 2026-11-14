// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// storePluginRecords adapts the plugin record Store to the plugin host,
// which may not import store. The host names the plugin on every call; this
// adapter only translates, and answers with the plugin package's errors so
// that no store type reaches a plugin.
type storePluginRecords struct {
	store store.PluginRecordStore
}

var _ pluginhost.RecordStore = storePluginRecords{}

// newStorePluginRecords returns the adapter, or nil without persistence so
// the host reports records unavailable.
func newStorePluginRecords(records store.PluginRecordStore) pluginhost.RecordStore {
	if records == nil {
		return nil
	}
	return storePluginRecords{store: records}
}

func (r storePluginRecords) Get(ctx context.Context, pluginID, kind, key string) (plugin.Record, error) {
	record, err := r.store.Get(ctx, model.PluginID(pluginID), kind, key)
	if err != nil {
		return plugin.Record{}, pluginRecordError(err)
	}
	return pluginRecord(record), nil
}

func (r storePluginRecords) Put(
	ctx context.Context,
	pluginID, kind, key string,
	document json.RawMessage,
	expectedRevision int64,
) (int64, error) {
	record, err := r.store.Put(ctx, &store.PluginRecordPut{
		PluginID: model.PluginID(pluginID), Kind: kind, Key: key, Document: document, ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return 0, pluginRecordError(err)
	}
	return record.Revision, nil
}

func (r storePluginRecords) Delete(ctx context.Context, pluginID, kind, key string, expectedRevision int64) error {
	err := r.store.Delete(ctx, &store.PluginRecordDeletion{
		PluginID: model.PluginID(pluginID), Kind: kind, Key: key, ExpectedRevision: expectedRevision,
	})
	return pluginRecordError(err)
}

func (r storePluginRecords) List(ctx context.Context, pluginID, kind, prefix, after string, limit int) ([]plugin.Record, error) {
	records, err := r.store.List(ctx, store.PluginRecordListOptions{
		PluginID: model.PluginID(pluginID), Kind: kind, Prefix: prefix, After: after, Limit: limit,
	})
	if err != nil {
		return nil, pluginRecordError(err)
	}
	page := make([]plugin.Record, 0, len(records))
	for _, record := range records {
		page = append(page, pluginRecord(record))
	}
	return page, nil
}

func pluginRecord(record *store.PluginRecord) plugin.Record {
	return plugin.Record{Key: record.Key, Document: record.Document, Revision: record.Revision}
}

// pluginRecordError turns a Store error into the plugin package's.
func pluginRecordError(err error) error {
	var conflict *store.ErrPluginRecordRevisionConflict
	var invalid *store.ErrInvalidInput
	switch {
	case err == nil:
		return nil
	case store.IsNotFound(err):
		return plugin.ErrRecordNotFound
	case errors.As(err, &conflict):
		return plugin.ErrRevisionConflict
	case errors.Is(err, store.ErrPluginRecordTooLarge):
		return plugin.ErrRecordTooLarge
	case errors.As(err, &invalid):
		return plugin.ErrInvalidArgument
	default:
		return fmt.Errorf("plugin records: %w", err)
	}
}
