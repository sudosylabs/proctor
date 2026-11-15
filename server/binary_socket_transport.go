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
	"github.com/sudosylabs/proctor/server/websocket"
)

// binarySocketTransport serves the HTTP API's binary sockets, such as a
// terminal's, on the WebSocket hub.
type binarySocketTransport struct{ hub *websocket.Hub }

var _ httpapi.BinarySocketTransport = binarySocketTransport{}

func (t binarySocketTransport) ServeBinarySocket(
	writer http.ResponseWriter,
	request *http.Request,
	allowMissingOrigin bool,
	maxMessageBytes int64,
	serve func(ctx context.Context, socket httpapi.BinarySocket) httpapi.BinarySocketOutcome,
) error {
	return t.hub.ServeBinary(writer, request, allowMissingOrigin, maxMessageBytes,
		func(ctx context.Context, socket *websocket.BinarySocket) websocket.BinarySocketOutcome {
			switch serve(ctx, socket) {
			case httpapi.BinarySocketAccessEnded:
				return websocket.BinarySocketAccessEnded
			case httpapi.BinarySocketFailed:
				return websocket.BinarySocketFailed
			default:
				return websocket.BinarySocketDone
			}
		})
}
