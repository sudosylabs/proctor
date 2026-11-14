// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/store"
)

func TestPluginBoundsMatchTheStore(t *testing.T) {
	t.Parallel()

	require.Equal(t, store.PluginRecordDocumentMaxBytes, plugin.MaxRecordDocumentBytes)
	require.Equal(t, store.PluginRecordListMaxLimit, plugin.MaxRecordListLimit)
	require.Equal(t, store.PluginLeaseClaimMaxKeys, plugin.MaxLeaseClaimKeys)
}

func TestPluginStoreErrorsReachPluginsAsPluginErrors(t *testing.T) {
	t.Parallel()

	transient := errors.New("connection reset")
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"missing record", store.NewErrNotFound("plugin_record", "k"), plugin.ErrRecordNotFound},
		{"revision conflict", &store.ErrPluginRecordRevisionConflict{CurrentRevision: 2}, plugin.ErrRevisionConflict},
		{"oversized document", store.ErrPluginRecordTooLarge, plugin.ErrRecordTooLarge},
		{"invalid input", store.NewErrInvalidInput("plugin_record", "key", nil), plugin.ErrInvalidArgument},
		{"other failure", transient, transient},
	} {
		require.ErrorIs(t, pluginRecordError(test.err), test.want, test.name)
	}
	require.NoError(t, pluginRecordError(nil))

	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"held", store.ErrPluginLeaseHeld, plugin.ErrLeaseHeld},
		{"lost", store.ErrPluginLeaseLost, plugin.ErrLeaseLost},
		{"invalid input", store.NewErrInvalidInput("plugin_lease", "claim", nil), plugin.ErrInvalidArgument},
		{"other failure", transient, transient},
	} {
		require.ErrorIs(t, pluginLeaseError(test.err), test.want, test.name)
	}
	require.NoError(t, pluginLeaseError(nil))
}
