// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAcademicAdministrationBatchHTTPIsStrictBoundedAndSafe(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	fixture := newOnboardingFixture(t)
	source, destination := fixture.addClass("Biology 101"), fixture.addClass("Biology 102")
	// The first User transfers, the second's transfer was already committed
	// and the third has no membership to transfer from.
	transferring, replayed, unenrolled := fixture.addUser("first"), fixture.addUser("second"), fixture.addUser("third")
	previous := map[model.UserID]*model.ClassMember{}
	for _, account := range []*model.User{transferring, replayed} {
		member := &model.ClassMember{ID: model.NewClassMemberID(), ClassID: source.ID, AcademicPeriodID: source.AcademicPeriodID,
			UserID: account.ID, StartsAt: fixture.period.StartsAt, Revision: 1}
		fixture.classMembers.add(member)
		previous[account.ID] = member
	}
	fixture.classMembers.replayUsers[replayed.ID] = true
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: fixture.principal(model.AuthenticationMultiFactor)},
		academicAdministrationBatchResource(fixture.service))
	body, _ := json.Marshal(map[string]any{"operation": "class.transfer", "scope_type": "class", "scope_id": destination.ID.String(),
		"items": []map[string]any{
			{"key": "row-1", "user_id": transferring.ID.String(), "relationship_id": previous[transferring.ID].ID.String()},
			{"key": "row-2", "user_id": replayed.ID.String(), "relationship_id": previous[replayed.ID].ID.String()},
			{"key": "row-3", "user_id": unenrolled.ID.String(), "relationship_id": model.NewClassMemberID().String()},
		}})
	request := newJSONRequest(http.MethodPost, "/api/v1/academic-administration-batches", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer session")
	request.Header.Set("Idempotency-Key", "batch-key")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	enrollments := fixture.classMembers.enrollments
	require.Equal(t, http.StatusOK, response.Code, "academic administration batch = %d %s", response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Len(t, enrollments, 2, "the two transferable rows reached the Store: %s", response.Body.String())
	for index, account := range []*model.User{transferring, replayed} {
		require.Equal(t, destination.ID, enrollments[index].Member.ClassID, "row %d enrolls into the batch's Class", index)
		require.Equal(t, account.ID, enrollments[index].Member.UserID, "row %d", index)
		require.Equal(t, previous[account.ID].ID, enrollments[index].ExpectedPreviousID, "row %d transfers from its relationship", index)
		require.Equal(t, sha256.Sum256([]byte("9:batch-key:row-"+strconv.Itoa(index+1))), enrollments[index].Command.KeyDigest,
			"row %d is keyed under the batch's Idempotency-Key", index)
	}
	require.Contains(t, response.Body.String(), `"succeeded":1`)
	require.Contains(t, response.Body.String(), `"no_op":1`)
	require.Contains(t, response.Body.String(), `"class.enrollment_conflict"`)
	for _, account := range []*model.User{transferring, replayed, unenrolled} {
		require.NotContains(t, response.Body.String(), account.ID.String())
	}

	unknown := newJSONRequest(http.MethodPost, "/api/v1/academic-administration-batches", strings.NewReader(`{"operation":"class.end","scope_type":"class","scope_id":"`+destination.ID.String()+`","items":[],"command":"forbidden"}`))
	unknown.Header.Set("Authorization", "Bearer session")
	unknown.Header.Set("Idempotency-Key", "unknown")
	unknownResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(unknownResponse, unknown)
	require.Equal(t, http.StatusBadRequest, unknownResponse.Code, "unknown command envelope = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	require.Empty(t, fixture.classMembers.ends, "a refused envelope reached the Store")
}
