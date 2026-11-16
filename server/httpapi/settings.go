// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/websocket"
)

// BuildInfo is the build identity GET /system/version returns.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// Settings holds what composition supplies beside the application: logging,
// observation and localization ports, node health, build information, and
// the server's request limits. New validates it before compiling any route.
type Settings struct {
	Logger    *logging.Logger
	Metrics   route.Metrics
	Tracing   route.Tracing
	Localizer route.Localizer
	Health    *app.Health
	BuildInfo BuildInfo
	// Plugins reports the active plugins GET /system/plugins lists and
	// whether readiness is degraded by a failed plugin.
	Plugins   Plugins
	PublicURL string
	// DocumentationURL is the installation's documentation site, which Problem
	// Details link to. Empty sends type "about:blank".
	DocumentationURL string
	MaxBodyBytes     int64
	// ReadTimeout and WriteTimeout repeat the server-wide connection
	// deadlines. Streaming upload and binary download routes extend them by
	// the time their declared size takes at the minimum admitted throughput.
	// Zero leaves the corresponding deadline unchanged.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// TrustedProxies lists reverse-proxy peers whose X-Forwarded-For and
	// X-Request-ID headers are accepted. Empty trusts no peer.
	TrustedProxies          []netip.Prefix
	RecentAuthenticationTTL time.Duration
	NodeID                  string
	// WebSocket is the sibling transport constructed at composition root.
	// HTTP owns only route mounting and session middleware around Accept.
	WebSocket WebSocketTransport
	// BinarySockets upgrades a request to a binary socket, such as a
	// terminal's. Nil refuses every binary socket as unavailable.
	BinarySockets *websocket.Hub
}

// BinarySocket is one upgraded binary WebSocket. Read returns the next
// binary message; Write sends one. A failure of either ends the socket.
type BinarySocket interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, message []byte) error
}

// WebSocketTransport is the narrow mount surface HTTP needs from the sibling
// websocket package. Composition supplies the concrete hub. Accept owns the
// response after invocation; a returned error must describe a pre-upgrade
// failure and must not follow a committed response.
type WebSocketTransport interface {
	Accept(
		writer http.ResponseWriter,
		request *http.Request,
		principal model.Principal,
		metadata model.RequestMetadata,
		connectionID string,
		sequence int64,
		allowMissingOrigin bool,
	) error
}
