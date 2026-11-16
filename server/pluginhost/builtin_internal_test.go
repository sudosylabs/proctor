// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

// idPlugin is a plugin known only by its ID.
type idPlugin struct{ id string }

func (p idPlugin) Manifest() plugin.Manifest                 { return plugin.Manifest{ID: p.id, Name: p.id} }
func (idPlugin) Activate(context.Context, plugin.Host) error { return nil }
func (idPlugin) Deactivate(context.Context) error            { return nil }

func TestTestPluginsReplaceTheBuiltInWithTheirID(t *testing.T) {
	builtin := []plugin.Plugin{idPlugin{id: "cas"}, idPlugin{id: "export.zip"}}
	replacement, added := idPlugin{id: "export.zip"}, idPlugin{id: "display"}
	require.Equal(t, []plugin.Plugin{idPlugin{id: "cas"}, replacement, added},
		withTestPlugins(builtin, []plugin.Plugin{added, replacement}))

	ids := pluginIDSet(builtinPlugins(), func(p plugin.Plugin) string { return p.Manifest().ID })
	require.Equal(t, map[string]struct{}{"cas": {}, "oidc": {}, "export.zip": {}}, ids,
		"only edge adapters are plugins; execution is core")
}
