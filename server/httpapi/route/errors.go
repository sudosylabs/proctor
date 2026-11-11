// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/user.go login/logout handlers,
// its session-authenticated boundary, and browser session-cookie behavior.
// Proctor keeps explicit catalog classification and an immutable request
// principal without Mattermost's product-specific handler context.

package route

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

// InvalidRequest returns request.invalid wrapping err: the one code for a
// request the transport cannot decode, or whose path, query or body value is
// malformed. The cause is kept for diagnostics and never presented.
func InvalidRequest(err error) error {
	out := apperror.New("request.invalid")
	if err != nil {
		out = out.Wrap(err)
	}
	return out
}

// AuthenticationRequired returns authentication.required, for an operation that
// reads a credential itself and finds none.
func AuthenticationRequired() error {
	return apperror.New("authentication.required")
}

func ambiguousCredentialError() error {
	return apperror.New("authentication.credential_ambiguous").WithField("field", "credential")
}

func writeApplicationError(
	writer http.ResponseWriter,
	request *http.Request,
	logger *logging.Logger,
	appErr error,
) {
	if errorIsLogged(appErr) {
		logger.ErrorContext(
			request.Context(),
			"application request failed",
			logging.String("request_id", RequestID(request.Context())),
			logging.String("error_id", applicationErrorCode(appErr)),
			logging.Err(appErr),
		)
	}
	WriteError(writer, request, appErr)
}

func applicationErrorCode(err error) string {
	var failure applicationFailure
	if errors.As(err, &failure) {
		return failure.Code()
	}
	return serverInternalCode
}

// errorIsLogged reports whether the kernel writes an error record when an
// operation fails with err: every failure presented as a 5xx or without a
// mapped code, except service.busy, which its work pool counts instead.
func errorIsLogged(err error) bool {
	var failure applicationFailure
	if errors.As(err, &failure) {
		// Capacity refusals are expected overload outcomes counted by the owning
		// work pool. Logging each refusal would amplify a request burst.
		if failure.Code() == "service.busy" {
			return false
		}
		mapping, ok := applicationErrorMappings[failure.Code()]
		return !ok || mapping.status >= http.StatusInternalServerError
	}
	return true
}

type operationResponseError struct {
	err     error
	headers http.Header
}

func (f *operationResponseError) Error() string { return f.err.Error() }

func (f *operationResponseError) Unwrap() error { return f.err }

// ErrorWithHeaders attaches response headers to a failure, such as cleared
// cookies or a DPoP nonce challenge. The kernel writes them only when the route
// declares the failure's code. A nil err stays nil.
func ErrorWithHeaders(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	return &operationResponseError{err: err, headers: headers.Clone()}
}

func responseErrorParts(err error) (http.Header, error) {
	var responseError *operationResponseError
	if errors.As(err, &responseError) {
		return responseError.headers.Clone(), responseError.err
	}
	return nil, err
}

func writeRouteApplicationError(
	writer http.ResponseWriter,
	request *http.Request,
	logger *logging.Logger,
	policy routeErrorPolicy,
	err error,
) {
	if !routeErrorAllowed(policy, err) {
		logger.ErrorContext(
			request.Context(),
			"route returned an undeclared application error",
			logging.String("request_id", RequestID(request.Context())),
			logging.String("error_id", applicationErrorCode(err)),
			logging.Err(err),
		)
		writeProblem(writer, internalProblem(request))
		return
	}
	writeApplicationError(writer, request, logger, err)
}

// routeErrorAllowed reports whether a route may present err. Every route may
// fail with an oversized body or an unexpected internal fault, which carry no
// application code; an application error must be declared by the route.
func routeErrorAllowed(policy routeErrorPolicy, err error) bool {
	var failure applicationFailure
	if policy == nil || requestBodyTooLarge(err) || !errors.As(err, &failure) {
		return true
	}
	_, allowed := policy[failure.Code()]
	return allowed
}

// applicationFailure is the transport-facing view of a transport-neutral
// application error such as *apperror.Error (Code + Fields). The interface keeps
// HTTP mapping duck-typed over any error exposing a stable code and safe
// fields.
type applicationFailure interface {
	error
	Code() string
	Fields() map[string]string
}

// applicationErrorMapping is the HTTP presentation for one public error code.
// The Problem Details type is derived from the code. The table itself is
// generated from error_catalog.yaml into error_mapping.go.
type applicationErrorMapping struct {
	status int
	// retryAfter is the Retry-After delay in seconds; zero sends none.
	retryAfter int
	// extension is the Problem Details extension member the code may carry.
	extension problemExtension
}

