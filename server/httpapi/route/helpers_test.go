// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

// newFocusedResourceAPI compiles resources under the production API prefix
// with the limits most kernel tests share.
func newFocusedResourceAPI(
	t *testing.T,
	logger *logging.Logger,
	authenticator Authenticator,
	resources ...Resource,
) *API {
	t.Helper()
	cookies, err := NewCookies("http://localhost:8065")
	require.NoError(t, err)
	httpAPI := &API{
		authenticator:           authenticator,
		logger:                  logger,
		localizer:               routetest.NewLocalizer(t),
		cookies:                 cookies,
		browserRequests:         browserRequestPolicy{origin: "http://localhost:8065"},
		recentAuthenticationTTL: time.Minute,
	}
	require.NoError(t, httpAPI.compileRoutes(model.APIURLSuffix, 1<<20, resources...))
	return httpAPI
}

// healthResponse is a small JSON body for operations under test.
type healthResponse struct {
	Status string `json:"status"`
}

// problemTestLocalizer translates the forbidden presentation into French and
// every other message into the empty string.
type problemTestLocalizer struct{}

func (problemTestLocalizer) Translate(_ string, id string, _ any) (string, error) {
	translations := map[string]string{
		"problem.forbidden.title":  "Accès refusé",
		"problem.forbidden.detail": "Cette action n’est pas autorisée.",
	}
	return translations[id], nil
}

func (problemTestLocalizer) SupportedLocales() []string { return []string{"en", "fr"} }

func testHTTPDeliveryBudget(part model.AttemptParticipationID) *model.DeliveryBudgetSnapshot {
	return &model.DeliveryBudgetSnapshot{ParticipationID: part, Generation: 1, Native: model.DeliveryFamilyBudget{Participation: model.NewDeliveryQuotaUsage(true, false), Attempt: model.NewDeliveryQuotaUsage(true, true), PendingByteLimit: model.DeliveryPendingByteLimit}, Browser: model.DeliveryFamilyBudget{Participation: model.NewDeliveryQuotaUsage(false, false), Attempt: model.NewDeliveryQuotaUsage(false, true), PendingByteLimit: model.DeliveryPendingByteLimit}, ServerTime: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}
}
