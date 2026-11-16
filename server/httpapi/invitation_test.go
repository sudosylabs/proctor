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
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

// invitationAPI serves the Invitation resource over the fixture's real
// service, authenticating every request as principal.
func invitationAPI(t *testing.T, fixture *onboardingFixture, principal model.Principal) http.Handler {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	return newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, invitationResource(fixture.service))
}

func TestInvitationAdministrationRejectsNoProgressPage(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	// The Store reports a further page but returned no Invitation.
	fixture.invitations.more = true
	httpAPI := invitationAPI(t, fixture, fixture.principal(model.AuthenticationMultiFactor))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/invitations", nil)
	request.Header.Set("Authorization", "Bearer session")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code, "no-progress status=%d body=%s", response.Code, response.Body.String())
	require.Len(t, fixture.invitations.listOptions, 1, "the empty page was read once")
}

func TestInvitationAdministrationHTTPIsBoundedSafeAndRevisionFenced(t *testing.T) {
	fixture := newOnboardingFixture(t)
	class := fixture.addClass("Biology 101")
	invitation, _ := fixture.pendingStudentInvitation(t, class, "student@example.edu")
	fixture.invitations.listed, fixture.invitations.more = []*model.Invitation{invitation}, true
	httpAPI := invitationAPI(t, fixture, fixture.principal(model.AuthenticationMultiFactor))

	list := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/invitations?purpose=student_class&state=pending&email=student%40example.edu&limit=25&created_after=2023-11-14T22:13:20Z&created_before=2030-03-17T17:46:40Z", nil)
	list.Header.Set("Authorization", "Bearer session")
	listResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(listResponse, list)
	options := fixture.invitations.lastList(t)
	require.Equal(t, http.StatusOK, listResponse.Code, "list Invitations = %d %s options=%#v", listResponse.Code, listResponse.Body.String(), options)
	require.Equal(t, "no-store", listResponse.Header().Get("Cache-Control"))
	require.Equal(t, model.InvitationPurposeStudentClass, options.Purpose, "list options=%#v", options)
	require.Equal(t, model.InvitationPending, options.State, "list options=%#v", options)
	require.Equal(t, "student@example.edu", options.TargetEmail, "list options=%#v", options)
	require.Equal(t, 25, options.Limit, "list options=%#v", options)
	require.False(t, options.CreatedAfter.IsZero(), "list options=%#v", options)
	require.False(t, options.CreatedBefore.IsZero(), "list options=%#v", options)
	require.True(t, options.Visibility.InstitutionWide, "the caller's Institution-wide invitation.view reached the Store")
	var listed map[string]any
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &listed))
	require.NotEmpty(t, listed["next_cursor"])
	for _, forbidden := range []string{"claim", "claim_hash", "encrypted_payload", "rendered", "provider", "message_id", "job_id", "failure_detail"} {
		require.NotContains(t, listResponse.Body.String(), forbidden, "Invitation list leaked")
	}
	cursor, ok := listed["next_cursor"].(string)
	require.True(t, ok)
	require.NotEmpty(t, cursor)
	decodedCursor, err := decodeInvitationCursor(cursor)
	require.NoError(t, err)
	secondPage := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/invitations?limit=25&cursor="+url.QueryEscape(cursor), nil)
	secondPage.Header.Set("Authorization", "Bearer session")
	secondPageResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondPageResponse, secondPage)
	options = fixture.invitations.lastList(t)
	require.Equal(t, http.StatusOK, secondPageResponse.Code, "second Invitation page = %d %s options=%#v", secondPageResponse.Code, secondPageResponse.Body.String(), options)
	require.Equal(t, decodedCursor.CreatedAt, options.BeforeCreatedAt.Format(time.RFC3339Nano), "second page options=%#v", options)
	require.Equal(t, decodedCursor.ID, options.BeforeID.String(), "second page options=%#v", options)
	require.Equal(t, invitation.ID, options.BeforeID, "the cursor continues after the listed Invitation")

	id := invitation.ID.String()
	detail := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/invitations/"+id, nil)
	detail.Header.Set("Authorization", "Bearer session")
	detailResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(detailResponse, detail)
	require.Equal(t, http.StatusOK, detailResponse.Code, "Invitation detail = %d %s", detailResponse.Code, detailResponse.Body.String())
	require.Equal(t, "no-store", detailResponse.Header().Get("Cache-Control"))
	require.Equal(t, []model.InvitationID{invitation.ID}, fixture.invitations.administrationReads, "Invitation detail read")

	replacementClass := fixture.addClass("Biology 102")
	for _, mutation := range []struct {
		path string
		body map[string]any
		code int
	}{
		{path: "/api/v1/invitations/" + id + "/resend", body: map[string]any{"expected_revision": 7}, code: http.StatusOK},
		{path: "/api/v1/invitations/" + id + "/revoke", body: map[string]any{"expected_revision": 8}, code: http.StatusOK},
		{path: "/api/v1/invitations/" + id + "/replacement", body: map[string]any{"expected_revision": 9, "purpose": "student_class", "email": "replacement@example.edu", "class_id": replacementClass.ID.String()}, code: http.StatusCreated},
	} {
		t.Run(mutation.path, func(t *testing.T) {
			body, _ := json.Marshal(mutation.body)
			request := newJSONRequest(http.MethodPost, mutation.path, bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer session")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, mutation.code, response.Code, "%s = %d %s", mutation.path, response.Code, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "%s = %d %s", mutation.path, response.Code, response.Body.String())
		})
	}
	invitations := fixture.invitations
	require.Len(t, invitations.resends, 1, "resend reached the Store")
	require.Equal(t, invitation.ID, invitations.resends[0].ID)
	require.Equal(t, int64(7), invitations.resends[0].ExpectedRevision)
	require.NotEqual(t, invitation.ClaimHash, invitations.resends[0].ClaimHash, "a resend rotates the claim")
	require.Len(t, invitations.revocations, 1, "revoke reached the Store")
	require.Equal(t, invitation.ID, invitations.revocations[0].ID)
	require.Equal(t, int64(8), invitations.revocations[0].ExpectedRevision)
	require.Len(t, invitations.replacements, 1, "replacement reached the Store")
	require.Equal(t, invitation.ID, invitations.replacements[0].CurrentID)
	require.Equal(t, int64(9), invitations.replacements[0].ExpectedRevision)
	require.Equal(t, "replacement@example.edu", invitations.replacements[0].Replacement.TargetEmail)
	require.Equal(t, replacementClass.ID, invitations.replacements[0].Replacement.ClassID)
}

