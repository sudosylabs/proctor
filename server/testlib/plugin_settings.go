// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package testlib

import (
	"context"
	"errors"
	"sync"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SavedPluginSettings is an in-memory store.PluginSettingsStore holding the
// settings a test saved. Set replaces a plugin's row and raises the change
// count, as an administrator's save would.
type SavedPluginSettings struct {
	mu    sync.Mutex
	rows  map[model.PluginID]*model.PluginSettings
	count int64
}

// NewSavedPluginSettings returns a store holding rows.
func NewSavedPluginSettings(rows ...*model.PluginSettings) *SavedPluginSettings {
	saved := &SavedPluginSettings{rows: make(map[model.PluginID]*model.PluginSettings)}
	for _, row := range rows {
		saved.Set(row)
	}
	return saved
}

// Set saves row, replacing the plugin's earlier settings.
func (s *SavedPluginSettings) Set(row *model.PluginSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[row.PluginID] = row.Clone()
	s.count++
}

// List returns the saved settings ordered by plugin ID.
func (s *SavedPluginSettings) List(context.Context) ([]*model.PluginSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]*model.PluginSettings, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row.Clone())
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].PluginID < rows[j-1].PluginID; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	return rows, nil
}

// ChangeCount reports how many times Set ran.
func (s *SavedPluginSettings) ChangeCount(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, nil
}

// Replace is not supported; tests save with Set.
func (s *SavedPluginSettings) Replace(
	context.Context,
	*store.PluginSettingsReplacement,
	*store.CommandIdempotency,
) (*store.PluginSettingsReplacementResult, error) {
	return nil, errors.New("SavedPluginSettings saves only through Set")
}

var _ store.PluginSettingsStore = (*SavedPluginSettings)(nil)
