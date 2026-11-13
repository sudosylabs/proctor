// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

// recordingHost keeps what a plugin registers.
type recordingHost struct {
	types []plugin.IdentityProviderType
}

func (*recordingHost) Logger() *slog.Logger      { return slog.New(slog.DiscardHandler) }
func (*recordingHost) Settings() json.RawMessage { return nil }
func (*recordingHost) Secrets() json.RawMessage  { return nil }
func (h *recordingHost) AddIdentityProviderType(providerType plugin.IdentityProviderType) error {
	h.types = append(h.types, providerType)
	return nil
}

func (*recordingHost) AddExportFormat(plugin.ExportFormat) error {
	return errors.New("not offered")
}

func (*recordingHost) AddEventHandler(plugin.EventTopic, plugin.EventHandler) error {
	return errors.New("not offered")
}

func TestPluginOffersTheProviderTypeThatBuildsConfiguredProviders(t *testing.T) {
	t.Parallel()

	require.Equal(t, plugin.Manifest{ID: "oidc", Name: "OpenID Connect", EnabledByDefault: true}, NewPlugin().Manifest())
	require.NoError(t, NewPlugin().Manifest().Validate())

	host := &recordingHost{}
	require.NoError(t, NewPlugin().Activate(context.Background(), host))
	require.Len(t, host.types, 1)
	require.Equal(t, "oidc", host.types[0].Type())

	built, err := host.types[0].New(testSettings("https://oidc.example.test").definition())
	require.NoError(t, err)
	require.IsType(t, &Provider{}, built)
	require.NoError(t, NewPlugin().Deactivate(context.Background()))
}