func TestInvitationBatchHTTPIsStrictBoundedAndIdempotent(t *testing.T) {
	fixture := newOnboardingFixture(t)
	class := fixture.addClass("Biology 101")
	// The second row was already committed under its key and the third
	// collides with a pending Invitation, so the batch reports one of each.
	fixture.invitations.replayEmails["second@example.edu"] = true
	fixture.invitations.conflictEmails["third@example.edu"] = true
	httpAPI := invitationAPI(t, fixture, fixture.principal(model.AuthenticationMultiFactor))
	classID := class.ID.String()
	body, _ := json.Marshal(map[string]any{"operation": "student_class.create", "scope_type": "class", "scope_id": classID,
		"items": []map[string]any{{"key": "row-1", "email": "first@example.edu"}, {"key": "row-2", "email": "second@example.edu"}, {"key": "row-3", "email": "third@example.edu"}}})
	request := newJSONRequest(http.MethodPost, "/api/v1/invitation-batches", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer session")
	request.Header.Set("Idempotency-Key", "batch-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	issues := fixture.invitations.studentIssues
	require.Equal(t, http.StatusOK, response.Code, "Invitation batch = %d %s", response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Len(t, issues, 3, "every row reached the Store")
	for index, row := range []struct{ key, email string }{{"row-1", "first@example.edu"}, {"row-2", "second@example.edu"}, {"row-3", "third@example.edu"}} {
		require.Equal(t, class.ID, issues[index].Invitation.ClassID, "row %d is issued in the batch's Class", index)
		require.Equal(t, model.InvitationPurposeStudentClass, issues[index].Invitation.Purpose, "row %d is a student_class.create", index)
		require.Equal(t, row.email, issues[index].Invitation.TargetEmail, "row %d", index)
		command := fixture.invitations.studentIssueCommands[index]
		require.Equal(t, sha256.Sum256([]byte("10:batch-once:"+row.key)), command.KeyDigest, "row %d is keyed under the batch's Idempotency-Key", index)
	}
	require.Contains(t, response.Body.String(), `"succeeded":1`)
	require.Contains(t, response.Body.String(), `"no_op":1`)
	require.Contains(t, response.Body.String(), `"error_code":"invitation.conflict"`)
	require.NotContains(t, response.Body.String(), "@example.edu")

	missingKey := newJSONRequest(http.MethodPost, "/api/v1/invitation-batches", bytes.NewReader(body))
	missingKey.Header.Set("Authorization", "Bearer session")
	missingResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(missingResponse, missingKey)
	require.Equal(t, http.StatusBadRequest, missingResponse.Code, "Invitation batch missing key")
	require.Contains(t, missingResponse.Body.String(), "idempotency.key_required", "Invitation batch missing key")

	unknownBody := []byte(`{"operation":"revoke","scope_type":"class","scope_id":"` + classID + `","items":[],"command":"arbitrary"}`)
	unknown := newJSONRequest(http.MethodPost, "/api/v1/invitation-batches", bytes.NewReader(unknownBody))
	unknown.Header.Set("Authorization", "Bearer session")
	unknown.Header.Set("Idempotency-Key", "unknown-field")
	unknownResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(unknownResponse, unknown)
	require.Equal(t, http.StatusBadRequest, unknownResponse.Code, "Invitation batch unknown JSON = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	require.Len(t, fixture.invitations.studentIssues, 3, "a refused batch reached the Store")
}

func TestInvitationAdministrationHTTPRejectsMalformedCursor(t *testing.T) {
	fixture := newOnboardingFixture(t)
	principal := fixture.principal(model.AuthenticationSingleFactor)
	principal.ClientType = model.SessionClientCLI
	logger, logs := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, invitationResource(fixture.service))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/invitations?cursor=not-a-cursor", nil)
	request.Header.Set("Authorization", "Bearer pat")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "malformed Invitation cursor = %d %s logs=%s", response.Code, response.Body.String(), logs.String())
	require.Contains(t, response.Body.String(), "request.invalid", "malformed Invitation cursor = %d %s logs=%s", response.Code, response.Body.String(), logs.String())
	require.Empty(t, fixture.invitations.listOptions, "a malformed cursor reached the Store")
}

func TestInvitationAcceptanceHTTPReturnsOnlyRecordIDsForFreshAndReplay(t *testing.T) {
	for _, replayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "replay"}[replayed], func(t *testing.T) {
			fixture := newOnboardingFixture(t)
			invitation, claim := fixture.pendingStudentInvitation(t, fixture.addClass("Biology 101"), "student@example.edu")
			fixture.invitations.replayAcceptance = replayed
			logger, _ := routetest.NewLogger(t)
			httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, invitationResource(fixture.service))
			body, _ := json.Marshal(map[string]string{"claim": claim, "password": "correct horse battery staple", "username": "student"})
			request := newJSONRequest(http.MethodPost, "/api/v1/invitations/student-class/accept", bytes.NewReader(body))
			response := httptest.NewRecorder()

			httpAPI.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code, "accept invitation = %d: %s", response.Code, response.Body.String())
			require.Len(t, fixture.invitations.studentAcceptances, 1, "the acceptance reached the Store")
			accepted := fixture.invitations.studentAcceptances[0]
			require.Equal(t, invitation.ClaimHash, accepted.ClaimHash)
			require.Equal(t, "student", accepted.User.Username)
			require.Equal(t, "student@example.edu", accepted.User.Email, "the account takes the Invitation's mailbox")
			var projection map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &projection))
			expected := map[string]any{
				"user_id":         accepted.User.ID.String(),
				"invitation_id":   invitation.ID.String(),
				"affiliation_id":  accepted.Affiliation.ID.String(),
				"class_member_id": accepted.ClassMember.ID.String(),
				"replayed":        replayed,
			}
			require.Equal(t, expected, projection, "acceptance projection")
		})
	}
}

