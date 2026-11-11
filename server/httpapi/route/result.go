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
	"net/url"
	"strings"
)

// Result is the success an ordinary operation returns: a JSON body, an empty
// response, or a Problem the route declares. Build it with JSON, NoContent,
// Status or ProblemResult. The kernel validates it before writing anything.
type Result struct {
	status    int
	body      any
	noContent bool
	problem   *Problem
	headers   http.Header
}

// JSON answers with a 2xx status and body encoded as JSON. The response is
// Cache-Control: no-store unless a header set otherwise.
func JSON(status int, body any) Result {
	return Result{status: status, body: body}
}

// NoContent answers 204 with no body.
func NoContent() Result {
	return Result{status: http.StatusNoContent, noContent: true}
}

// Status answers with a 2xx status and no body, for 202 Accepted and the like.
func Status(status int) Result {
	return Result{status: status, noContent: true}
}

// ProblemResult answers with a Problem the operation built itself, whose code
// must be in the endpoint's Errors. Most failures are returned as errors
// instead.
func ProblemResult(problem Problem) Result {
	return Result{status: problem.Status, problem: &problem}
}

// WithHeaders returns the result with a copy of headers to write before the
// body, replacing any attached earlier.
func (r Result) WithHeaders(headers http.Header) Result {
	r.headers = headers.Clone()
	return r
}

// Operation handles an ordinary endpoint. A returned error is presented as
// Problem Details when the endpoint declares its code, and as server.internal
// otherwise.
type Operation func(Request) (Result, error)

// ProtocolResult is the success a Protocol operation returns. Its constructor
// must match the endpoint's ProtocolKind.
type ProtocolResult struct {
	kind          ProtocolKind
	status        int
	location      string
	body          io.ReadCloser
	contentLength int64
	jsonBody      any
	headers       http.Header
}

// Redirect answers 303 See Other to location.
func Redirect(location string) ProtocolResult {
	return ProtocolResult{kind: ProtocolRedirect, status: http.StatusSeeOther, location: location}
}

// BinaryDownload streams exactly contentLength bytes of body with status 200.
// The kernel sets Content-Length, extends the write deadline for the size, and
// closes body.
func BinaryDownload(body io.ReadCloser, contentLength int64) ProtocolResult {
	return ProtocolResult{kind: ProtocolBinaryDownload, status: http.StatusOK, body: body, contentLength: contentLength}
}

// NotModified answers 304 to a conditional download whose representation of
// contentLength bytes is unchanged.
func NotModified(contentLength int64) ProtocolResult {
	return ProtocolResult{kind: ProtocolBinaryDownload, status: http.StatusNotModified, contentLength: contentLength}
}

// StreamingUpload answers a streamed upload with a 2xx status and a JSON body.
func StreamingUpload(status int, body any) ProtocolResult {
	return ProtocolResult{kind: ProtocolStreamingUpload, status: status, jsonBody: body}
}

// WithHeaders returns the result with a copy of headers to write before the
// response, replacing any attached earlier. Location and Content-Length belong
// to the kernel and are refused.
func (r ProtocolResult) WithHeaders(headers http.Header) ProtocolResult {
	r.headers = headers.Clone()
	return r
}

type headerOnlyResponseWriter struct {
	header http.Header
}

func (w headerOnlyResponseWriter) Header() http.Header { return w.header }

func (headerOnlyResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("response header operation cannot write a body")
}

func (headerOnlyResponseWriter) WriteHeader(int) {
	panic("response header capture cannot write a status")
}

// CaptureHeaders runs operation against a writer that records only headers and
// returns them, so the Set-Cookie headers of a Cookies method can travel on a
// Result or an error. Writing a body through it fails.
func CaptureHeaders(operation func(http.ResponseWriter)) http.Header {
	headers := make(http.Header)
	if operation != nil {
		operation(headerOnlyResponseWriter{header: headers})
	}
	return headers
}

