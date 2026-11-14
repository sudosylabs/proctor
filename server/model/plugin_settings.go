// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// PluginSettingsMaxBytes bounds one plugin's settings document.
const PluginSettingsMaxBytes = 64 << 10

var (
	// ErrPluginSettingsRevisionConflict means the settings changed since the
	// revision the caller expected.
	ErrPluginSettingsRevisionConflict = errors.New("plugin settings revision conflict")
	errPluginSettingsInvalid          = errors.New("plugin settings are invalid")
)

// PluginID identifies a plugin compiled into the server: 1 to 64 lowercase
// letters, digits, dots or hyphens, starting with a letter.
type PluginID string

// IsValid reports whether id is a well-formed plugin ID.
func (id PluginID) IsValid() bool {
	if id == "" || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func (id PluginID) String() string { return string(id) }

// PluginSettings is what an administrator chose for one plugin: whether it
// runs and its settings document. A plugin without saved settings uses its
// built-in defaults. Secrets are never part of it; they stay in the
// installation's configuration file.
type PluginSettings struct {
	PluginID PluginID
	Revision int64
	Enabled  bool
	// Settings is the plugin's own JSON object in normalized form: keys
	// sorted, insignificant whitespace removed, numbers as written.
	Settings  json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PluginSettingsChange is the complete replaceable part of PluginSettings.
type PluginSettingsChange struct {
	Enabled  bool
	Settings json.RawMessage
}

// NormalizePluginSettingsDocument returns document in the normalized form
// PluginSettings stores, or an error when it is not a JSON object within
// PluginSettingsMaxBytes. Normalizing makes equal documents compare and
// fingerprint equal however they were written.
func NormalizePluginSettingsDocument(document json.RawMessage) (json.RawMessage, error) {
	if len(document) == 0 || len(document) > PluginSettingsMaxBytes {
		return nil, errPluginSettingsInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errPluginSettingsInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errPluginSettingsInvalid
	}
	normalized, err := json.Marshal(object)
	if err != nil || len(normalized) > PluginSettingsMaxBytes {
		return nil, errPluginSettingsInvalid
	}
	return normalized, nil
}

// Validate checks a change, which must carry a normalized settings document.
func (c PluginSettingsChange) Validate() error {
	normalized, err := NormalizePluginSettingsDocument(c.Settings)
	if err != nil || !bytes.Equal(normalized, c.Settings) {
		return errPluginSettingsInvalid
	}
	return nil
}

// NewPluginSettings returns the first saved settings of a plugin, at
// revision 1.
func NewPluginSettings(id PluginID, change PluginSettingsChange, at time.Time) (*PluginSettings, error) {
	if !id.IsValid() || change.Validate() != nil || at.IsZero() {
		return nil, errPluginSettingsInvalid
	}
	at = TimeUTC(at)
	settings := &PluginSettings{
		PluginID:  id,
		Revision:  1,
		Enabled:   change.Enabled,
		Settings:  bytes.Clone(change.Settings),
		CreatedAt: at,
		UpdatedAt: at,
	}
	return settings, settings.Validate()
}

// Validate checks persisted settings without normalizing malformed data.
func (s *PluginSettings) Validate() error {
	if s == nil ||
		!s.PluginID.IsValid() ||
		s.Revision < 1 ||
		s.CreatedAt.IsZero() ||
		s.UpdatedAt.IsZero() ||
		s.UpdatedAt.Before(s.CreatedAt) {
		return errPluginSettingsInvalid
	}
	return s.Change().Validate()
}

// Change returns a defensive copy of the replaceable part.
func (s *PluginSettings) Change() PluginSettingsChange {
	if s == nil {
		return PluginSettingsChange{}
	}
	return PluginSettingsChange{Enabled: s.Enabled, Settings: bytes.Clone(s.Settings)}
}

// Replace applies a complete optimistic replacement. An exact no-op keeps
// the current revision.
func (s *PluginSettings) Replace(expectedRevision int64, change PluginSettingsChange, at time.Time) error {
	if s == nil || s.Validate() != nil || change.Validate() != nil || at.IsZero() {
		return errPluginSettingsInvalid
	}
	if expectedRevision != s.Revision {
		return ErrPluginSettingsRevisionConflict
	}
	at = TimeUTC(at)
	if at.Before(s.UpdatedAt) {
		return errPluginSettingsInvalid
	}
	if s.Enabled == change.Enabled && bytes.Equal(s.Settings, change.Settings) {
		return nil
	}
	s.Enabled = change.Enabled
	s.Settings = bytes.Clone(change.Settings)
	s.Revision++
	s.UpdatedAt = at
	return s.Validate()
}

// Clone returns a defensive copy.
func (s *PluginSettings) Clone() *PluginSettings {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Settings = bytes.Clone(s.Settings)
	return &clone
}

// Auditable returns bounded facts about the settings, never their values,
// which may describe an institution's security posture.
func (s *PluginSettings) Auditable() map[string]any {
	if s == nil {
		return map[string]any{}
	}
	return map[string]any{
		"plugin_id":      s.PluginID.String(),
		"revision":       s.Revision,
		"enabled":        s.Enabled,
		"settings_bytes": len(s.Settings),
	}
}
