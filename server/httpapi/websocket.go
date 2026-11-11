// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"strconv"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type webSocketHandlers struct {
	transport WebSocketTransport
}

func webSocketResource(transport WebSocketTransport) route.Resource {
	h := webSocketHandlers{transport: transport}
	return route.NewResource(route.WebSocketResourceName,
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/" + route.WebSocketPath,
			Auth:         route.AuthSessionRequired,
			ProtocolName: route.WebSocketProtocolName,
			ProtocolKind: route.ProtocolUpgrade,
			Errors: []string{
				"request.invalid",
				"websocket.origin.invalid",
				"websocket.unavailable",
			},
			Upgrade: h.connect,
		},
	)
}

func (h webSocketHandlers) connect(writer http.ResponseWriter, request route.Request) error {
	connectionID, sequence, err := parseWebSocketResume(request.QueryValue("connection_id"), request.QueryValue("sequence_number"))
	if err != nil {
		return err
	}
	allowMissingOrigin := route.CredentialSourceOf(request.Context) == route.CredentialBearer
	return h.transport.Accept(
		writer,
		request.HTTP,
		request.Principal,
		request.Metadata,
		connectionID,
		sequence,
		allowMissingOrigin,
	)
}

func parseWebSocketResume(connectionID, sequenceNumber string) (string, int64, error) {
	if connectionID == "" && sequenceNumber == "" {
		return "", 0, nil
	}
	if !model.IsValidOpaqueID(connectionID) || sequenceNumber == "" {
		return "", 0, route.InvalidRequest(nil)
	}
	sequence, err := strconv.ParseInt(sequenceNumber, 10, 64)
	if err != nil || sequence < 0 {
		return "", 0, route.InvalidRequest(err)
	}
	return connectionID, sequence, nil
}
