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
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

// A role holder without two-step verification signs in on the web to a
// Session restricted to setting it up; enrolling lifts the restriction, and
// Proctor Desktop refuses the single-factor sign-in meanwhile.
func TestRoleHolderMFAPolicyRestrictsSignInUntilEnrollment(t *testing.T) {
	ctx := context.Background()
	persistence := openAuthenticationStore(t, requireAuthenticationDatabase(t))
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithConfig(func(cfg *config.Config) {
		cfg.Server.ListenAddress = "127.0.0.1:0"
		cfg.Authentication.MFA.Enabled = true
		cfg.Authentication.MFA.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32))
	}))
	const adminPassword = "amber walrus ledger tidal frost"
	bootstrap := performJSONRequest(helper.Handler(), http.MethodPost, "/api/v1/bootstrap", map[string]any{
		"bootstrap_secret": testlib.BootstrapSecret,
		"institution":      map[string]any{"name": "role-holder-mfa", "display_name": "Role Holder MFA"},
		"administrator":    map[string]any{"username": "role-holder-mfa-admin", "email": "role-holder-mfa-admin@example.edu"},
		"password":         adminPassword,
	}, "")
	require.Equal(t, http.StatusCreated, bootstrap.Code, "bootstrap=%d %s", bootstrap.Code, bootstrap.Body.String())
	admin, err := helper.App.Login(ctx, invocation.Invocation{}, app.LoginCommand{LoginID: "role-holder-mfa-admin", Password: adminPassword,
		ClientType: model.SessionClientWeb, Source: "127.0.0.1:1"})
	require.NoError(t, err)
	strengthenRoleAdministratorSession(t, helper.Handler(), admin.Tokens.AccessToken)
	adminPrincipal, err := helper.App.AuthenticateAccess(ctx, admin.Tokens.AccessToken)
	require.NoError(t, err)
	adminInvocation := invocation.New(*adminPrincipal, model.RequestMetadata{RequestID: "role-holder-mfa-policy"})
	current, err := helper.App.GetAccessPolicy(ctx, adminInvocation)
	require.NoError(t, err)
	settings := current.Policy.Settings()
	settings.RoleHolderMFARequired = true
	replaced, err := helper.App.ReplaceAccessPolicy(ctx, adminInvocation, app.ReplaceAccessPolicyCommand{
		ExpectedRevision: current.Policy.Revision, Settings: settings, RevokeExistingSessions: true, IdempotencyKey: "role-holder-mfa-enable",
	})
	require.NoError(t, err)
	require.True(t, replaced.Policy.RoleHolderMFARequired)
	stillAdmin := performJSONRequest(helper.Handler(), http.MethodGet, "/api/v1/users/me", nil, admin.Tokens.AccessToken)
	require.Equal(t, http.StatusOK, stillAdmin.Code, "a multi-factor role holder's Session is kept: %s", stillAdmin.Body.String())

	const roleHolderPassword = "cobalt fennel canyon brisk otter"
	roleHolder := createIntegrationUser(t, helper, "role-holder-mfa-teacher", roleHolderPassword)
	role, err := persistence.Role().GetByName(ctx, model.SystemAdministratorRoleName)
	require.NoError(t, err)
	institution, err := persistence.Institution().GetSingleton(ctx)
	require.NoError(t, err)
	_, err = storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{UserID: roleHolder.ID, RoleID: role.ID,
		ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), StartsAt: model.TimeFromMillis(model.GetMillis() - 100)})
	require.NoError(t, err)

	restricted, err := helper.App.Login(ctx, invocation.Invocation{}, app.LoginCommand{LoginID: "role-holder-mfa-teacher", Password: roleHolderPassword,
		ClientType: model.SessionClientWeb, Source: "127.0.0.1:2"})
	require.NoError(t, err)
	require.True(t, restricted.Session.MFAEnrollmentRequired, "a role holder without two-step gets a restricted Session")
	ordinary := performJSONRequest(helper.Handler(), http.MethodGet, "/api/v1/users/me", nil, restricted.Tokens.AccessToken)
	require.Equal(t, http.StatusUnauthorized, ordinary.Code, "restricted Session reached an ordinary route: %s", ordinary.Body.String())
	status := performJSONRequest(helper.Handler(), http.MethodGet, "/api/v1/users/me/mfa", nil, restricted.Tokens.AccessToken)
	require.Equal(t, http.StatusOK, status.Code, "status=%s", status.Body.String())
	var state struct {
		EnrollmentRequired bool `json:"mfa_enrollment_required"`
		RecoveryRequired   bool `json:"mfa_recovery_required"`
	}
	require.NoError(t, json.Unmarshal(status.Body.Bytes(), &state))
	require.True(t, state.EnrollmentRequired)
	require.False(t, state.RecoveryRequired)

	_, err = helper.App.AuthenticateDesktopAuthorizationLocally(ctx, invocation.Invocation{}, app.AuthenticateDesktopAuthorizationLocallyCommand{
		Binding: model.NewCredentialToken(), LoginID: "role-holder-mfa-teacher", Password: roleHolderPassword, Source: "127.0.0.1:3"})
	require.True(t, apperror.Is(err, "authentication.mfa.enrollment_required"), "Desktop sign-in error = %v", err)
	_, err = helper.App.Login(ctx, invocation.Invocation{}, app.LoginCommand{LoginID: "role-holder-mfa-teacher", Password: roleHolderPassword,
		ClientType: model.SessionClientCLI, Source: "127.0.0.1:4"})
	require.True(t, apperror.Is(err, "authentication.mfa.enrollment_required"), "CLI sign-in error = %v", err)

	strengthenRoleAdministratorSession(t, helper.Handler(), restricted.Tokens.AccessToken)
	enrolled, err := persistence.Session().Get(ctx, restricted.Session.ID)
	require.NoError(t, err)
	require.False(t, enrolled.MFAEnrollmentRequired, "enrollment lifted the restriction")
	require.Equal(t, model.AuthenticationMultiFactor, enrolled.AuthenticationStrength)
	ordinary = performJSONRequest(helper.Handler(), http.MethodGet, "/api/v1/users/me", nil, restricted.Tokens.AccessToken)
	require.Equal(t, http.StatusOK, ordinary.Code, "enrolled Session=%d %s", ordinary.Code, ordinary.Body.String())
	disabled := performJSONRequest(helper.Handler(), http.MethodPost, "/api/v1/users/me/mfa/disable", nil, restricted.Tokens.AccessToken)
	require.Equal(t, http.StatusConflict, disabled.Code, "disable=%s", disabled.Body.String())
	require.Contains(t, disabled.Body.String(), `"code":"mfa.required_by_policy"`)
}

