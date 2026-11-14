// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/plugin"
)

// RecordStore keeps every plugin's records. The host names the plugin on
// each call; the store keeps one plugin's records apart from another's and
// answers with the plugin package's errors.
type RecordStore interface {
	Get(ctx context.Context, pluginID, kind, key string) (plugin.Record, error)
	Put(ctx context.Context, pluginID, kind, key string, document json.RawMessage, expectedRevision int64) (int64, error)
	Delete(ctx context.Context, pluginID, kind, key string, expectedRevision int64) error
	List(ctx context.Context, pluginID, kind, prefix, after string, limit int) ([]plugin.Record, error)
}

// pluginRecords is the plugin.Records one activation of a plugin receives:
// the store scoped to its plugin, usable until the activation ends.
type pluginRecords struct {
	store      RecordStore
	activation *activation
}

var _ plugin.Records = pluginRecords{}

func (r pluginRecords) available() error {
	if r.store == nil || !r.activation.usable() {
		return fmt.Errorf("plugin %q records: %w", r.activation.entry.manifest.ID, plugin.ErrUnavailable)
	}
	return nil
}

func (r pluginRecords) Get(ctx context.Context, kind, key string) (plugin.Record, error) {
	if err := r.available(); err != nil {
		return plugin.Record{}, err
	}
	return r.store.Get(ctx, r.activation.entry.manifest.ID, kind, key)
}

func (r pluginRecords) Put(ctx context.Context, kind, key string, document json.RawMessage, expectedRevision int64) (int64, error) {
	if err := r.available(); err != nil {
		return 0, err
	}
	return r.store.Put(ctx, r.activation.entry.manifest.ID, kind, key, slices.Clone(document), expectedRevision)
}

func (r pluginRecords) Delete(ctx context.Context, kind, key string, expectedRevision int64) error {
	if err := r.available(); err != nil {
		return err
	}
	return r.store.Delete(ctx, r.activation.entry.manifest.ID, kind, key, expectedRevision)
}

func (r pluginRecords) List(ctx context.Context, kind, prefix, after string, limit int) ([]plugin.Record, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	return r.store.List(ctx, r.activation.entry.manifest.ID, kind, prefix, after, limit)
}
