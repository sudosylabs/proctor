// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// TestInvitationRoutesCarryTheChosenLifetime proves every create and
// replacement body passes expires_in_days to its command unchanged, so the
// application alone decides the bounds; absent and null select the default.
func TestInvitationRoutesCarryTheChosenLifetime(t *testing.T) {
	t.Parallel()
	roleID := model.NewRoleID().String()
	routes := map[string]struct {
		path    string
		members string
		days    func(*invitationHTTPApplication) *int
	}{
		"student class": {path: "/api/v1/classes/" + model.NewClassID().String() + "/invitations/student",
			members: `"email":"student@example.edu"`, days: func(a *invitationHTTPApplication) *int { return a.issue.ExpiresInDays }},
		"teacher academic unit": {path: "/api/v1/academic-units/" + model.NewAcademicUnitID().String() + "/invitations/teacher",
			members: `"email":"teacher@example.edu","role_id":"` + roleID + `"`, days: func(a *invitationHTTPApplication) *int { return a.teacherIssue.ExpiresInDays }},
		"academic unit role": {path: "/api/v1/academic-units/" + model.NewAcademicUnitID().String() + "/invitations/role",
			members: `"email":"reviewer@example.edu","role_id":"` + roleID + `"`, days: func(a *invitationHTTPApplication) *int { return a.unitRoleIssue.ExpiresInDays }},
		"institution role": {path: "/api/v1/institutions/" + model.NewInstitutionID().String() + "/invitations/role",
			members: `"email":"admin@example.edu","role_id":"` + roleID + `"`, days: func(a *invitationHTTPApplication) *int { return a.institutionRoleIssue.ExpiresInDays }},
		"replacement": {path: "/api/v1/invitations/" + model.NewInvitationID().String() + "/replacement",
			members: `"expected_revision":2,"purpose":"student_class","email":"new@example.edu","class_id":"` + model.NewClassID().String() + `"`,
			days:    func(a *invitationHTTPApplication) *int { return a.replace.ExpiresInDays }},
	}
	for name, route := range routes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for lifetime, test := range map[string]struct {
				member string
				code   int
				want   *int
			}{
				"absent":       {code: http.StatusCreated},
				"null":         {member: `,"expires_in_days":null`, code: http.StatusCreated},
				"twelve":       {member: `,"expires_in_days":12`, code: http.StatusCreated, want: invitationLifetimeDays(12)},
				"out of range": {member: `,"expires_in_days":31`, code: http.StatusCreated, want: invitationLifetimeDays(31)},
				"fractional":   {member: `,"expires_in_days":1.5`, code: http.StatusBadRequest},
				"text":         {member: `,"expires_in_days":"7"`, code: http.StatusBadRequest},
			} {
				t.Run(lifetime, func(t *testing.T) {
					applicationFake := &invitationHTTPApplication{}
					response := serveInvitationLifetimeRequest(t, applicationFake, route.path, "{"+route.members+test.member+"}")
					require.Equal(t, test.code, response.Code, response.Body.String())
					if test.code == http.StatusCreated {
						require.Equal(t, test.want, route.days(applicationFake))
					}
				})
			}
		})
	}
}

func invitationLifetimeDays(days int) *int { return &days }

func serveInvitationLifetimeRequest(t *testing.T, applicationFake *invitationHTTPApplication, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now(), MFACompletedAt: model.OptionalTimeFrom(time.Now())}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, invitationResource(applicationFake))
	request := newJSONRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer session")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	return response
}