func TestInvitationAcceptanceOpenAPIExposesOnlyRecordIDs(t *testing.T) {
	document := readOpenAPIDocument(t)
	schema := document.Components.Schemas["InvitationAcceptanceResponse"]
	wantFields := []string{"academic_unit_member_id", "affiliation_id", "class_member_id", "invitation_id", "replayed", "role_binding_id", "user_id"}
	wantRequired := []string{"invitation_id", "replayed", "user_id"}
	gotFields := make([]string, 0, len(schema.Properties))
	for field := range schema.Properties {
		gotFields = append(gotFields, field)
	}
	sort.Strings(gotFields)
	sort.Strings(schema.Required)
	require.Equal(t, wantFields, gotFields, "InvitationAcceptanceResponse fields")
	require.Equal(t, wantRequired, schema.Required, "InvitationAcceptanceResponse fields")
}

func TestInvitationHTTPKeepsClaimOutOfIssueResponseAndAcceptsPublicly(t *testing.T) {
	fixture := newOnboardingFixture(t)
	principal := fixture.principal(model.AuthenticationSingleFactor)
	principal.ClientType = model.SessionClientCLI
	httpAPI := invitationAPI(t, fixture, principal)
	class := fixture.addClass("Biology 101")
	body, _ := json.Marshal(map[string]any{"email": "student@example.edu", "start_at": "2027-01-15T08:00:00Z",
		"end_at": "2027-05-11T01:46:40.5Z", "suggested_username": "student-one"})
	issue := newJSONRequest(http.MethodPost, "/api/v1/classes/"+class.ID.String()+"/invitations/student", bytes.NewReader(body))
	issue.Header.Set("Authorization", "Bearer access")
	issueResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(issueResponse, issue)
	require.Equal(t, http.StatusCreated, issueResponse.Code, "issue response=%d %s", issueResponse.Code, issueResponse.Body.String())
	require.NotContains(t, issueResponse.Body.String(), "student@example.edu")
	require.NotContains(t, issueResponse.Body.String(), "claim")
	require.Len(t, fixture.invitations.studentIssues, 1, "the issue reached the Store")
	issued := fixture.invitations.studentIssues[0].Invitation
	require.Equal(t, class.ID, issued.ClassID)
	require.Equal(t, int64(1_800_000_000_000), issued.IntendedStartsAt.UnixMilli())
	require.Equal(t, int64(1_810_000_000_500), issued.IntendedEndsAt.Time.UnixMilli())
	require.Equal(t, "student-one", issued.Suggestions.Username)
	require.Equal(t, 1, fixture.renders.Count(), "the claim was mailed")
	require.Equal(t, model.MailTemplateAccessStudentClassInvitation, fixture.invitations.studentIssues[0].Delivery.TemplateKey, "the claim was mailed")

	invitation, raw := fixture.pendingStudentInvitation(t, class, "pending@example.edu")
	acceptBody, _ := json.Marshal(map[string]string{"claim": raw, "password": "correct horse battery staple", "username": "student-one"})
	accept := newJSONRequest(http.MethodPost, "/api/v1/invitations/student-class/accept", bytes.NewReader(acceptBody))
	acceptResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(acceptResponse, accept)
	require.Equal(t, http.StatusOK, acceptResponse.Code, "accept response=%d %s", acceptResponse.Code, acceptResponse.Body.String())
	require.Len(t, fixture.invitations.studentAcceptances, 1, "the acceptance reached the Store")
	require.Equal(t, invitation.ClaimHash, fixture.invitations.studentAcceptances[0].ClaimHash, "the raw claim selected its Invitation")
	require.NotContains(t, acceptResponse.Body.String(), raw)
}

