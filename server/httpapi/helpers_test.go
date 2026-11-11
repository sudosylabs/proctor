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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/search"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

// testHTTPApplication authenticates every request as its principal.
type testHTTPApplication struct {
	principal *model.Principal
}

func (a *testHTTPApplication) AuthenticateAccess(context.Context, string) (*model.Principal, error) {
	return a.principal, nil
}

func (a *testHTTPApplication) AuthenticateBearer(context.Context, string) (*model.Principal, error) {
	return a.principal, nil
}

type testHTTPHealth struct{}

func (testHTTPHealth) Live() bool  { return true }
func (testHTTPHealth) Ready() bool { return true }

type testWebSocketTransport struct{}

func (testWebSocketTransport) Accept(http.ResponseWriter, *http.Request, model.Principal, model.RequestMetadata, string, int64, bool) error {
	return nil
}

func validHTTPSettings(t *testing.T) Settings {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	return Settings{
		Logger: logger, Localizer: routetest.NewLocalizer(t), Health: testHTTPHealth{},
		BuildInfo: BuildInfo{Version: "test"}, PublicURL: "http://localhost:8065",
		MaxBodyBytes: 1 << 20, RecentAuthenticationTTL: time.Minute, NodeID: "node-a",
		WebSocket: testWebSocketTransport{},
	}
}

// completeHTTPApplication names every service New builds a resource from,
// each with no collaborators. Resource constructors only declare handlers
// while the route catalog compiles, so a test may compile and inspect the
// production catalog over it but must never invoke one of its handlers.
func completeHTTPApplication() *app.App {
	return &app.App{
		Identity: &identity.Service{}, Onboarding: &onboarding.Service{},
		Administration: &administration.Service{}, Academic: &academic.Service{},
		Users: &user.Service{}, Search: &search.Service{}, Counts: &administration.Counts{},
		ExamAuthoring: &exam.Authoring{}, ExamPublication: &exam.Publication{},
		Sittings: &sitting.Service{}, Corrections: &correction.Service{},
		Resources: &resource.Service{}, StarterWorkspaces: &workspace.Service{},
		Attempts: &attempt.Service{}, Reviews: &review.Service{},
		ExamRecords: &exam.Records{}, ExamExports: &exam.Exports{},
	}
}

// newProductionCatalogAPI compiles the production route catalog through New.
func newProductionCatalogAPI(t *testing.T) *route.API {
	t.Helper()
	api, err := New(completeHTTPApplication(), validHTTPSettings(t))
	require.NoError(t, err)
	return api
}

// newResourceAPI compiles resources built over a test's fakes through the
// same constructor New uses, authenticating every request as principal.
func newResourceAPI(t *testing.T, principal model.Principal, resources ...route.Resource) *route.API {
	t.Helper()
	api, err := newAPI(validHTTPSettings(t), &testHTTPApplication{principal: &principal}, resources...)
	require.NoError(t, err)
	return api
}
