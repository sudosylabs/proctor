// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

func TestRouteValidateAcceptsOnlyBoundedGuardedRoutes(t *testing.T) {
	t.Parallel()

	handler := func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
		return plugin.Response{}, nil
	}
	socket := func(context.Context, plugin.Access, plugin.Socket) error { return nil }
	valid := plugin.Route{Method: "POST", Path: "terminal/resize", Guard: plugin.GuardCandidateAttemptAccess, Handler: handler}
	require.NoError(t, valid.Validate())
	require.NoError(t, plugin.Route{Method: "GET", Path: "terminal", Guard: plugin.GuardCandidateAttemptAccess, Socket: socket}.Validate())

	for name, change := range map[string]func(*plugin.Route){
		"unknown method":        func(r *plugin.Route) { r.Method = "TRACE" },
		"lowercase method":      func(r *plugin.Route) { r.Method = "post" },
		"empty path":            func(r *plugin.Route) { r.Path = "" },
		"absolute path":         func(r *plugin.Route) { r.Path = "/terminal" },
		"trailing slash":        func(r *plugin.Route) { r.Path = "terminal/" },
		"parameter":             func(r *plugin.Route) { r.Path = "terminal/{id}" },
		"uppercase path":        func(r *plugin.Route) { r.Path = "Terminal" },
		"unknown guard":         func(r *plugin.Route) { r.Guard = "manager" },
		"no guard":              func(r *plugin.Route) { r.Guard = "" },
		"negative body":         func(r *plugin.Route) { r.MaxBodyBytes = -1 },
		"oversized body":        func(r *plugin.Route) { r.MaxBodyBytes = plugin.MaxRouteBodyBytes + 1 },
		"no handler":            func(r *plugin.Route) { r.Handler = nil },
		"handler and socket":    func(r *plugin.Route) { r.Socket = socket },
		"socket on POST":        func(r *plugin.Route) { r.Handler, r.Socket = nil, socket },
		"socket with a body":    func(r *plugin.Route) { r.Method, r.Handler, r.Socket, r.MaxBodyBytes = "GET", nil, socket, 1 },
		"double hyphen in path": func(r *plugin.Route) { r.Path = "terminal--resize" },
	} {
		route := valid
		change(&route)
		require.ErrorIs(t, route.Validate(), plugin.ErrInvalidRoute, name)
	}
}

func TestOnlySingleSegmentPluginIDsServeRoutes(t *testing.T) {
	t.Parallel()

	require.True(t, plugin.IsRoutableID("execution"))
	require.True(t, plugin.IsRoutableID("screen-recorder2"))
	require.False(t, plugin.IsRoutableID("export.zip"))
	require.False(t, plugin.IsRoutableID("native"))
	require.False(t, plugin.IsRoutableID("Execution"))
	require.False(t, plugin.IsRoutableID(""))
}

func TestGuardListIsClosed(t *testing.T) {
	t.Parallel()

	require.True(t, plugin.GuardCandidateAttemptAccess.Valid())
	require.Equal(t, plugin.GuardCandidateAttemptAccess, plugin.CandidateAttemptAccess{}.Guard())
	require.False(t, plugin.Guard("candidate_attempt_access ").Valid())
}
