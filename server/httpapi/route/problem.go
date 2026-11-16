// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sudosylabs/proctor/server/model"
)

// Problem is the RFC 9457 Problem Details body of every failure. Code is the
// stable machine identifier and Type its documentation URI; Title and Detail
// are localized prose; Fields carries only safe field names and reasons.
// Delivery and the embedded capacity appear only on exam delivery failures.
type Problem struct {
	Delivery *model.DeliveryRecovery `json:"delivery,omitempty"`
	*model.DeliveryMetadataCapacity
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Instance  string            `json:"instance,omitempty"`
	Code      string            `json:"code"`
	RequestID string            `json:"request_id,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

func writeProblem(writer http.ResponseWriter, problem Problem) {
	if problem.Type == "" {
		problem.Type = "about:blank"
	}
	if problem.Status == 0 {
		problem.Status = http.StatusInternalServerError
	}
	if problem.Code == "" {
		problem.Code = serverInternalCode
	}

	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(problem.Status)
	_ = json.NewEncoder(writer).Encode(problem)
}

// Kernel problem codes are answered by the kernel itself, not by an
// operation; error_catalog.yaml lists them as problem codes.
const (
	serverInternalCode        = "server.internal"
	routeNotFoundCode         = "route.not_found"
	routeMethodNotAllowedCode = "route.method_not_allowed"
	requestTooLargeCode       = "request.too_large"
)

// ProblemType is the type URI of a Problem Details code: its page on the
// installation's documentation site, or "about:blank" when none is configured.
// Clients act on code; type only points a person at the explanation.
func ProblemType(request *http.Request, code string) string {
	if request != nil {
		if base, ok := request.Context().Value(problemDocumentationContextKey{}).(string); ok && base != "" {
			return base + code
		}
	}
	return "about:blank"
}

type problemDocumentationContextKey struct{}

// WithProblemDocumentation attaches the documentation base that ProblemType
// reads, such as "https://docs.example.edu/problems/". The API does this for
// every request; a test uses it to build a Problem outside one.
func WithProblemDocumentation(ctx context.Context, base string) context.Context {
	return context.WithValue(ctx, problemDocumentationContextKey{}, base)
}

// problemDocumentationBase turns the configured documentation URL into the
// prefix of every problem page. Empty stays empty.
func problemDocumentationBase(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("documentation URL must be an absolute HTTP or HTTPS URL without query or fragment")
	}
	return strings.TrimSuffix(raw, "/") + "/problems/", nil
}

func (a *API) prepare(request *http.Request) *http.Request {
	if a.problemDocumentation != "" {
		request = request.WithContext(WithProblemDocumentation(request.Context(), a.problemDocumentation))
	}
	return a.localize(request)
}

func internalProblem(request *http.Request) Problem {
	title, detail := localizedProblemPresentation(request, http.StatusInternalServerError)
	return Problem{
		Type:      ProblemType(request, serverInternalCode),
		Title:     title,
		Status:    http.StatusInternalServerError,
		Detail:    detail,
		Instance:  request.URL.Path,
		Code:      serverInternalCode,
		RequestID: RequestID(request.Context()),
	}
}

func localizedProblemPresentation(request *http.Request, status int) (string, string) {
	return ProblemPresentation(request, problemPresentationName(status))
}

// ProblemPresentation returns the localized title and detail of the named
// presentation, such as "not_live" or "forbidden". Each is "" when the request
// carries no localizer or the message is missing. An operation that builds a
// Problem itself uses it.
func ProblemPresentation(request *http.Request, name string) (string, string) {
	if request == nil {
		return "", ""
	}
	localization, ok := request.Context().Value(localizationContextKey{}).(requestLocalization)
	if !ok || localization.localizer == nil {
		return "", ""
	}
	title, detail := "", ""
	if translated, err := localization.localizer.Translate(localization.locale, "problem."+name+".title", nil); err == nil {
		title = translated
	}
	if translated, err := localization.localizer.Translate(localization.locale, "problem."+name+".detail", nil); err == nil {
		detail = translated
	}
	return title, detail
}

func problemPresentationName(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "content_too_large"
	case http.StatusTooManyRequests:
		return "too_many_requests"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		if status >= 400 && status < 500 {
			return "client_error"
		}
		return "internal"
	}
}
