// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/internal/errorcatalog"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestNativeDeliveryHTTPPreservesOptionalHistoricalFences(t *testing.T) {
	f := newAttemptFixture(t)
	api := f.api(t, nativeDeliveryResource(f.service))
	path := f.candidatePath("/security-streams/" + model.NewID())
	for _, live := range []bool{true, false} {
		request := f.candidateRequest(t, http.MethodGet, path)
		if !live {
			request.Header.Del(candidateAttemptCredentialHeader)
			request.Header.Del(candidateAttemptConnectionHeader)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		require.Equal(t, 200, response.Code, "response: %d %s", response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "response: %d %s", response.Code, response.Body.String())
		query := f.deliveryStore.nativeStatuses[len(f.deliveryStore.nativeStatuses)-1].Access
		if live {
			require.Equal(t, model.HashToken(f.credential), query.ContinuityCredentialHash, "live fences not forwarded")
			require.Equal(t, f.connection.ID, query.ConnectionID, "live fences not forwarded")
		}
		if !live {
			require.Empty(t, query.ContinuityCredentialHash, "historical request invented live fences")
			require.Empty(t, query.ConnectionID, "historical request invented live fences")
		}
		require.NotContains(t, response.Body.String(), f.credential, "response exposed continuity credential")
	}
	before := f.deliveryStore.requests()
	request := f.candidateRequest(t, http.MethodGet, path)
	request.Header.Add(candidateAttemptCredentialHeader, f.credential)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, 400, response.Code, "duplicate fence reached the Store")
	require.Equal(t, before, f.deliveryStore.requests(), "duplicate fence reached the Store")
}

func TestNativeDeliverySummaryStrictBodyAndRetryDelay(t *testing.T) {
	f := newAttemptFixture(t)
	f.deliveryStore.summaryErr = store.NewErrConflict("native_delivery", "summary_rate_limited", nil)
	api := f.api(t, nativeDeliveryResource(f.service))
	path := f.candidatePath("/security-streams/" + model.NewID() + "/summary")
	for _, body := range []string{
		`{"summary_sequence":1,"unretained_record_count":0,"count_complete":false,"raw_content":"forbidden"}`,
		`{"summary_sequence":1,"unretained_record_count":0,"count_complete":false,"first_unretained_at":null}`,
	} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer candidate")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "summary-once")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		require.Equal(t, 400, response.Code, "invalid body reached the Store: %d %s", response.Code, response.Body.String())
		require.Equal(t, 0, f.deliveryStore.requests(), "invalid body reached the Store: %d %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(`{"summary_sequence":1,"unretained_record_count":0,"count_complete":false}`))
	request.Header.Set("Authorization", "Bearer candidate")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "summary-once")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, 429, response.Code, "summary rate response: %d %s", response.Code, response.Body.String())
	require.Equal(t, "1", response.Header().Get("Retry-After"), "summary rate response: %d %s", response.Code, response.Body.String())
	require.Len(t, f.deliveryStore.nativeSummaries, 1, "summary rate response: %d %s", response.Code, response.Body.String())
}

func TestNativeDeliveryAppendClosedBodyAndBounds(t *testing.T) {
	f := newAttemptFixture(t)
	api := f.api(t, nativeDeliveryResource(f.service))
	path := f.candidatePath("/security-batches")
	batch := model.NativeSecurityBatch{StreamID: model.NewID(), BatchSequence: 1, ParticipationID: f.participation.ID, Generation: 1, SecuritySessionID: model.NewID(), PolicyDigest: model.SHA256Fingerprint([]byte("synthetic-policy")), ApplicationReleaseID: "synthetic-release", MatrixID: "synthetic-matrix", Records: []model.NativeRecord{{Gap: &model.NativeSourceGap{Kind: "source_gap", Capability: "display", SourceInstanceID: model.NewID(), Reason: "source_loss", OccurredAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}}}}
	raw, err := json.Marshal(batch)
	require.NoError(t, err)
	for _, body := range [][]byte{raw, bytes.Replace(raw, []byte(`"kind":"source_gap"`), []byte(`"raw_inventory":"forbidden","kind":"source_gap"`), 1), append(bytes.Repeat([]byte(" "), 256*1024), raw...)} {
		before := len(f.deliveryStore.nativeAppends)
		request := f.candidateRequest(t, http.MethodPost, path)
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "native-append-http")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if bytes.Equal(body, raw) {
			require.Equal(t, 200, response.Code, "append: %d %s", response.Code, response.Body.String())
			require.Len(t, f.deliveryStore.nativeAppends, before+1, "append: %d %s", response.Code, response.Body.String())
			require.Equal(t, batch.StreamID, f.deliveryStore.nativeAppends[before].Access.StreamID, "append: %d %s", response.Code, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "append: %d %s", response.Code, response.Body.String())
			continue
		}
		want := http.StatusBadRequest
		if len(body) > 256*1024 {
			want = http.StatusRequestEntityTooLarge
		}
		require.Equal(t, want, response.Code, "invalid append reached the Store: %d %s", response.Code, response.Body.String())
		require.Len(t, f.deliveryStore.nativeAppends, before, "invalid append reached the Store: %d %s", response.Code, response.Body.String())
	}
}

// TestDeliveryRecoveryCodesAgreeWithTheErrorCatalog holds the routing kernel's
// catalog-driven delivery member to the Attempt module's list: the kernel
// presents recovery state for exactly the codes the application attaches it
// to, which the WebSocket transport also reads.
func TestDeliveryRecoveryCodesAgreeWithTheErrorCatalog(t *testing.T) {
	source, err := os.ReadFile("route/error_catalog.yaml")
	require.NoError(t, err)
	catalog, err := errorcatalog.Parse(source)
	require.NoError(t, err)
	var catalogued, supported []string
	for _, entry := range catalog.Entries {
		if entry.Extension == "delivery" {
			catalogued = append(catalogued, entry.Code)
		}
		if attempt.SupportsDeliveryRecovery(entry.Code) {
			supported = append(supported, entry.Code)
		}
	}
	require.NotEmpty(t, supported)
	require.Equal(t, supported, catalogued)
}
