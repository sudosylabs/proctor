// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/websocket"
)

const previewPortsPath = environmentPath + "/ports"
const previewSocketPath = previewPortsPath + "/{port:sequence}/socket"

type examAttemptPreviewHandlers struct {
	app     *environment.Previews
	sockets *websocket.Hub
	recheck time.Duration
}

func examAttemptPreviewResource(application *environment.Previews, sockets *websocket.Hub) route.Resource {
	h := examAttemptPreviewHandlers{app: application, sockets: sockets, recheck: terminalRecheck}
	return route.NewResource("exam_attempt_previews",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   previewPortsPath,
			Auth:   route.AuthSessionRequired,
			Errors: withCandidateAccessErrors("exam.attempt.environment_not_found", "exam.attempt.environment_not_running",
				"exam.attempt.environment_unavailable"),
			Handle: h.list,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         previewSocketPath,
			Auth:         route.AuthSessionRequired,
			ProtocolName: "exam-attempt-preview-socket",
			ProtocolKind: route.ProtocolUpgrade,
			Errors: withCandidateAccessErrors("exam.attempt.environment_not_found", "exam.attempt.environment_not_running",
				"exam.attempt.environment_unavailable", "exam.attempt.preview_not_allowed", "exam.attempt.preview_limit_reached",
				"exam.attempt.preview_port_closed", "websocket.origin.invalid", "websocket.unavailable"),
			Upgrade: h.socket,
		},
	)
}

type previewPortResponse struct {
	Port       int                                `json:"port"`
	Address    string                             `json:"address"`
	Allowed    bool                               `json:"allowed"`
	ProcessTag model.AttemptEnvironmentProcessTag `json:"process_tag,omitempty"`
}

type previewPortsResponse struct {
	Ports           []previewPortResponse `json:"ports"`
	ConnectionLimit int                   `json:"connection_limit"`
}

func (h examAttemptPreviewHandlers) list(request route.Request) (route.Result, error) {
	access, err := terminalAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	ports, err := h.app.ListPreviewPorts(request.Context, request.Invocation(), access)
	if err != nil {
		return route.Result{}, err
	}
	response := previewPortsResponse{Ports: make([]previewPortResponse, len(ports)), ConnectionLimit: environment.MaxPreviewConnections}
	for i, port := range ports {
		response.Ports[i] = previewPortResponse{Port: port.Port, Address: port.Address, Allowed: port.Allowed, ProcessTag: port.Tag}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptPreviewHandlers) socket(writer http.ResponseWriter, request route.Request) error {
	access, err := terminalAccess(request)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(request.Params["port"])
	if err != nil || port < 1 || port > 65535 {
		return route.InvalidRequest(errors.New("port number out of bounds"))
	}
	if h.sockets == nil {
		return apperror.New("websocket.unavailable")
	}
	allowMissingOrigin := route.CredentialSourceOf(request.Context) == route.CredentialBearer
	if !h.sockets.OriginAllowed(request.HTTP.Header.Get("Origin"), allowMissingOrigin) {
		return apperror.New("websocket.origin.invalid")
	}
	conn, err := h.app.OpenPreview(request.Context, request.Invocation(), access, port)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return h.sockets.ServeBinary(writer, request.HTTP, allowMissingOrigin, maxPreviewData+1,
		func(ctx context.Context, socket *websocket.BinarySocket) websocket.BinarySocketOutcome {
			return servePreview(ctx, socket, conn, h.recheck)
		})
}
