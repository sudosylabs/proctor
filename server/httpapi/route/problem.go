// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"encoding/json"
	"net/http"

	"github.com/sudosylabs/proctor/server/model"
)

// Problem is the RFC 9457 Problem Details body of every failure. Code is the
// stable machine identifier and Type its documentation URI; Title and Detail
// are localized prose; Fields carries only safe field names and reasons.
// Delivery and the embedded capacity appear only on the Exam delivery failures
// that carry bounded repair context.
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

// Kernel problem codes belong to the routing kernel rather than to any one
// operation's declared application errors. error_catalog.yaml lists them as
// problem codes.
const (
	serverInternalCode        = "server.internal"
	routeNotFoundCode         = "route.not_found"
	routeMethodNotAllowedCode = "route.method_not_allowed"
	requestTooLargeCode       = "request.too_large"
)

// ProblemType is the documentation URI of a Problem Details code.
func ProblemType(code string) string {
	return "https://proctor.sudosylabs.com/problems/" + code
}

func internalProblem(request *http.Request) Problem {
	title, detail := localizedProblemPresentation(request, http.StatusInternalServerError)
	return Problem{
		Type:      ProblemType(serverInternalCode),
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
