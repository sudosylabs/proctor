// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/websocket"
)

// pluginRouteSource hands the routes active plugins serve to the HTTP API,
// which mounts them behind its guards and follows every plugin change.
type pluginRouteSource struct {
	host *pluginhost.Host
}

var _ httpapi.PluginRoutes = pluginRouteSource{}

func (s pluginRouteSource) PluginRoutes() []httpapi.PluginRoute {
	mounted := s.host.Routes()
	routes := make([]httpapi.PluginRoute, len(mounted))
	for index, route := range mounted {
		routes[index] = httpapi.PluginRoute{Plugin: route.Plugin, Route: route.Route, Available: route.Available}
	}
	return routes
}

func (s pluginRouteSource) OnPluginRoutesChange(listener func()) {
	s.host.AddChangeListener(listener)
}

// pluginSocketTransport upgrades plugin socket routes through the WebSocket
// hub, which owns every upgraded connection and closes them when it stops.
type pluginSocketTransport struct {
	hub *websocket.Hub
}

var _ httpapi.PluginSockets = pluginSocketTransport{}

func (t pluginSocketTransport) ServePluginSocket(
	writer http.ResponseWriter,
	request *http.Request,
	allowMissingOrigin bool,
	serve func(ctx context.Context, socket plugin.Socket) httpapi.PluginSocketOutcome,
) error {
	return t.hub.ServeBinary(writer, request, allowMissingOrigin, plugin.MaxSocketMessageBytes,
		func(ctx context.Context, socket *websocket.BinarySocket) websocket.BinarySocketOutcome {
			switch serve(ctx, socket) {
			case httpapi.PluginSocketAccessEnded:
				return websocket.BinarySocketAccessEnded
			case httpapi.PluginSocketFailed:
				return websocket.BinarySocketFailed
			default:
				return websocket.BinarySocketDone
			}
		})
}
