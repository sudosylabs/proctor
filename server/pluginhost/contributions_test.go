// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// offeringPlugin registers what its activate function asks for.
type offeringPlugin struct {
	id       string
	activate func(plugin.Host) error
}

func (p offeringPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: "Offering " + p.id, EnabledByDefault: true}
}

func (p offeringPlugin) Activate(_ context.Context, host plugin.Host) error { return p.activate(host) }
func (offeringPlugin) Deactivate(context.Context) error                     { return nil }

type fakeFormat struct {
	mediaType string
	extension string
	panics    bool
}

func (f fakeFormat) MediaType() string     { return f.mediaType }
func (f fakeFormat) FileExtension() string { return f.extension }

func (f fakeFormat) Write(_ context.Context, archive plugin.ExportArchive, w io.Writer) error {
	if f.panics {
		panic("write exploded")
	}
	_, err := io.WriteString(w, archive.ExportID)
	return err
}

func TestExportFormatsAreLookedUpByMediaTypeAndGuarded(t *testing.T) {
	t.Parallel()

	host := activateIdentityPlugins(t,
		offeringPlugin{id: "zip", activate: func(h plugin.Host) error {
			return h.AddExportFormat(fakeFormat{mediaType: "application/zip", extension: ".zip"})
		}},
		offeringPlugin{id: "copycat", activate: func(h plugin.Host) error {
			return h.AddExportFormat(fakeFormat{mediaType: "application/zip", extension: ".zip"})
		}},
		offeringPlugin{id: "broken", activate: func(h plugin.Host) error {
			return h.AddExportFormat(fakeFormat{mediaType: "application/x-tar", extension: ".tar", panics: true})
		}},
		offeringPlugin{id: "nameless", activate: func(h plugin.Host) error {
			return h.AddExportFormat(fakeFormat{mediaType: "text/plain", extension: "txt"})
		}},
	)
	require.Equal(t, []pluginhost.Status{
		{ID: "zip", Name: "Offering zip", State: pluginhost.StateActive, EnabledByDefault: true},
		{ID: "copycat", Name: "Offering copycat", State: pluginhost.StateFailed, EnabledByDefault: true},
		{ID: "broken", Name: "Offering broken", State: pluginhost.StateActive, EnabledByDefault: true},
		{ID: "nameless", Name: "Offering nameless", State: pluginhost.StateFailed, EnabledByDefault: true},
	}, host.Statuses(), "a repeated media type or a bad extension fails the plugin that offers it")

	format, ok := host.ExportFormat("application/zip")
	require.True(t, ok)
	require.Equal(t, ".zip", format.FileExtension())
	var written stringWriter
	require.NoError(t, format.Write(context.Background(), plugin.ExportArchive{ExportID: "export-1"}, &written))
	require.Equal(t, "export-1", written.value)

	_, ok = host.ExportFormat("text/plain")
	require.False(t, ok, "a format from a failed plugin is not offered")

	broken, ok := host.ExportFormat("application/x-tar")
	require.True(t, ok)
	require.ErrorIs(t, broken.Write(context.Background(), plugin.ExportArchive{}, io.Discard), plugin.ErrUnavailable)
}

type stringWriter struct{ value string }

func (w *stringWriter) Write(p []byte) (int, error) {
	w.value += string(p)
	return len(p), nil
}
