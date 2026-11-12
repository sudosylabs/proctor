// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"context"
	"fmt"
	"net/http"

	"github.com/sudosylabs/proctor/server/localization"
)

const localizationOrigin = "httpapi"

var problemPresentationNames = []string{
	"bad_request",
	"client_error",
	"conflict",
	"content_too_large",
	"forbidden",
	"internal",
	"method_not_allowed",
	"not_live",
	"not_found",
	"not_ready",
	"service_unavailable",
	"too_many_requests",
	"unauthorized",
}

// LocalizationDefinitions returns the messages the kernel presents: a title and
// a detail for each Problem Details presentation. New refuses a localizer that
// cannot translate one of them.
func LocalizationDefinitions() []localization.Definition {
	definitions := make([]localization.Definition, 0, len(problemPresentationNames)*2)
	for _, name := range problemPresentationNames {
		for _, field := range []string{"detail", "title"} {
			definitions = append(definitions, localization.Definition{
				ID:     "problem." + name + "." + field,
				Origin: localizationOrigin,
			})
		}
	}
	return definitions
}

// Translate returns message id in the request's negotiated locale, or fallback
// when the request carries no localizer or the message is missing.
func Translate(request *http.Request, id, fallback string) string {
	return TranslateRequest(request, localization.Request{ID: id}, fallback)
}

// TranslateRequest returns message in the request's negotiated locale with its
// template data and, for a plural message, its count, or fallback when the
// request carries no localizer or the message is missing or cannot render.
func TranslateRequest(request *http.Request, message localization.Request, fallback string) string {
	if request == nil {
		return fallback
	}
	requestLocalization, ok := request.Context().Value(localizationContextKey{}).(requestLocalization)
	if !ok || requestLocalization.localizer == nil {
		return fallback
	}
	translated, err := requestLocalization.localizer.TranslateRequest(requestLocalization.locale, message)
	if err != nil {
		return fallback
	}
	return translated
}

// ValidateLocalization reports the first definition the localizer cannot
// translate, so a missing message fails construction instead of a request.
// Each message renders with a sample value for every declared variable and,
// when plural, a count, so a templated message is proven renderable too.
func ValidateLocalization(localizer Localizer, definitions []localization.Definition) error {
	for _, definition := range definitions {
		if _, err := localizer.TranslateRequest("", sampleLocalizationRequest(definition)); err != nil {
			return fmt.Errorf("validate localization %q: %w", definition.ID, err)
		}
	}
	return nil
}

func sampleLocalizationRequest(definition localization.Definition) localization.Request {
	message := localization.Request{ID: definition.ID}
	if len(definition.Variables) == 0 && definition.PluralVariable == "" {
		return message
	}
	data := make(map[string]any, len(definition.Variables)+1)
	for _, variable := range definition.Variables {
		data[variable] = variable
	}
	if definition.PluralVariable != "" {
		data[definition.PluralVariable] = 1
		message.PluralCount = 1
	}
	message.TemplateData = data
	return message
}

func (a *API) localize(request *http.Request) *http.Request {
	if a.localizer == nil {
		return request
	}
	return request.WithContext(WithLocalization(
		request.Context(),
		a.localizer,
		localization.PreferredLocale(request.Header.Get("Accept-Language"), a.localizer.SupportedLocales()),
	))
}

type localizationContextKey struct{}

type requestLocalization struct {
	localizer Localizer
	locale    string
}

// WithLocalization attaches the localizer and negotiated locale that Translate
// and Problem Details presentation read. The API does this for every request; a
// test uses it to present a value outside one.
func WithLocalization(ctx context.Context, localizer Localizer, locale string) context.Context {
	return context.WithValue(ctx, localizationContextKey{}, requestLocalization{localizer: localizer, locale: locale})
}
