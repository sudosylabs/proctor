//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

type academicAdministrationPreviewWire struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	PreviewDigest    string `json:"preview_digest"`
	Revision         int64  `json:"revision"`
	TotalRows        int    `json:"total_rows"`
	SucceededRows    int    `json:"succeeded_rows"`
	NoOpRows         int    `json:"no_op_rows"`
	FailedRows       int    `json:"failed_rows"`
	SkippedRows      int    `json:"skipped_rows"`
	ExecutionJobID   string `json:"execution_job_id"`
	FailureCode      string `json:"failure_code"`
	PreviewExpiresAt string `json:"preview_expires_at"`
	Summary          *struct {
		WillChange  int `json:"will_change"`
		NoOp        int `json:"no_op"`
		Skipped     int `json:"skipped"`
		SkipReasons []struct {
			Reason string `json:"reason"`
			Rows   int    `json:"rows"`
		} `json:"skip_reasons"`
	} `json:"summary"`
}

type academicAdministrationPreviewRowWire struct {
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	Group      string `json:"group"`
	Reason     string `json:"reason"`
	Status     string `json:"status"`
	PublicCode string `json:"public_code"`
}

// TestAcademicAdministrationPreviewIntegration runs bulk changes by filter
// end to end against PostgreSQL: the preview freezes the matching Users
// under the caller's visibility, skips the caller and System
// Administrators, commits once within its deadline against its exact digest
// and revision, executes only the frozen list with each row authorized
// again, and records one batch-level audit entry.
func TestAcademicAdministrationPreviewIntegration(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithConfig(func(cfg *config.Config) {
		cfg.Server.ListenAddress = "127.0.0.1:0"
		cfg.Authentication.MFA.Enabled = true
		cfg.Authentication.MFA.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	}))
	startIntegrationServer(t, helper)
	handler := helper.Handler()
	ctx := context.Background()
	password := "correct horse battery staple"

	bootstrap := performJSONRequest(handler, http.MethodPost, "/api/v1/bootstrap", map[string]any{
		"bootstrap_secret": testlib.BootstrapSecret,
		"institution":      map[string]any{"name": "northbridge", "display_name": "Northbridge University"},
		"administrator":    map[string]any{"username": "preview-owner", "email": "preview-owner@example.edu"},
		"password":         password,
	}, "")
	require.Equal(t, http.StatusCreated, bootstrap.Code, "bootstrap = %d: %s", bootstrap.Code, bootstrap.Body.String())
	var installation struct {
		Institution struct {
			ID string `json:"id"`
		} `json:"institution"`
		Administrator struct {
			ID string `json:"id"`
		} `json:"administrator"`
	}
	decodeIntegrationResponse(t, bootstrap, &installation)
	institutionID, ownerID := installation.Institution.ID, model.UserID(installation.Administrator.ID)
	owner := strongIntegrationSession(t, handler, "preview-owner", password, "preview-owner-cli")

	now := model.GetMillis()
	addAffiliation := func(userID model.UserID, kind model.AffiliationKind) {
		t.Helper()
		createIntegrationResource[map[string]any](t, handler, http.MethodPost, "/api/v1/users/"+userID.String()+"/affiliations",
			map[string]any{"kind": kind, "start_at": integrationWireTime(now - 60_000)}, owner)
	}
	alpha := createIntegrationUser(t, helper, "preview-alpha", password)
	bravo := createIntegrationUser(t, helper, "preview-bravo", password)
	administrator := createIntegrationUser(t, helper, "preview-administrator", password)
	for _, id := range []model.UserID{ownerID, alpha.ID, bravo.ID, administrator.ID} {
		addAffiliation(id, model.AffiliationStudent)
	}
	systemAdministrator, err := persistence.Role().GetByName(ctx, model.SystemAdministratorRoleName)
	require.NoError(t, err)
	_, err = storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: administrator.ID, RoleID: systemAdministrator.ID,
		ScopeType: model.RoleScopeInstitution, ScopeID: institutionID, StartsAt: model.TimeFromMillis(now - 60_000)})
	require.NoError(t, err)

	t.Run("RequiresAStrongRecentSession", func(t *testing.T) {
		single := loginIntegrationUser(t, handler, "preview-alpha", password, model.SessionClientCLI, "preview-alpha-single")
		response := performJSONRequest(handler, http.MethodPost, "/api/v1/academic-administration-previews", map[string]any{
			"operation": "user.disable", "scope_type": "institution", "scope_id": institutionID, "filter": map[string]any{},
		}, single.Tokens.AccessToken)
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "authentication.strong_required")
	})

	disable := createAcademicAdministrationPreview(t, handler, owner, map[string]any{
		"operation": "user.disable", "scope_type": "institution", "scope_id": institutionID,
		"filter": map[string]any{"affiliation": []string{"student"}},
	})
	disable = waitForAcademicAdministrationPreview(t, handler, owner, disable.ID, "preview_ready")
	t.Run("FreezesAndClassifiesTheMatchingUsers", func(t *testing.T) {
		require.Equal(t, 4, disable.TotalRows)
		require.NotNil(t, disable.Summary)
		require.Equal(t, 2, disable.Summary.WillChange)
		require.Equal(t, 0, disable.Summary.NoOp)
		require.Equal(t, 2, disable.Summary.Skipped)
		reasons := map[string]int{}
		for _, reason := range disable.Summary.SkipReasons {
			reasons[reason.Reason] = reason.Rows
		}
		require.Equal(t, map[string]int{
			"academic_administration_preview.self":                 1,
			"academic_administration_preview.system_administrator": 1,
		}, reasons)
		require.NotEmpty(t, disable.PreviewExpiresAt)
		require.Equal(t, []string{alpha.ID.String(), bravo.ID.String()},
			academicAdministrationPreviewRowUsers(t, handler, owner, disable.ID, "group=will_change"))
		require.Equal(t, []string{ownerID.String()},
			academicAdministrationPreviewRowUsers(t, handler, owner, disable.ID, "group=skipped&reason=academic_administration_preview.self"))
		require.Equal(t, []string{administrator.ID.String()},
			academicAdministrationPreviewRowUsers(t, handler, owner, disable.ID, "group=skipped&reason=academic_administration_preview.system_administrator"))
	})
	t.Run("IsReadOnlyThroughItsOwnSurface", func(t *testing.T) {
		response := performJSONRequest(handler, http.MethodGet, "/api/v1/onboarding-imports/"+disable.ID, nil, owner)
		require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		report := performJSONRequest(handler, http.MethodGet, "/api/v1/onboarding-imports/"+disable.ID+"/report", nil, owner)
		require.Equal(t, http.StatusNotFound, report.Code, report.Body.String())
	})

	// Charlie starts matching only after the preview froze.
	charlie := createIntegrationUser(t, helper, "preview-charlie", password)
	addAffiliation(charlie.ID, model.AffiliationStudent)

	commitPath := "/api/v1/academic-administration-previews/" + disable.ID + "/commit"
	t.Run("RefusesAStaleDigestOrRevision", func(t *testing.T) {
		wrongDigest := performIdempotentJSONRequest(handler, commitPath, map[string]any{
			"expected_revision": disable.Revision, "preview_digest": strings.Repeat("0", 64),
		}, owner, "preview-wrong-digest")
		require.Equal(t, http.StatusConflict, wrongDigest.Code, wrongDigest.Body.String())
		require.Contains(t, wrongDigest.Body.String(), "academic_administration_preview.conflict")
		wrongRevision := performIdempotentJSONRequest(handler, commitPath, map[string]any{
			"expected_revision": disable.Revision + 1, "preview_digest": disable.PreviewDigest,
		}, owner, "preview-wrong-revision")
		require.Equal(t, http.StatusConflict, wrongRevision.Code, wrongRevision.Body.String())
		require.Contains(t, wrongRevision.Body.String(), "academic_administration_preview.conflict")
	})

	commitBody := map[string]any{"expected_revision": disable.Revision, "preview_digest": disable.PreviewDigest}
	committed := performIdempotentJSONRequest(handler, commitPath, commitBody, owner, "preview-commit")
	require.Equal(t, http.StatusAccepted, committed.Code, committed.Body.String())
	var commitView academicAdministrationPreviewWire
	decodeIntegrationResponse(t, committed, &commitView)
	require.NotEmpty(t, commitView.ExecutionJobID)
	t.Run("ReplaysTheCommit", func(t *testing.T) {
		replayed := performIdempotentJSONRequest(handler, commitPath, commitBody, owner, "preview-commit")
		require.Equal(t, http.StatusAccepted, replayed.Code, replayed.Body.String())
		var replay academicAdministrationPreviewWire
		decodeIntegrationResponse(t, replayed, &replay)
		require.Equal(t, commitView.ExecutionJobID, replay.ExecutionJobID)
	})

	disable = waitForAcademicAdministrationPreview(t, handler, owner, disable.ID, "completed")
	t.Run("ExecutesOnlyTheFrozenList", func(t *testing.T) {
		require.Equal(t, 2, disable.SucceededRows)
		require.Equal(t, 2, disable.SkippedRows)
		require.Equal(t, 0, disable.FailedRows)
		for _, id := range []model.UserID{alpha.ID, bravo.ID} {
			value, getErr := persistence.User().Get(ctx, id)
			require.NoError(t, getErr)
			require.True(t, value.DisabledAt.Valid, "a frozen row was not disabled")
		}
		for _, id := range []model.UserID{charlie.ID, administrator.ID, ownerID} {
			value, getErr := persistence.User().Get(ctx, id)
			require.NoError(t, getErr)
			require.False(t, value.DisabledAt.Valid, "a User outside the frozen will-change rows was disabled")
		}
	})
	t.Run("RecordsOneBatchEntryAndNamesItOnEveryRow", func(t *testing.T) {
		batches, rows := 0, 0
		options := store.AuditListOptions{Actions: []string{string(model.ActionOnboardingBatchManage), string(model.ActionUserManage)},
			ActorID: ownerID, Limit: 200, Visibility: store.AuditVisibilityScope{InstitutionWide: true}}
		events, listErr := persistence.Audit().List(ctx, options)
		require.NoError(t, listErr)
		for _, event := range events {
			parameters := string(event.Parameters)
			if !strings.Contains(parameters, disable.ID) {
				continue
			}
			if strings.Contains(parameters, `"academic_administration_preview.batch"`) {
				batches++
				require.Equal(t, model.AuditStatusSuccess, event.Status)
				require.Contains(t, parameters, `"user.disable"`)
				require.NotContains(t, parameters, "preview-alpha", "the batch entry names no User")
			}
			if event.Action == string(model.ActionUserManage) && strings.Contains(parameters, `"onboarding_import_id"`) {
				rows++
			}
		}
		require.Equal(t, 1, batches)
		require.Equal(t, 2, rows)
	})

	t.Run("RefusesARowWhoseUserBecameASystemAdministrator", func(t *testing.T) {
		preview := createAcademicAdministrationPreview(t, handler, owner, map[string]any{
			"operation": "user.disable", "scope_type": "institution", "scope_id": institutionID,
			"filter": map[string]any{"q": "preview-charlie"},
		})
		preview = waitForAcademicAdministrationPreview(t, handler, owner, preview.ID, "preview_ready")
		require.Equal(t, []string{charlie.ID.String()}, academicAdministrationPreviewRowUsers(t, handler, owner, preview.ID, "group=will_change"))
		_, bindErr := storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: charlie.ID, RoleID: systemAdministrator.ID,
			ScopeType: model.RoleScopeInstitution, ScopeID: institutionID, StartsAt: model.TimeFromMillis(now - 60_000)})
		require.NoError(t, bindErr)
		response := performIdempotentJSONRequest(handler, "/api/v1/academic-administration-previews/"+preview.ID+"/commit", map[string]any{
			"expected_revision": preview.Revision, "preview_digest": preview.PreviewDigest,
		}, owner, "preview-administrator-commit")
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
		preview = waitForAcademicAdministrationPreview(t, handler, owner, preview.ID, "completed_with_errors")
		rows, listErr := persistence.OnboardingImport().ListOnboardingImportRows(ctx, model.OnboardingImportID(preview.ID), 0, 10)
		require.NoError(t, listErr)
		require.Len(t, rows.Rows, 1)
		require.Equal(t, "academic_administration_preview.system_administrator", rows.Rows[0].PublicCode)
		value, getErr := persistence.User().Get(ctx, charlie.ID)
		require.NoError(t, getErr)
		require.False(t, value.DisabledAt.Valid, "a System Administrator was disabled by filter")
	})

	t.Run("RefusesACommitAfterTheDeadline", func(t *testing.T) {
		expiring := createAcademicAdministrationPreview(t, handler, owner, map[string]any{
			"operation": "user_sessions.revoke", "scope_type": "institution", "scope_id": institutionID,
			"filter": map[string]any{"affiliation": []string{"student"}},
		})
		expiring = waitForAcademicAdministrationPreview(t, handler, owner, expiring.ID, "preview_ready")
		_, execErr := persistence.GetMaster().Exec(ctx,
			`UPDATE onboarding_imports SET preview_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, expiring.ID)
		require.NoError(t, execErr)
		response := performIdempotentJSONRequest(handler, "/api/v1/academic-administration-previews/"+expiring.ID+"/commit", map[string]any{
			"expected_revision": expiring.Revision, "preview_digest": expiring.PreviewDigest,
		}, owner, "preview-expired")
		require.Equal(t, http.StatusGone, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "academic_administration_preview.expired")
	})

	// A scoped administrator manages Classes under one Academic Unit and sees
	// only the Users anchored inside it.
	unit := createIntegrationResource[model.AcademicUnit](t, handler, http.MethodPost, "/api/v1/academic-units",
		map[string]any{"name": "engineering", "display_name": "Engineering"}, owner)
	otherUnit := createIntegrationResource[model.AcademicUnit](t, handler, http.MethodPost, "/api/v1/academic-units",
		map[string]any{"name": "humanities", "display_name": "Humanities"}, owner)
	programme := createIntegrationResource[model.Programme](t, handler, http.MethodPost, "/api/v1/academic-units/"+unit.ID.String()+"/programmes",
		map[string]any{"name": "computing", "display_name": "Computing"}, owner)
	level := createIntegrationResource[model.ProgrammeLevel](t, handler, http.MethodPost, "/api/v1/programmes/"+programme.ID.String()+"/levels",
		map[string]any{"name": "year-1", "display_name": "Year 1"}, owner)
	period := createIntegrationResource[model.AcademicPeriod](t, handler, http.MethodPost, "/api/v1/academic-periods", map[string]any{
		"owner_type": "academic_unit", "owner_id": unit.ID.String(), "name": "2026-2027", "display_name": "2026-2027",
		"start_at": integrationWireTime(now - 86_400_000), "end_at": integrationWireTime(now + 31_536_000_000),
	}, owner)
	class := createIntegrationResource[model.Class](t, handler, http.MethodPost, "/api/v1/programme-levels/"+level.ID.String()+"/classes",
		map[string]any{"academic_period_id": period.ID.String(), "name": "class-a", "display_name": "Class A"}, owner)
	delta := createIntegrationUser(t, helper, "preview-delta", password)
	echo := createIntegrationUser(t, helper, "preview-echo", password)
	addAffiliation(delta.ID, model.AffiliationStudent)
	addAffiliation(echo.ID, model.AffiliationStudent)
	type membershipWire struct {
		ID string `json:"id"`
	}
	deltaMembership := createIntegrationResource[membershipWire](t, handler, http.MethodPost, "/api/v1/academic-units/"+unit.ID.String()+"/members",
		map[string]any{"user_id": delta.ID.String(), "start_at": integrationWireTime(now - 60_000)}, owner)

	scopedRole, err := storetest.SaveRole(ctx, persistence, &model.Role{Name: "unit-onboarding", DisplayName: "Unit onboarding", Permissions: []string{
		string(model.ActionOnboardingBatchManage), string(model.ActionOnboardingBatchView), string(model.ActionClassMembersManage),
		string(model.ActionClassMembersView), string(model.ActionClassView), string(model.ActionUserView),
	}})
	require.NoError(t, err)
	scoped := createIntegrationUser(t, helper, "preview-scoped", password)
	_, err = storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: scoped.ID, RoleID: scopedRole.ID,
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: unit.ID.String(), StartsAt: model.TimeFromMillis(now - 60_000)})
	require.NoError(t, err)
	scopedToken := strongIntegrationSession(t, handler, "preview-scoped", password, "preview-scoped-cli")
	enroll := map[string]any{"operation": "class.enroll", "scope_type": "class", "scope_id": class.ID.String()}
	withFilter := func(filter map[string]any) map[string]any {
		body := map[string]any{"filter": filter}
		for key, value := range enroll {
			body[key] = value
		}
		return body
	}

	t.Run("ScopedViewerCannotWidenThroughTheFilter", func(t *testing.T) {
		activity := performJSONRequest(handler, http.MethodPost, "/api/v1/academic-administration-previews",
			withFilter(map[string]any{"never_signed_in": true}), scopedToken)
		require.Equal(t, http.StatusBadRequest, activity.Code, activity.Body.String())
		require.Contains(t, activity.Body.String(), "never_signed_in")

		outside := createAcademicAdministrationPreview(t, handler, scopedToken, withFilter(map[string]any{"academic_unit_id": otherUnit.ID.String()}))
		outside = waitForAcademicAdministrationPreview(t, handler, scopedToken, outside.ID, "preview_ready")
		require.Equal(t, 0, outside.TotalRows, "a unit outside the scope matches nobody")

		students := createAcademicAdministrationPreview(t, handler, scopedToken, withFilter(map[string]any{"affiliation": []string{"student"}}))
		students = waitForAcademicAdministrationPreview(t, handler, scopedToken, students.ID, "preview_ready")
		require.Equal(t, 1, students.TotalRows, "only the User anchored in the scope matches")
		require.Equal(t, []string{delta.ID.String()}, academicAdministrationPreviewRowUsers(t, handler, scopedToken, students.ID, "group=will_change"))
		require.NotContains(t, academicAdministrationPreviewRowUsers(t, handler, scopedToken, students.ID, "group=skipped"), echo.ID.String())

		foreign := performJSONRequest(handler, http.MethodGet, "/api/v1/academic-administration-previews/"+disable.ID, nil, scopedToken)
		require.Equal(t, http.StatusForbidden, foreign.Code, "a preview outside the reader's scope is refused: %s", foreign.Body.String())
		require.NotContains(t, foreign.Body.String(), "user.disable")
	})

	t.Run("RefusesARowWhoseUserStoppedBeingVisible", func(t *testing.T) {
		preview := createAcademicAdministrationPreview(t, handler, scopedToken, withFilter(map[string]any{"q": "preview-delta"}))
		preview = waitForAcademicAdministrationPreview(t, handler, scopedToken, preview.ID, "preview_ready")
		require.Equal(t, []string{delta.ID.String()}, academicAdministrationPreviewRowUsers(t, handler, scopedToken, preview.ID, "group=will_change"))

		ended := performJSONRequest(handler, http.MethodDelete, "/api/v1/academic-unit-members/"+deltaMembership.ID, nil, owner)
		require.Equal(t, http.StatusOK, ended.Code, ended.Body.String())
		require.Empty(t, academicAdministrationPreviewRowUsers(t, handler, scopedToken, preview.ID, "group=will_change"),
			"a User the reader no longer sees is not shown")

		response := performIdempotentJSONRequest(handler, "/api/v1/academic-administration-previews/"+preview.ID+"/commit", map[string]any{
			"expected_revision": preview.Revision, "preview_digest": preview.PreviewDigest,
		}, scopedToken, "preview-scoped-commit")
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
		preview = waitForAcademicAdministrationPreview(t, handler, scopedToken, preview.ID, "completed_with_errors")
		require.Equal(t, 1, preview.FailedRows)
		rows, listErr := persistence.OnboardingImport().ListOnboardingImportRows(ctx, model.OnboardingImportID(preview.ID), 0, 10)
		require.NoError(t, listErr)
		require.Len(t, rows.Rows, 1)
		require.Equal(t, "authorization.denied", rows.Rows[0].PublicCode)
		memberships, memberErr := persistence.ClassMember().ListByUser(ctx, delta.ID)
		require.NoError(t, memberErr)
		require.Empty(t, memberships, "the refused row enrolled nobody")
	})
}

// strongIntegrationSession signs a User in and completes an authenticator
// challenge, returning the access token of a strong, recent Session.
func strongIntegrationSession(t *testing.T, handler http.Handler, username, password, device string) string {
	t.Helper()
	login := loginIntegrationUser(t, handler, username, password, model.SessionClientCLI, device)
	token := login.Tokens.AccessToken
	setupResponse := performJSONRequest(handler, http.MethodPost, "/api/v1/users/me/mfa/setup", nil, token)
	require.Equal(t, http.StatusCreated, setupResponse.Code, setupResponse.Body.String())
	var setup wireMFASetup
	require.NoError(t, json.Unmarshal(setupResponse.Body.Bytes(), &setup))
	activateResponse := performJSONRequest(handler, http.MethodPost, "/api/v1/users/me/mfa/activate",
		map[string]any{"code": integrationTOTP(t, setup.Secret, time.Now().UTC().Unix()/30)}, token)
	require.Equal(t, http.StatusOK, activateResponse.Code, activateResponse.Body.String())
	var activation wireMFAActivation
	require.NoError(t, json.Unmarshal(activateResponse.Body.Bytes(), &activation))
	challenge := performJSONRequest(handler, http.MethodPost, "/api/v1/users/me/mfa/challenge",
		map[string]any{"code": activation.RecoveryCodes[0]}, token)
	require.Equal(t, http.StatusOK, challenge.Code, challenge.Body.String())
	return token
}

func createAcademicAdministrationPreview(t *testing.T, handler http.Handler, token string, body map[string]any) academicAdministrationPreviewWire {
	t.Helper()
	response := performJSONRequest(handler, http.MethodPost, "/api/v1/academic-administration-previews", body, token)
	require.Equal(t, http.StatusAccepted, response.Code, "create preview = %d: %s", response.Code, response.Body.String())
	var view academicAdministrationPreviewWire
	decodeIntegrationResponse(t, response, &view)
	return view
}

func waitForAcademicAdministrationPreview(t *testing.T, handler http.Handler, token, id, wanted string) academicAdministrationPreviewWire {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response := performJSONRequest(handler, http.MethodGet, "/api/v1/academic-administration-previews/"+id, nil, token)
		require.Equal(t, http.StatusOK, response.Code, "get preview = %d: %s", response.Code, response.Body.String())
		var current academicAdministrationPreviewWire
		decodeIntegrationResponse(t, response, &current)
		if current.State == wanted {
			return current
		}
		terminal := []string{"failed", "canceled", "completed", "completed_with_errors"}
		require.False(t, slices.Contains(terminal, current.State), "preview reached %q while waiting for %q: %#v", current.State, wanted, current)
		time.Sleep(20 * time.Millisecond)
	}
	require.FailNow(t, fmt.Sprintf("preview %s did not reach %s", id, wanted))
	return academicAdministrationPreviewWire{}
}

func academicAdministrationPreviewRowUsers(t *testing.T, handler http.Handler, token, id, query string) []string {
	t.Helper()
	response := performJSONRequest(handler, http.MethodGet, "/api/v1/academic-administration-previews/"+id+"/rows?"+query, nil, token)
	require.Equal(t, http.StatusOK, response.Code, "preview rows = %d: %s", response.Code, response.Body.String())
	var page struct {
		Items []academicAdministrationPreviewRowWire `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	users := []string{}
	for _, row := range page.Items {
		users = append(users, row.UserID)
	}
	return users
}
