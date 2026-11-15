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

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

const (
	environmentPath   = "/exam-attempts/{exam_attempt_id}/environment"
	terminalsPath     = environmentPath + "/terminals"
	terminalPath      = terminalsPath + "/{terminal:sequence}"
	terminalSignal    = terminalPath + "/signal"
	terminalSocket    = terminalPath + "/socket"
	terminalOpenBytes = 1 << 10
)

// terminalApplication is what the terminal routes call: the candidate's
// access is proved by each call.
type terminalApplication interface {
	GetEnvironment(context.Context, invocation.Invocation, attempt.CandidateAccess) (model.AttemptEnvironment, error)
	OpenTerminal(context.Context, invocation.Invocation, attempt.CandidateAccess, environment.Window) (environment.Terminal, error)
	ListTerminals(context.Context, invocation.Invocation, attempt.CandidateAccess) (environment.TerminalList, error)
	InterruptTerminal(context.Context, invocation.Invocation, attempt.CandidateAccess, string) error
	AttachTerminal(context.Context, invocation.Invocation, attempt.CandidateAccess, string) (terminalSession, error)
}

// appTerminals adapts the application's terminals, which are absent when
// the installation runs no execution environments.
type appTerminals struct{ terminals *app.Terminals }

func (a appTerminals) OpenTerminal(
	ctx context.Context,
	inv invocation.Invocation,
	access attempt.CandidateAccess,
	window environment.Window,
) (environment.Terminal, error) {
	return a.terminals.OpenTerminal(ctx, inv, access, window)
}

func (a appTerminals) ListTerminals(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess) (environment.TerminalList, error) {
	return a.terminals.ListTerminals(ctx, inv, access)
}

func (a appTerminals) GetEnvironment(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess) (model.AttemptEnvironment, error) {
	return a.terminals.GetEnvironment(ctx, inv, access)
}

func (a appTerminals) InterruptTerminal(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess, tag string) error {
	return a.terminals.InterruptTerminal(ctx, inv, access, tag)
}

func (a appTerminals) AttachTerminal(ctx context.Context, inv invocation.Invocation, access attempt.CandidateAccess, tag string) (terminalSession, error) {
	session, err := a.terminals.AttachTerminal(ctx, inv, access, tag)
	if err != nil {
		return nil, err
	}
	return session, nil
}

type examAttemptTerminalHandlers struct {
	app     terminalApplication
	sockets BinarySocketTransport
	logger  *logging.Logger
}

// candidateTerminalErrors are the codes every terminal route can answer
// with: the candidate access check's and the environment's.
var candidateTerminalErrors = []string{
	"request.invalid",
	"resource.not_found",
	"exam.attempt.invalid",
	"exam.attempt.unavailable",
	"exam.attempt.registered_desktop_required",
	"exam.delivery.metadata_capacity",
	"exam.attempt.environment_not_found",
	"exam.attempt.environment_not_running",
	"exam.attempt.environment_unavailable",
	"exam.attempt.terminal_limit_reached",
	"exam.attempt.terminal_not_found",
	"exam.attempt.terminal_exited",
}

// environmentErrors are the codes reading the environment answers with.
var environmentErrors = []string{
	"request.invalid",
	"resource.not_found",
	"exam.attempt.invalid",
	"exam.attempt.unavailable",
	"exam.attempt.registered_desktop_required",
	"exam.delivery.metadata_capacity",
	"exam.attempt.environment_not_found",
	"exam.attempt.environment_unavailable",
}

func terminalErrors(extra ...string) []string {
	return append(append([]string(nil), candidateTerminalErrors...), extra...)
}

func examAttemptTerminalResource(application terminalApplication, sockets BinarySocketTransport, logger *logging.Logger) route.Resource {
	h := examAttemptTerminalHandlers{app: application, sockets: sockets, logger: logger}
	return route.NewResource("exam_attempt_terminals",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   environmentPath,
			Auth:   route.AuthSessionRequired,
			Errors: environmentErrors,
			Handle: h.environment,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         terminalsPath,
			Auth:         route.AuthSessionRequired,
			MaxBodyBytes: terminalOpenBytes,
			Errors:       terminalErrors(),
			Handle:       h.open,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   terminalsPath,
			Auth:   route.AuthSessionRequired,
			Errors: terminalErrors(),
			Handle: h.list,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         terminalSignal,
			Auth:         route.AuthSessionRequired,
			MaxBodyBytes: terminalOpenBytes,
			Errors:       terminalErrors(),
			Handle:       h.signal,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         terminalSocket,
			Auth:         route.AuthSessionRequired,
			ProtocolName: "exam-attempt-terminal-socket",
			ProtocolKind: route.ProtocolUpgrade,
			Errors:       terminalErrors("websocket.origin.invalid", "websocket.unavailable"),
			Upgrade:      h.socket,
		},
	)
}

// openTerminalRequest is the window a new terminal starts with; both
// members are optional and default to 80 by 24.
type openTerminalRequest struct {
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
}

