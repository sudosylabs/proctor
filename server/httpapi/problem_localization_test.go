// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

type problemTestLocalizer struct{}

func (problemTestLocalizer) Translate(_ string, id string, _ any) (string, error) {
	translations := map[string]string{
		"authorization.role.system_admin.description": "Accès complet à l’installation.",
		"authorization.role.system_admin.name":        "Administrateur système",
		"problem.forbidden.title":                     "Accès refusé",
		"problem.forbidden.detail":                    "Cette action n’est pas autorisée.",
		"session.revocation.user_logout":              "Déconnectée par l’utilisateur.",
	}
	return translations[id], nil
}

func TestSessionAndBuiltInRolePresentationLocalizesWithoutChangingIdentifiers(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request = request.WithContext(route.WithLocalization(context.Background(), problemTestLocalizer{}, "fr"))

	session := sessionResponseFromModel(request, &model.Session{
		RevocationReason: model.SessionRevocationUserLogout,
	})
	require.Equal(t, "user_logout", session.RevocationReasonCode)
	require.Equal(t, "Déconnectée par l’utilisateur.", session.RevocationReason)

	role := roleResponseFromModel(request, &model.Role{
		Name: model.SystemAdministratorRoleName, DisplayName: "System Administrator",
		Description: "Full installation access.", BuiltIn: true,
	})
	require.Equal(t, model.SystemAdministratorRoleName, role.Name)
	require.Equal(t, "Administrateur système", role.DisplayName)
	require.Equal(t, "Accès complet à l’installation.", role.Description)
}

func (l problemTestLocalizer) TranslateRequest(locale string, request localization.Request) (string, error) {
	return l.Translate(locale, request.ID, request.TemplateData)
}

func (problemTestLocalizer) SupportedLocales() []string { return []string{"en", "fr"} }

func TestPreferredLocaleHonorsAcceptLanguageQuality(t *testing.T) {
	t.Parallel()
	require.Equal(t, "fr", localization.PreferredLocale("de;q=0.4, fr-CA;q=0.9, en;q=0.8", []string{"en", "fr"}))
}

func TestNewRequiresLocalizer(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	_, err := New(completeHTTPApplication(), Settings{Logger: logger, PublicURL: "http://localhost:8065"})
	require.Error(t, err, "New without localizer error")
	require.Contains(t, err.Error(), "localizer is required", "New without localizer error")
}
