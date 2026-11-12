// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package zipexport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

func TestZipExportFormatDescribesItsBytes(t *testing.T) {
	require.Equal(t, "application/zip", zipExportFormat{}.MediaType())
	require.Equal(t, ".zip", zipExportFormat{}.FileExtension())
}

func TestPluginOffersTheZipFormat(t *testing.T) {
	t.Parallel()

	manifest := NewPlugin().Manifest()
	require.Equal(t, plugin.Manifest{ID: "export.zip", Name: "Zip export", EnabledByDefault: true, Required: true}, manifest)
	require.NoError(t, manifest.Validate())

	host := &formatRecordingHost{}
	require.NoError(t, NewPlugin().Activate(context.Background(), host))
	require.Equal(t, []plugin.ExportFormat{zipExportFormat{}}, host.formats)
	require.Equal(t, MediaType, host.formats[0].MediaType())
	require.NoError(t, NewPlugin().Deactivate(context.Background()))
}

// formatRecordingHost keeps the export formats a plugin offers.
type formatRecordingHost struct {
	formats []plugin.ExportFormat
}

func (*formatRecordingHost) Logger() *slog.Logger      { return slog.New(slog.DiscardHandler) }
func (*formatRecordingHost) Settings() json.RawMessage { return nil }
func (*formatRecordingHost) AddIdentityProviderType(plugin.IdentityProviderType) error {
	return errors.New("not offered")
}
func (h *formatRecordingHost) AddExportFormat(format plugin.ExportFormat) error {
	h.formats = append(h.formats, format)
	return nil
}
func (*formatRecordingHost) AddEventHandler(plugin.EventTopic, plugin.EventHandler) error {
	return errors.New("not offered")
}