// A deployment that disables MFA after the rule is on pauses the rule rather
// than locking every role holder, administrators included, out.
func TestRoleHolderMFAPolicyPausesWhileMFAIsDisabled(t *testing.T) {
	ctx := context.Background()
	persistence := openAuthenticationStore(t, requireAuthenticationDatabase(t))
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithConfig(func(cfg *config.Config) {
		cfg.Server.ListenAddress = "127.0.0.1:0"
		cfg.Authentication.MFA.Enabled = false
	}))
	const adminPassword = "saffron glacier pylon murmur quill"
	bootstrap := performJSONRequest(helper.Handler(), http.MethodPost, "/api/v1/bootstrap", map[string]any{
		"bootstrap_secret": testlib.BootstrapSecret,
		"institution":      map[string]any{"name": "paused-rule", "display_name": "Paused Rule"},
		"administrator":    map[string]any{"username": "paused-rule-admin", "email": "paused-rule-admin@example.edu"},
		"password":         adminPassword,
	}, "")
	require.Equal(t, http.StatusCreated, bootstrap.Code, "bootstrap=%d %s", bootstrap.Code, bootstrap.Body.String())
	// The preflight blocker refuses turning the rule on without MFA, so the
	// test stands in for a deployment that disabled MFA after it was on.
	_, err := persistence.GetMaster().Exec(ctx, `UPDATE access_policies SET role_holder_mfa_required=true WHERE singleton=1`)
	require.NoError(t, err)

	admin, err := helper.App.Login(ctx, invocation.Invocation{}, app.LoginCommand{LoginID: "paused-rule-admin", Password: adminPassword,
		ClientType: model.SessionClientWeb, Source: "127.0.0.1:1"})
	require.NoError(t, err)
	require.False(t, admin.Session.MFAEnrollmentRequired, "a paused rule restricts nobody")
	ordinary := performJSONRequest(helper.Handler(), http.MethodGet, "/api/v1/users/me", nil, admin.Tokens.AccessToken)
	require.Equal(t, http.StatusOK, ordinary.Code, "ordinary=%s", ordinary.Body.String())

	read := performJSONRequest(helper.Handler(), http.MethodGet, "/api/v1/access-policy", nil, admin.Tokens.AccessToken)
	require.Equal(t, http.StatusOK, read.Code, "read=%s", read.Body.String())
	var policy struct {
		Required bool `json:"role_holder_mfa_required"`
		Enforced bool `json:"role_holder_mfa_enforced"`
	}
	require.NoError(t, json.Unmarshal(read.Body.Bytes(), &policy))
	require.True(t, policy.Required)
	require.False(t, policy.Enforced)

	_, err = helper.App.AuthenticateDesktopAuthorizationLocally(ctx, invocation.Invocation{}, app.AuthenticateDesktopAuthorizationLocallyCommand{
		Binding: model.NewCredentialToken(), LoginID: "paused-rule-admin", Password: adminPassword, Source: "127.0.0.1:2"})
	require.Error(t, err, "an unbound Desktop transaction still fails")
	require.False(t, apperror.Is(err, "authentication.mfa.enrollment_required"), "Desktop sign-in is not refused by a paused rule: %v", err)
	cli, err := helper.App.Login(ctx, invocation.Invocation{}, app.LoginCommand{LoginID: "paused-rule-admin", Password: adminPassword,
		ClientType: model.SessionClientCLI, Source: "127.0.0.1:3"})
	require.NoError(t, err, "CLI sign-in is not refused by a paused rule")
	require.False(t, cli.Session.MFAEnrollmentRequired)
}
