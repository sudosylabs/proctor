// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/localization"
)

// templatedTestLocalizer compiles a small English and French catalog with a
// templated message and a plural one, so rendering runs through go-i18n.
func templatedTestLocalizer(t *testing.T) *localization.Localizer {
	t.Helper()
	localizer, err := localization.New(fstest.MapFS{
		"en.json": {Data: []byte(`[
			{"id": "test.greeting", "translation": "{{.Actor}} enrolled {{.Subject}}"},
			{"id": "test.plain", "translation": "Plain"},
			{"id": "test.sessions", "translation": {"one": "{{.Count}} session", "other": "{{.Count}} sessions"}}
		]`)},
		"fr.json": {Data: []byte(`[
			{"id": "test.greeting", "translation": "{{.Actor}} a inscrit {{.Subject}}"}
		]`)},
	}, localization.EnglishLocale)
	require.NoError(t, err)
	return localizer
}

func localizedTestRequest(t *testing.T, localizer Localizer, locale string) *http.Request {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	return request.WithContext(WithLocalization(request.Context(), localizer, locale))
}

func TestTranslateRequestRendersArgumentsAndPluralForms(t *testing.T) {
	t.Parallel()
	localizer := templatedTestLocalizer(t)
	english := localizedTestRequest(t, localizer, "en")
	french := localizedTestRequest(t, localizer, "fr")
	greeting := localization.Request{ID: "test.greeting", TemplateData: map[string]string{"Actor": "Ada", "Subject": "Bo"}}

	require.Equal(t, "Ada enrolled Bo", TranslateRequest(english, greeting, "fallback"))
	require.Equal(t, "Ada a inscrit Bo", TranslateRequest(french, greeting, "fallback"))
	for count, want := range map[int]string{1: "1 session", 3: "3 sessions"} {
		require.Equal(t, want, TranslateRequest(english, localization.Request{
			ID: "test.sessions", TemplateData: map[string]int{"Count": count}, PluralCount: count,
		}, "fallback"))
	}
	require.Equal(t, "Plain", TranslateRequest(french, localization.Request{ID: "test.plain"}, "fallback"),
		"a message missing from the negotiated locale falls back to English")
	require.Equal(t, "Plain", Translate(localizedTestRequest(t, localizer, ""), "test.plain", "fallback"))
}

func TestTranslateRequestFallsBackWhenAMessageCannotRender(t *testing.T) {
	t.Parallel()
	request := localizedTestRequest(t, templatedTestLocalizer(t), "en")
	require.Equal(t, "fallback", TranslateRequest(request, localization.Request{ID: "test.greeting",
		TemplateData: map[string]string{"Actor": "Ada"}}, "fallback"), "a missing argument")
	require.Equal(t, "fallback", TranslateRequest(request, localization.Request{ID: "test.sessions"}, "fallback"), "a plural without a count")
	require.Equal(t, "fallback", TranslateRequest(request, localization.Request{ID: "test.unknown"}, "fallback"), "an unknown message")
	unlocalized := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	require.Equal(t, "fallback", TranslateRequest(unlocalized, localization.Request{ID: "test.plain"}, "fallback"), "no localizer")
}

// TestValidateLocalizationRendersEveryDeclaredMessage pins the construction
// check: a templated or plural message must render with its declared
// variables, and a message the declaration does not describe fails.
func TestValidateLocalizationRendersEveryDeclaredMessage(t *testing.T) {
	t.Parallel()
	localizer := templatedTestLocalizer(t)
	require.NoError(t, ValidateLocalization(localizer, []localization.Definition{
		{ID: "test.greeting", Origin: "test", Variables: []string{"Actor", "Subject"}},
		{ID: "test.plain", Origin: "test"},
		{ID: "test.sessions", Origin: "test", Variables: []string{"Count"}, PluralVariable: "Count"},
	}))

	for name, definition := range map[string]localization.Definition{
		"missing message":     {ID: "test.missing", Origin: "test"},
		"undeclared variable": {ID: "test.greeting", Origin: "test", Variables: []string{"Actor"}},
		"undeclared plural":   {ID: "test.sessions", Origin: "test", Variables: []string{"Count"}},
	} {
		require.Error(t, ValidateLocalization(localizer, []localization.Definition{definition}), name)
	}
}