// CombineHeaders returns the union of groups, keeping every value of a repeated
// header in order.
func CombineHeaders(groups ...http.Header) http.Header {
	combined := make(http.Header)
	for _, headers := range groups {
		for key, values := range headers {
			for _, value := range values {
				combined.Add(key, value)
			}
		}
	}
	return combined
}

func validateResponseHeaders(headers http.Header) error {
	for key, values := range headers {
		if key == "" || http.CanonicalHeaderKey(key) == "" || strings.ContainsAny(key, "\r\n") {
			return fmt.Errorf("response header name %q is invalid", key)
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("response header %q contains an invalid value", key)
			}
		}
	}
	return nil
}

func validateOperationResult(result Result, errorPolicy routeErrorPolicy) error {
	if err := validateResponseHeaders(result.headers); err != nil {
		return err
	}

	if result.problem != nil {
		if result.status < 400 ||
			result.status > 599 ||
			result.problem.Status != result.status ||
			result.body != nil ||
			result.noContent ||
			result.problem.Code == "" {
			return errors.New("problem result shape is invalid")
		}

		if _, allowed := errorPolicy[result.problem.Code]; !allowed {
			return fmt.Errorf("problem code %q is undeclared", result.problem.Code)
		}

		return nil
	}

	if result.status < 200 || result.status > 299 {
		return fmt.Errorf("success status %d is invalid", result.status)
	}
	if result.noContent && result.body != nil {
		return errors.New("empty result contains a body")
	}

	return nil
}

func validateProtocolResult(expected ProtocolKind, result ProtocolResult) error {
	if result.kind != expected {
		return fmt.Errorf("protocol result kind %q does not match route kind %q", result.kind, expected)
	}
	if err := validateResponseHeaders(result.headers); err != nil {
		return err
	}
	if result.headers.Get("Location") != "" || result.headers.Get("Content-Length") != "" {
		return errors.New("protocol-owned headers cannot be supplied by an operation")
	}

	switch result.kind {
	case ProtocolRedirect:
		if result.status != http.StatusSeeOther ||
			result.location == "" ||
			strings.ContainsAny(result.location, "\r\n") ||
			result.body != nil ||
			result.contentLength != 0 ||
			result.jsonBody != nil {
			return errors.New("redirect protocol result is invalid")
		}

		if _, err := url.Parse(result.location); err != nil {
			return fmt.Errorf("redirect location: %w", err)
		}
	case ProtocolBinaryDownload:
		if result.location != "" || result.jsonBody != nil {
			return errors.New("binary protocol result contains incompatible fields")
		}

		if result.status == http.StatusNotModified {
			if result.body != nil || result.contentLength < 0 {
				return errors.New("not-modified binary result contains a body")
			}
		} else if result.status != http.StatusOK || result.body == nil || result.contentLength < 0 {
			return errors.New("binary protocol result is invalid")
		}
	case ProtocolStreamingUpload:
		if result.status < 200 || result.status > 299 || result.location != "" || result.body != nil || result.contentLength != 0 {
			return errors.New("streaming upload protocol result is invalid")
		}
	default:
		return fmt.Errorf("protocol result kind %q is unsupported", result.kind)
	}

	return nil
}

// Status returns the HTTP status the result answers with.
func (r Result) Status() int { return r.status }

// Body returns the value a JSON result encodes, or nil for an empty result
// or a problem.
func (r Result) Body() any { return r.body }

// Headers returns a copy of the response headers attached to the result.
func (r Result) Headers() http.Header { return r.headers.Clone() }

// ProtocolOperation handles a Protocol endpoint. The kernel closes a body it
// returns, on failure too.
type ProtocolOperation func(Request) (ProtocolResult, error)

// UpgradeOperation handles the WebSocket upgrade. It alone receives the
// ResponseWriter; an error it returns must precede any write.
type UpgradeOperation func(http.ResponseWriter, Request) error