func TestTeacherInvitationHTTPFreezesRoleAndReturnsRelationshipIDs(t *testing.T) {
	fixture := newOnboardingFixture(t)
	principal := fixture.principal(model.AuthenticationSingleFactor)
	principal.ClientType = model.SessionClientCLI
	httpAPI := invitationAPI(t, fixture, principal)
	role := fixture.addDelegableRole()
	body, _ := json.Marshal(map[string]string{"email": "teacher@example.edu", "role_id": role.ID.String()})
	request := newJSONRequest(http.MethodPost, "/api/v1/academic-units/"+fixture.unit.ID.String()+"/invitations/teacher", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer access")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "teacher issue response=%d %s", response.Code, response.Body.String())
	require.Len(t, fixture.invitations.teacherIssues, 1, "the teacher issue reached the Store")
	issued := fixture.invitations.teacherIssues[0].Invitation
	require.Equal(t, fixture.unit.ID, issued.AcademicUnitID)
	require.Equal(t, role.ID, issued.RoleID)
	require.Equal(t, role.Permissions, issued.RoleActions, "the Role's Actions are frozen into the Invitation")
	require.Contains(t, response.Body.String(), string(model.ActionClassView), "the response names the frozen Actions")
	require.NotContains(t, response.Body.String(), "teacher@example.edu")
	require.NotContains(t, response.Body.String(), "claim")

	invitation, raw := fixture.pendingTeacherInvitation(t, role, "pending.teacher@example.edu")
	acceptBody, _ := json.Marshal(map[string]string{"claim": raw, "password": "correct horse battery staple", "username": "teacher-one"})
	accept := newJSONRequest(http.MethodPost, "/api/v1/invitations/teacher-academic-unit/accept", bytes.NewReader(acceptBody))
	acceptResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(acceptResponse, accept)
	require.Equal(t, http.StatusOK, acceptResponse.Code, "teacher accept response=%d %s", acceptResponse.Code, acceptResponse.Body.String())
	require.Len(t, fixture.invitations.teacherAcceptances, 1, "the teacher acceptance reached the Store")
	accepted := fixture.invitations.teacherAcceptances[0]
	require.Equal(t, invitation.ClaimHash, accepted.ClaimHash, "the raw claim selected its Invitation")
	require.Equal(t, "teacher-one", accepted.User.Username)
	require.Contains(t, acceptResponse.Body.String(), accepted.AcademicUnitMember.ID.String())
	require.Contains(t, acceptResponse.Body.String(), accepted.RoleBinding.ID.String())
	require.NotContains(t, acceptResponse.Body.String(), raw)
}

func TestScopedRoleInvitationHTTPRequiresAuthenticatedExistingUserAndInstitutionAssurance(t *testing.T) {
	fixture := newOnboardingFixture(t)
	principal := fixture.principal(model.AuthenticationMultiFactor)
	httpAPI := invitationAPI(t, fixture, principal)
	role := fixture.addDelegableRole()
	institutionID := fixture.access.Institution.ID
	body, _ := json.Marshal(map[string]string{"email": "existing@example.edu", "role_id": role.ID.String()})
	for _, target := range []struct {
		path    string
		purpose model.InvitationPurpose
		scope   string
	}{
		{path: "/api/v1/academic-units/" + fixture.unit.ID.String() + "/invitations/role", purpose: model.InvitationPurposeAcademicUnitRole, scope: fixture.unit.ID.String()},
		{path: "/api/v1/institutions/" + institutionID.String() + "/invitations/role", purpose: model.InvitationPurposeInstitutionRole, scope: institutionID.String()},
	} {
		t.Run(target.path, func(t *testing.T) {
			before := len(fixture.invitations.scopedRoleIssues)
			request := newJSONRequest(http.MethodPost, target.path, bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer session")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusCreated, response.Code, "scoped Role issue %s = %d %s", target.path, response.Code, response.Body.String())
			require.Len(t, fixture.invitations.scopedRoleIssues, before+1, "the scoped Role issue reached the Store")
			issued := fixture.invitations.scopedRoleIssues[before].Invitation
			require.Equal(t, target.purpose, issued.Purpose)
			require.Equal(t, target.scope, issued.ScopeID)
			require.Equal(t, role.ID, issued.RoleID)
			require.NotContains(t, response.Body.String(), "existing@example.edu")
		})
	}
	for _, target := range []struct {
		path    string
		purpose model.InvitationPurpose
	}{
		{path: "/api/v1/invitations/academic-unit-role/accept", purpose: model.InvitationPurposeAcademicUnitRole},
		{path: "/api/v1/invitations/institution-role/accept", purpose: model.InvitationPurposeInstitutionRole},
	} {
		t.Run(target.path, func(t *testing.T) {
			// Each route accepts only its own purpose, so each gets a claim of it.
			invitation, claim := fixture.pendingRoleInvitation(t, target.purpose, role)
			before := len(fixture.invitations.scopedAcceptances)
			request := newJSONRequest(http.MethodPost, target.path, bytes.NewReader([]byte(`{"claim":"`+claim+`"}`)))
			request.Header.Set("Authorization", "Bearer session")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, "scoped Role accept %s = %d %s", target.path, response.Code, response.Body.String())
			require.Len(t, fixture.invitations.scopedAcceptances, before+1, "the scoped Role acceptance reached the Store")
			accepted := fixture.invitations.scopedAcceptances[before]
			require.Equal(t, invitation.ClaimHash, accepted.ClaimHash, "the raw claim selected its Invitation")
			require.Equal(t, principal.UserID, accepted.UserID, "the Role binds the Session's own User")
			require.NotContains(t, response.Body.String(), claim)
			require.NotContains(t, response.Body.String(), "email")
		})
	}
	pat := principal
	pat.CredentialType, pat.SessionID = model.CredentialPersonalAccessToken, ""
	pat.CredentialID = model.PrincipalCredentialID(model.NewPersonalAccessTokenID())
	patAPI := invitationAPI(t, fixture, pat)
	issued := len(fixture.invitations.scopedRoleIssues)
	request := newJSONRequest(http.MethodPost, "/api/v1/institutions/"+institutionID.String()+"/invitations/role", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer pat")
	response := httptest.NewRecorder()
	patAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, "institution Role PAT issue = %d %s", response.Code, response.Body.String())
	require.Len(t, fixture.invitations.scopedRoleIssues, issued, "a PAT issue reached the Store")
}