// problemExtension names an optional Problem Details extension member. The
// catalog grants one to a code; the kernel presents it only for that code and
// only when the error's cause chain supplies a valid value.
type problemExtension int

const (
	problemExtensionNone problemExtension = iota
	// problemExtensionDelivery is the delivery member: a cause in the chain
	// implements DeliveryRecovery() *model.DeliveryRecovery.
	problemExtensionDelivery
	// problemExtensionDeliveryMetadataCapacity is the
	// delivery_metadata_capacity member: the chain holds a
	// *model.DeliveryMetadataCapacity, which replaces the fields member.
	problemExtensionDeliveryMetadataCapacity
)

func problemFromApplicationFailure(request *http.Request, failure applicationFailure) Problem {
	mapping, ok := applicationErrorMappings[failure.Code()]
	if !ok {
		return internalProblem(request)
	}

	status := mapping.status
	if status < 400 || status > 599 {
		return internalProblem(request)
	}
	title, detail := localizedProblemPresentation(request, status)
	return newProblem(request, failure.Code(), status, title, detail, failure.Fields())
}

func newProblem(
	request *http.Request,
	code string,
	status int,
	title, detail string,
	fields map[string]string,
) Problem {
	return Problem{
		Type:      ProblemType(code),
		Title:     title,
		Status:    status,
		Detail:    detail,
		Instance:  request.URL.Path,
		Code:      code,
		RequestID: RequestID(request.Context()),
		Fields:    fields,
	}
}

// ApplicationErrorStatuses returns a copy of the generated table that maps
// every public application error code to its HTTP status. The agreement tests
// in httpapi compare the OpenAPI document with it.
func ApplicationErrorStatuses() map[string]int {
	cloned := make(map[string]int, len(applicationErrorMappings))
	for code, mapping := range applicationErrorMappings {
		cloned[code] = mapping.status
	}
	return cloned
}

// ProblemStatuses returns a copy of the generated table of the codes the
// kernel and the health probes write directly as Problem Details, which no
// application code mints. The agreement tests in httpapi read it.
func ProblemStatuses() map[string]int {
	return maps.Clone(transportProblemCodes)
}

// WriteError maps a failure to RFC 9457 Problem Details. Transport-neutral
// application failures (*apperror.Error) use the centralized code table through the
// applicationFailure interface. Unexpected errors become a generic correlated
// internal response with no internal detail.
func WriteError(writer http.ResponseWriter, request *http.Request, err error) {
	if requestBodyTooLarge(err) {
		writeRequestTooLarge(writer, request)
		return
	}

	var failure applicationFailure
	if errors.As(err, &failure) {
		mapping := applicationErrorMappings[failure.Code()]
		if mapping.retryAfter > 0 {
			writer.Header().Set("Retry-After", strconv.Itoa(mapping.retryAfter))
		}

		problem := problemFromApplicationFailure(request, failure)
		switch mapping.extension {
		case problemExtensionDeliveryMetadataCapacity:
			var capacity *model.DeliveryMetadataCapacity
			if errors.As(err, &capacity) && capacity.Validate() == nil {
				value := *capacity
				problem.DeliveryMetadataCapacity = &value
				problem.Fields = nil
			}
		case problemExtensionDelivery:
			var recovery interface {
				DeliveryRecovery() *model.DeliveryRecovery
			}
			if errors.As(err, &recovery) {
				if value := recovery.DeliveryRecovery(); value != nil && value.Validate() == nil {
					problem.Delivery = value
				}
			}
		case problemExtensionNone:
		}

		writeProblem(writer, problem)
		return
	}

	writeProblem(writer, internalProblem(request))
}

// ErrUnpresentable reports an application result or capability that the
// transport cannot present: a missing or malformed result, a result that
// exceeds its wire bound, or a cursor that cannot be encoded. It is a server
// fault, never a public application condition, so the transport mints no
// application code for it: the kernel answers with the generic correlated
// internal problem and logs the cause once.
var ErrUnpresentable = errors.New("application result cannot be presented")

// Unpresentable returns ErrUnpresentable wrapping cause, which may be nil.
func Unpresentable(cause error) error {
	if cause == nil {
		return ErrUnpresentable
	}
	return fmt.Errorf("%w: %w", ErrUnpresentable, cause)
}