// terminalResponse describes one terminal on the Attempt's current machine.
type terminalResponse struct {
	Number       int    `json:"number"`
	Tag          string `json:"tag"`
	Running      bool   `json:"running"`
	StartedAt    string `json:"started_at,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	ExitSignal   *int   `json:"exit_signal,omitempty"`
	OutputOffset uint64 `json:"output_offset"`
	InputOffset  uint64 `json:"input_offset"`
}

// terminalsResponse is the candidate's terminals on the current machine and
// how many may run at once.
type terminalsResponse struct {
	TerminalLimit int                `json:"terminal_limit"`
	Terminals     []terminalResponse `json:"terminals"`
}

// signalTerminalRequest names the signal; a candidate may only interrupt.
type signalTerminalRequest struct {
	Signal string `json:"signal"`
}

func terminalResponseFrom(terminal environment.Terminal) terminalResponse {
	number, _ := strconv.Atoi(terminal.Tag[len(environment.TerminalTagPrefix):])
	response := terminalResponse{Number: number, Tag: terminal.Tag, Running: terminal.Running,
		OutputOffset: terminal.Output, InputOffset: terminal.Input}
	if !terminal.Started.IsZero() {
		response.StartedAt = model.FormatRFC3339(terminal.Started)
	}
	if !terminal.Running {
		if terminal.ExitSignal != 0 {
			signal := terminal.ExitSignal
			response.ExitSignal = &signal
		} else {
			code := terminal.ExitCode
			response.ExitCode = &code
		}
	}
	return response
}

// terminalTag reads the terminal number of the path as its tag.
func terminalTag(request route.Request) (string, error) {
	number, err := strconv.Atoi(request.Params["terminal"])
	if err != nil || number < 1 || number > environment.MaxTerminalOpens {
		return "", route.InvalidRequest(errors.New("terminal number out of bounds"))
	}
	return environment.TerminalTag(number), nil
}

// windowFrom reads a requested window; neither member asks for the default.
func windowFrom(cols, rows int) (environment.Window, error) {
	if cols == 0 && rows == 0 {
		return environment.Window{}, nil
	}
	if cols < 1 || rows < 1 || cols > environment.MaxWindowCells || rows > environment.MaxWindowCells {
		return environment.Window{}, route.InvalidRequest(errors.New("cols and rows must each be from 1 to the window limit"))
	}
	return environment.Window{Cols: uint16(cols), Rows: uint16(rows)}, nil
}

func (h examAttemptTerminalHandlers) open(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	var body openTerminalRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	window, err := windowFrom(body.Cols, body.Rows)
	if err != nil {
		return route.Result{}, err
	}
	terminal, err := h.app.OpenTerminal(request.Context, request.Invocation(), access, window)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusCreated, terminalResponseFrom(terminal)).WithHeaders(privateNoStoreHeaders()), nil
}

// candidateEnvironmentResponse is the candidate's execution environment.
// A Workspace entry whose last change is past projection_cursor is not in
// the machine yet.
type candidateEnvironmentResponse struct {
	State            model.AttemptEnvironmentState   `json:"state"`
	Desired          model.AttemptEnvironmentDesired `json:"desired"`
	Incarnation      int64                           `json:"incarnation"`
	ProjectionCursor int64                           `json:"projection_cursor"`
	TerminalLimit    int                             `json:"terminal_limit"`
	WaitingSince     *string                         `json:"waiting_since,omitempty"`
}

func (h examAttemptTerminalHandlers) environment(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	environment, err := h.app.GetEnvironment(request.Context, request.Invocation(), access)
	if err != nil {
		return route.Result{}, err
	}
	response := candidateEnvironmentResponse{State: environment.State, Desired: environment.Desired,
		Incarnation: environment.Incarnation, ProjectionCursor: environment.ProjectionCursor,
		TerminalLimit: environment.Terminals}
	if !environment.WaitingSince.IsZero() {
		value := model.FormatRFC3339(environment.WaitingSince)
		response.WaitingSince = &value
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptTerminalHandlers) list(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	listed, err := h.app.ListTerminals(request.Context, request.Invocation(), access)
	if err != nil {
		return route.Result{}, err
	}
	response := terminalsResponse{TerminalLimit: listed.Limit, Terminals: make([]terminalResponse, len(listed.Terminals))}
	for index, terminal := range listed.Terminals {
		response.Terminals[index] = terminalResponseFrom(terminal)
	}
	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examAttemptTerminalHandlers) signal(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}
	tag, err := terminalTag(request)
	if err != nil {
		return route.Result{}, err
	}
	var body signalTerminalRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.Signal != string(environment.SignalInterrupt) {
		return route.Result{}, route.InvalidRequest(errors.New("signal must be interrupt"))
	}
	if err = h.app.InterruptTerminal(request.Context, request.Invocation(), access, tag); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

// socket attaches to the terminal before the upgrade, so a refused attach
// answers as a problem, then serves the terminal protocol on the socket.
func (h examAttemptTerminalHandlers) socket(writer http.ResponseWriter, request route.Request) error {
	access, err := candidateAccess(request)
	if err != nil {
		return err
	}
	tag, err := terminalTag(request)
	if err != nil {
		return err
	}
	if h.sockets == nil {
		return apperror.New("websocket.unavailable")
	}
	session, err := h.app.AttachTerminal(request.Context, request.Invocation(), access, tag)
	if err != nil {
		return err
	}
	allowMissingOrigin := route.CredentialSourceOf(request.Context) == route.CredentialBearer
	err = h.sockets.ServeBinarySocket(writer, request.HTTP, allowMissingOrigin, maxTerminalClientMessage,
		func(ctx context.Context, socket BinarySocket) BinarySocketOutcome {
			return serveTerminal(ctx, socket, session, h.logger, terminalRecheck)
		})
	if err != nil {
		session.Close(context.WithoutCancel(request.Context))
	}
	return err
}
