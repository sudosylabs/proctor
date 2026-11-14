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
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func liveTestPrincipal() model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now().UTC(),
	}
}

func newLiveTestAPI(t *testing.T, resources ...Resource) (*API, *LiveRoutes) {
	t.Helper()
	live, err := NewLiveRoutes("/plugins")
	require.NoError(t, err)
	api, err := New(Settings{
		Logger: routetest.NewDiscardLogger(t), Localizer: routetest.NewLocalizer(t), PublicURL: "http://localhost:8065",
		MaxBodyBytes: 1 << 20, RecentAuthenticationTTL: time.Minute, Live: live,
	}, routetest.Authenticator{Principal: liveTestPrincipal()}, resources...)
	require.NoError(t, err)
	return api, live
}

func liveGet(path string) Endpoint {
	return Endpoint{Method: http.MethodGet, Path: path, Auth: AuthSessionRequired, Handle: func(Request) (Result, error) {
		return JSON(http.StatusOK, map[string]string{"served": path}), nil
	}}
}

func serveLive(api *API, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	return recorder
}

func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem), recorder.Body.String())
	return problem.Code
}

func TestLiveRoutesAppearAndDisappearWhileTheAPIServes(t *testing.T) {
	t.Parallel()

	settings := NewResource("plugin-settings", liveGet("/plugins/{plugin_id:plugin}/settings"))
	api, live := newLiveTestAPI(t, settings)
	const terminal = "/api/v1/plugins/execution/exam-attempts/" + modelIDForRouteTest + "/terminal"

	require.Equal(t, http.StatusNotFound, serveLive(api, http.MethodGet, terminal).Code, "nothing is mounted yet")

	var available atomic.Bool
	available.Store(true)
	require.NoError(t, live.Replace(LiveGroup{
		Resource:  NewResource("plugin-execution", liveGet("/plugins/execution/exam-attempts/{exam_attempt_id}/terminal")),
		Available: available.Load,
	}))
	served := serveLive(api, http.MethodGet, terminal)
	require.Equal(t, http.StatusOK, served.Code, served.Body.String())
	require.Equal(t, http.StatusOK, serveLive(api, http.MethodGet, "/api/v1/plugins/execution/settings").Code,
		"catalog routes under the live prefix still serve")
	refused := serveLive(api, http.MethodPost, terminal)
	require.Equal(t, http.StatusMethodNotAllowed, refused.Code)
	require.Equal(t, http.MethodGet, refused.Header().Get("Allow"))
	require.Len(t, live.Routes(), 1)
	require.Equal(t, AuthSessionRequired, live.Routes()[0].Auth)

	available.Store(false)
	gone := serveLive(api, http.MethodGet, terminal)
	require.Equal(t, http.StatusNotFound, gone.Code, "an unavailable group answers 404 at once")
	require.Equal(t, routeNotFoundCode, problemCode(t, gone))
	require.Equal(t, http.StatusNotFound, serveLive(api, http.MethodPost, terminal).Code,
		"an unavailable group offers no methods either")

	available.Store(true)
	require.NoError(t, live.Replace())
	require.Equal(t, http.StatusNotFound, serveLive(api, http.MethodGet, terminal).Code, "a replaced set unmounts its routes")
	require.Empty(t, live.Routes())
}

func TestLiveRoutesAreValidatedLikeTheCatalog(t *testing.T) {
	t.Parallel()

	settings := NewResource("plugin-settings", liveGet("/plugins/{plugin_id:plugin}/settings"))
	_, live := newLiveTestAPI(t, settings)
	always := func() bool { return true }
	group := func(endpoints ...Endpoint) LiveGroup {
		return LiveGroup{Resource: NewResource("plugin-execution", endpoints...), Available: always}
	}
	good := liveGet("/plugins/execution/exam-attempts/{exam_attempt_id}/terminal")
	require.NoError(t, live.Replace(group(good)))

	outside := liveGet("/exam-attempts/{exam_attempt_id}/terminal")
	require.ErrorContains(t, live.Replace(group(outside)), "not under the live route prefix")
	shadowing := liveGet("/plugins/execution/settings")
	require.ErrorContains(t, live.Replace(group(shadowing)), "may match the same requests as the catalog route")
	noAuth := good
	noAuth.Auth = ""
	require.ErrorContains(t, live.Replace(group(noAuth)), "authentication requirement is invalid")
	require.Error(t, live.Replace(LiveGroup{Resource: NewResource("plugin-execution", good)}), "a group needs an availability check")
	require.Error(t, live.Replace(group(good), group(good)), "group names are unique")

	upgrade := Endpoint{Method: http.MethodGet, Path: "/plugins/execution/exam-attempts/{exam_attempt_id}/socket",
		Auth: AuthSessionRequired, ProtocolName: "plugin-socket", ProtocolKind: ProtocolUpgrade,
		Upgrade: func(http.ResponseWriter, Request) error { return nil }}
	require.NoError(t, live.Replace(group(good, upgrade)), "a live route may upgrade to its own protocol")
	publicUpgrade := upgrade
	publicUpgrade.Auth = AuthPublic
	require.ErrorContains(t, live.Replace(group(publicUpgrade)), "not a valid live upgrade")

	require.Len(t, live.Routes(), 2, "a refused Replace keeps the routes in place")

	_, err := New(Settings{
		Logger: routetest.NewDiscardLogger(t), Localizer: routetest.NewLocalizer(t), PublicURL: "http://localhost:8065",
		MaxBodyBytes: 1 << 20, RecentAuthenticationTTL: time.Minute, Live: live,
	}, routetest.Authenticator{})
	require.ErrorContains(t, err, "already bound", "a live set serves one API")
}

func TestShapesMayOverlapIsConservative(t *testing.T) {
	t.Parallel()

	require.True(t, shapesMayOverlap("/api/v1/plugins/execution/settings", "/api/v1/plugins/{plugin_id}/settings"))
	require.False(t, shapesMayOverlap("/api/v1/plugins/execution/exam-attempts/{canonical_id}/terminal", "/api/v1/plugins/{plugin_id}/settings"))
	require.False(t, shapesMayOverlap("/api/v1/plugins/execution/terminal", "/api/v1/plugins/{plugin_id}/settings"))
}
