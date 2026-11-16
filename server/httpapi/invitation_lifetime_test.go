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
// replacement body carries expires_in_days into the Invitation the Store
// receives; absent and null select the default, and the application refuses
// a day count outside the bounds before anything is stored.
func TestInvitationRoutesCarryTheChosenLifetime(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	routes := map[string]struct {
		target func(*testing.T, *onboardingFixture) (path, members string)
		// lifetime is how long the stored Invitation's claim lives.
		lifetime func(*onboardingFixture) (time.Duration, bool)
		// fallback is the lifetime absent and null select.
		fallback time.Duration
	}{
		"student class": {
			target: func(_ *testing.T, f *onboardingFixture) (string, string) {
				return "/api/v1/classes/" + f.addClass("Biology 101").ID.String() + "/invitations/student", `"email":"student@example.edu"`
			},
			lifetime: func(f *onboardingFixture) (time.Duration, bool) {
				if len(f.invitations.studentIssues) != 1 {
					return 0, false
				}
				issued := f.invitations.studentIssues[0].Invitation
				return issued.ExpiresAt.Sub(issued.CreatedAt), true
			},
			fallback: model.InvitationLifetime,
		},
		"teacher academic unit": {
			target: func(_ *testing.T, f *onboardingFixture) (string, string) {
				return "/api/v1/academic-units/" + f.unit.ID.String() + "/invitations/teacher",
					`"email":"teacher@example.edu","role_id":"` + f.addDelegableRole().ID.String() + `"`
			},
			lifetime: func(f *onboardingFixture) (time.Duration, bool) {
				if len(f.invitations.teacherIssues) != 1 {
					return 0, false
				}
				issued := f.invitations.teacherIssues[0].Invitation
				return issued.ExpiresAt.Sub(issued.CreatedAt), true
			},
			fallback: model.InvitationLifetime,
		},
		"academic unit role": {
			target: func(_ *testing.T, f *onboardingFixture) (string, string) {
				return "/api/v1/academic-units/" + f.unit.ID.String() + "/invitations/role",
					`"email":"reviewer@example.edu","role_id":"` + f.addDelegableRole().ID.String() + `"`
			},
			lifetime: scopedRoleIssueLifetime,
			fallback: model.InvitationLifetime,
		},
		"institution role": {
			target: func(_ *testing.T, f *onboardingFixture) (string, string) {
				return "/api/v1/institutions/" + f.access.Institution.ID.String() + "/invitations/role",
					`"email":"admin@example.edu","role_id":"` + f.addDelegableRole().ID.String() + `"`
			},
			lifetime: scopedRoleIssueLifetime,
			fallback: model.InvitationLifetime,
		},
		"replacement": {
			target: func(t *testing.T, f *onboardingFixture) (string, string) {
				class := f.addClass("Biology 101")
				// The replaced Invitation lives ten days, the lifetime a
				// replacement keeps by default.
				current, _ := f.pendingStudentInvitation(t, class, "old@example.edu")
				current.ExpiresAt = current.CreatedAt.Add(10 * day)
				return "/api/v1/invitations/" + current.ID.String() + "/replacement",
					`"expected_revision":2,"purpose":"student_class","email":"new@example.edu","class_id":"` + class.ID.String() + `"`
			},
			lifetime: func(f *onboardingFixture) (time.Duration, bool) {
				if len(f.invitations.replacements) != 1 {
					return 0, false
				}
				replacement := f.invitations.replacements[0]
				require.Equal(t, replacement.Lifetime, replacement.Replacement.ExpiresAt.Sub(replacement.Replacement.CreatedAt))
				return replacement.Lifetime, true
			},
			fallback: 10 * day,
		},
	}
	for name, route := range routes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for lifetime, test := range map[string]struct {
				member string
				code   int
				want   time.Duration
			}{
				"absent":       {code: http.StatusCreated},
				"null":         {member: `,"expires_in_days":null`, code: http.StatusCreated},
				"twelve":       {member: `,"expires_in_days":12`, code: http.StatusCreated, want: 12 * day},
				"out of range": {member: `,"expires_in_days":31`, code: http.StatusBadRequest},
				"fractional":   {member: `,"expires_in_days":1.5`, code: http.StatusBadRequest},
				"text":         {member: `,"expires_in_days":"7"`, code: http.StatusBadRequest},
			} {
				t.Run(lifetime, func(t *testing.T) {
					fixture := newOnboardingFixture(t)
					path, members := route.target(t, fixture)
					response := serveInvitationLifetimeRequest(t, fixture, path, "{"+members+test.member+"}")
					require.Equal(t, test.code, response.Code, response.Body.String())
					stored, reached := route.lifetime(fixture)
					if test.code != http.StatusCreated {
						require.False(t, reached, "a refused lifetime reached the Store")
						return
					}
					want := test.want
					if want == 0 {
						want = route.fallback
					}
					require.True(t, reached, "the Invitation never reached the Store")
					require.Equal(t, want, stored)
				})
			}
		})
	}
}

// scopedRoleIssueLifetime is the lifetime of the one scoped Role Invitation
// the Store received.
func scopedRoleIssueLifetime(f *onboardingFixture) (time.Duration, bool) {
	if len(f.invitations.scopedRoleIssues) != 1 {
		return 0, false
	}
	issue := f.invitations.scopedRoleIssues[0]
	return issue.Invitation.ExpiresAt.Sub(issue.Invitation.CreatedAt), true
}

func serveInvitationLifetimeRequest(t *testing.T, fixture *onboardingFixture, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	httpAPI := invitationAPI(t, fixture, fixture.principal(model.AuthenticationMultiFactor))
	request := newJSONRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer session")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	return response
}
