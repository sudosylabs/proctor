// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
package httpapi

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestDeliveryBudgetHTTPRequiresOwnedSelectorAndLiveStopFences(t *testing.T) {
	f := newAttemptFixture(t)
	api := f.api(t, deliveryBudgetResource(f.service))
	base := f.candidatePath("/delivery-limits")
	request := f.candidateRequest(t, http.MethodGet, base+"?participation_id="+f.participation.ID.String())
	request.Header.Del(candidateAttemptCredentialHeader)
	request.Header.Del(candidateAttemptConnectionHeader)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, 200, response.Code, "budget=%d %s", response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "budget=%d %s", response.Code, response.Body.String())
	require.Len(t, f.deliveryStore.budgets, 1, "budget=%d %s", response.Code, response.Body.String())
	require.Equal(t, f.participation.ID, f.deliveryStore.budgets[0].ParticipationID, "budget=%d %s", response.Code, response.Body.String())
	for _, bad := range []string{"", "?participation_id=invalid", "?participation_id=" + f.participation.ID.String() + "&participation_id=" + f.participation.ID.String()} {
		before := f.deliveryStore.requests()
		request = f.candidateRequest(t, http.MethodGet, base+bad)
		response = httptest.NewRecorder()
		api.ServeHTTP(response, request)
		require.Equal(t, 400, response.Code, "invalid budget selector reached the Store")
		require.Equal(t, before, f.deliveryStore.requests(), "invalid budget selector reached the Store")
	}
	body := `{"family":"browser","reason":"local_loss_inventory_exhausted"}`
	for _, test := range []struct {
		name, body      string
		credential, key bool
		want            int
	}{
		{"valid", body, true, true, 200}, {"no Connection", body, false, true, 400}, {"no idempotency", body, true, false, 400}, {"unknown family", `{"family":"all","reason":"local_loss_inventory_exhausted"}`, true, true, 400}, {"invented reason", `{"family":"browser","reason":"reset_quota"}`, true, true, 400}, {"client-selected generation", `{"family":"browser","reason":"local_loss_inventory_exhausted","generation":2}`, true, true, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := f.deliveryStore.requests()
			request := f.candidateRequest(t, http.MethodPost, base+"/stop-details")
			request.Body = io.NopCloser(strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.key {
				request.Header.Set("Idempotency-Key", "stop-once")
			}
			if !test.credential {
				request.Header.Del(candidateAttemptConnectionHeader)
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			require.Equal(t, test.want, response.Code, "stop=%d %s", response.Code, response.Body.String())
			require.False(t, test.want != 200 && f.deliveryStore.requests() != before, "stop=%d %s", response.Code, response.Body.String())
			if test.want == 200 {
				stop := f.deliveryStore.stops[len(f.deliveryStore.stops)-1]
				require.Equal(t, f.connection.ID, stop.Access.ConnectionID, "live fences or idempotency lost")
				require.Equal(t, model.HashToken(f.credential), stop.Access.ContinuityCredentialHash, "live fences or idempotency lost")
				require.Equal(t, sha256.Sum256([]byte("stop-once")), f.deliveryStore.lastCommand(t).KeyDigest, "live fences or idempotency lost")
			}
		})
	}
}
