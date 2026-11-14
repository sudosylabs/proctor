// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/sudosylabs/proctor/server/logging"
)

// prepareOperation gathers what the middleware attached to the request into
// the value an operation receives. It answers the request itself and reports
// false when the route requires a Principal the request does not carry, or
// the path parameters are missing.
func (a *API) prepareOperation(
	writer http.ResponseWriter,
	request *http.Request,
	definition Endpoint,
	errorPolicy routeErrorPolicy,
) (Request, bool) {
	principal, hasPrincipal := Principal(request.Context())
	if authRequiresPrincipal(definition.Auth) && !hasPrincipal {
		writeRouteApplicationError(writer, request, a.logger, errorPolicy, AuthenticationRequired())
		return Request{}, false
	}

	params, ok := requestParams(request.Context())
	if !ok {
		writeRouteApplicationError(writer, request, a.logger, errorPolicy, InvalidRequest(nil))
		return Request{}, false
	}

	return Request{
		Context:        request.Context(),
		Principal:      principal,
		Metadata:       requestMetadata(request.Context()),
		Params:         params,
		HTTP:           request,
		IdempotencyKey: idempotencyKeyFromContext(request.Context()),
	}, true
}

// writeOperationError answers with the failure an operation returned. A code
// the route did not declare, or an invalid response header, becomes an
// internal error instead, so a route cannot leak an undeclared failure.
// retryInProgress adds Retry-After to idempotency.in_progress.
func (a *API) writeOperationError(
	writer http.ResponseWriter,
	request *http.Request,
	errorPolicy routeErrorPolicy,
	err error,
	retryInProgress bool,
) {
	headers, cause := responseErrorParts(err)
	var failure applicationFailure
	if retryInProgress && errors.As(cause, &failure) && failure.Code() == "idempotency.in_progress" {
		if headers == nil {
			headers = make(http.Header)
		}

		headers.Set("Retry-After", "1")
	}

	if validateResponseHeaders(headers) != nil || !routeErrorAllowed(errorPolicy, cause) {
		a.logInvalidRouteError(request, cause)
		writeProblem(writer, internalProblem(request))
		return
	}

	applyResponseHeaders(writer, headers)
	writeApplicationError(writer, request, a.logger, cause)
}

func (a *API) upgradeOperationHandler(
	definition Endpoint,
	errorPolicy routeErrorPolicy,
) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		prepared, ok := a.prepareOperation(writer, request, definition, errorPolicy)
		if !ok {
			return
		}

		err := definition.Upgrade(writer, prepared)
		if err == nil {
			return
		}

		a.writeOperationError(writer, request, errorPolicy, err, false)
	})
}

func applyResponseHeaders(writer http.ResponseWriter, headers http.Header) {
	for key, values := range headers {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
}

func (a *API) operationHandler(
	definition Endpoint,
	errorPolicy routeErrorPolicy,
) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		prepared, ok := a.prepareOperation(writer, request, definition, errorPolicy)
		if !ok {
			return
		}

		result, err := definition.Handle(prepared)
		if err != nil {
			a.writeOperationError(writer, request, errorPolicy, err, true)
			return
		}

		if err := validateOperationResult(result, errorPolicy); err != nil {
			a.logger.ErrorContext(request.Context(), "route returned an invalid operation result", logging.Err(err))
			writeProblem(writer, internalProblem(request))
			return
		}

		applyResponseHeaders(writer, result.headers)
		if result.problem != nil {
			writeProblem(writer, *result.problem)
			return
		}

		if result.noContent {
			if result.status == http.StatusNoContent {
				writer.Header().Set("Cache-Control", "no-store")
			}

			writer.WriteHeader(result.status)
			return
		}

		writeJSON(writer, result.status, result.body)
	})
}

func (a *API) protocolOperationHandler(
	definition Endpoint,
	errorPolicy routeErrorPolicy,
) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		prepared, ok := a.prepareOperation(writer, request, definition, errorPolicy)
		if !ok {
			return
		}

		if definition.ProtocolKind == ProtocolStreamingUpload {
			if err := a.transferDeadlines.extendForUpload(writer, request.ContentLength, definition.MaxBodyBytes); err != nil {
				a.logger.ErrorContext(request.Context(), "extend upload transfer deadline", logging.Err(err))
			}
		}

		result, err := definition.Protocol(prepared)
		if result.body != nil {
			defer func() {
				if closeErr := result.body.Close(); closeErr != nil {
					a.logger.ErrorContext(request.Context(), "close protocol response body", logging.Err(closeErr))
				}
			}()
		}

		if err != nil {
			a.writeOperationError(writer, request, errorPolicy, err, true)
			return
		}

		if err := validateProtocolResult(definition.ProtocolKind, result); err != nil {
			a.logger.ErrorContext(request.Context(), "route returned an invalid protocol result", logging.Err(err))
			writeProblem(writer, internalProblem(request))
			return
		}

		applyResponseHeaders(writer, result.headers)
		switch result.kind {
		case ProtocolRedirect:
			http.Redirect(writer, request, result.location, result.status)
		case ProtocolBinaryDownload:
			if result.status == http.StatusNotModified {
				writer.Header().Set("Content-Length", strconv.FormatInt(result.contentLength, 10))
				writer.WriteHeader(result.status)
				return
			}

			if err := a.transferDeadlines.extendForDownload(writer, result.contentLength); err != nil {
				a.logger.ErrorContext(request.Context(), "extend download transfer deadline", logging.Err(err))
			}

			writer.Header().Set("Content-Length", strconv.FormatInt(result.contentLength, 10))
			if result.status == http.StatusPartialContent {
				writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d",
					result.rangeStart, result.rangeStart+result.contentLength-1, result.rangeTotal))
			}
			writer.WriteHeader(result.status)
			written, copyErr := io.CopyN(writer, result.body, result.contentLength)
			if copyErr != nil || written != result.contentLength {
				a.logger.ErrorContext(request.Context(), "write bounded protocol response", logging.Int64("written", written), logging.Err(copyErr))
			}
		case ProtocolStreamingUpload:
			writeJSON(writer, result.status, result.jsonBody)
		}
	})
}

func (a *API) logInvalidRouteError(request *http.Request, err error) {
	a.logger.ErrorContext(
		request.Context(),
		"route returned an invalid application error response",
		logging.String("request_id", RequestID(request.Context())),
		logging.String("error_id", applicationErrorCode(err)),
		logging.Err(err),
	)
}
