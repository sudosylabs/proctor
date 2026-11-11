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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/testlib"
)

func TestAccountAndAdministrativeSessionNoticesUseRealServerGraph(t *testing.T) {
	dataSource := requireAuthenticationDatabase(t)
	persistence := openAuthenticationStore(t, dataSource)
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithConfig(func(cfg *config.Config) {
		cfg.Server.ListenAddress = "127.0.0.1:0"
	}))
	ctx := context.Background()
	const password = "correct horse battery staple"
	bootstrap, appErr := helper.App.Identity.BootstrapInstallation(ctx, invocation.Invocation{}, identity.BootstrapInstallationCommand{
		InstitutionName: "security-notice-university", InstitutionDisplayName: "Security Notice University",
		AdministratorUsername: "mail11-admin", AdministratorEmail: "mail11-admin@example.edu", AdministratorDisplayName: "Mail 11 Admin",
		AdministratorLocale: "en", AdministratorTimezone: "UTC", Password: password,
		BootstrapSecret: testlib.BootstrapSecret, Source: "203.0.113.42",
	})
	require.NoError(t, appErr)
	target, appErr := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "mail11-target", Email: "mail11-target@example.edu", DisplayName: "Mail 11 Target", Locale: "en", Timezone: "UTC",
	}, Password: password})
	require.NoError(t, appErr)
	adminLogin, appErr := helper.App.Identity.Login(ctx, invocation.Invocation{}, identity.LoginCommand{
		LoginID: bootstrap.Administrator.Username, Password: password, ClientType: model.SessionClientCLI,
		Source: "203.0.113.42", DeviceName: "mail11-admin-device",
	})
	require.NoError(t, appErr)
	adminPrincipal, appErr := helper.App.Identity.AuthenticateAccess(ctx, adminLogin.Tokens.AccessToken)
	require.NoError(t, appErr)
	adminInvocation := invocation.New(*adminPrincipal, model.RequestMetadata{RequestID: "mail11-admin", IPAddress: "203.0.113.42"})

	initialTargetLogin := loginIntegrationUser(t, helper.Handler(), target.Username, password, model.SessionClientCLI, "mail11-initial")
	_, appErr = helper.App.Identity.SetUserEnabled(ctx, adminInvocation, identity.SetUserEnabledCommand{ID: target.ID, Enabled: false})
	require.NoError(t, appErr)
	_, appErr = helper.App.Identity.SetUserEnabled(ctx, adminInvocation, identity.SetUserEnabledCommand{ID: target.ID, Enabled: true})
	require.NoError(t, appErr)
	first := loginIntegrationUser(t, helper.Handler(), target.Username, password, model.SessionClientCLI, "mail11-first")
	second := loginIntegrationUser(t, helper.Handler(), target.Username, password, model.SessionClientCLI, "mail11-second")
	require.NoError(t, helper.App.Identity.RevokeUserSession(ctx, adminInvocation, target.ID, first.Session.ID))
	require.NoError(t, helper.App.Identity.RevokeUserSessions(ctx, adminInvocation, identity.RevokeUserSessionsCommand{UserID: target.ID}))
	self := loginIntegrationUser(t, helper.Handler(), target.Username, password, model.SessionClientCLI, "mail11-self")
	selfPrincipal, appErr := helper.App.Identity.AuthenticateAccess(ctx, self.Tokens.AccessToken)
	require.NoError(t, appErr)
	selfInvocation := invocation.New(*selfPrincipal, model.RequestMetadata{RequestID: "mail11-self", IPAddress: "198.51.100.9"})
	require.NoError(t, helper.App.Identity.RevokeSession(ctx, selfInvocation, self.Session.ID))
	logout := loginIntegrationUser(t, helper.Handler(), target.Username, password, model.SessionClientCLI, "mail11-logout")
	logoutPrincipal, appErr := helper.App.Identity.AuthenticateAccess(ctx, logout.Tokens.AccessToken)
	require.NoError(t, appErr)
	require.NoError(t, helper.App.Identity.Logout(ctx, invocation.New(*logoutPrincipal, model.RequestMetadata{})))
	require.NoError(t, helper.App.Identity.Logout(ctx, invocation.New(*logoutPrincipal, model.RequestMetadata{})))
	all := loginIntegrationUser(t, helper.Handler(), target.Username, password, model.SessionClientCLI, "mail11-self-all")
	allPrincipal, appErr := helper.App.Identity.AuthenticateAccess(ctx, all.Tokens.AccessToken)
	require.NoError(t, appErr)
	allInvocation := invocation.New(*allPrincipal, model.RequestMetadata{})
	for range 2 {
		require.NoError(t, helper.App.Identity.RevokeAllSessions(ctx, allInvocation))
	}
	audits, err := persistence.Audit().List(ctx, store.AuditListOptions{
		ActorID: target.ID, Action: string(model.ActionSessionManage),
		Visibility: store.AuditVisibilityScope{InstitutionWide: true}, Limit: 20,
	})
	require.NoError(t, err)
	operations := map[string]int{}
	for _, event := range audits {
		var parameters struct {
			Operation string `json:"operation"`
		}
		require.NoError(t, json.Unmarshal(event.Parameters, &parameters))
		require.Equal(t, model.AuditStatusSuccess, event.Status)
		require.Equal(t, target.ID.String(), event.Resource.ID)
		operations[parameters.Operation]++
		for _, token := range []string{self.Tokens.AccessToken, self.Tokens.RefreshToken, logout.Tokens.AccessToken, logout.Tokens.RefreshToken, all.Tokens.AccessToken, all.Tokens.RefreshToken} {
			for _, payload := range [][]byte{event.Parameters, event.PriorState, event.Result} {
				require.False(t, bytes.Contains(payload, []byte(token)), "self Session audit exposed a credential or its hash")
				require.False(t, bytes.Contains(payload, []byte(model.HashToken(token))), "self Session audit exposed a credential or its hash")
			}
		}
	}
	require.Len(t, audits, 5)
	require.Equal(t, 1, operations["revoke_own_session"])
	require.Equal(t, 2, operations["revoke_own_sessions"])
	require.Equal(t, 2, operations["logout"])

	durable, err := persistence.Mail().ListDeliveries(ctx, store.MailDeliveryListOptions{TemplateKeys: []model.MailTemplateKey{
		model.MailTemplateIdentityAccountDisabled, model.MailTemplateIdentityAccountEnabled, model.MailTemplateIdentitySessionsRevokedByAdmin,
	}, Limit: 20})
	require.NoError(t, err)
	counts := map[model.MailTemplateKey]int{}
	for _, delivery := range durable {
		counts[delivery.TemplateKey]++
	}
	require.Len(t, durable, 4)
	require.Equal(t, 1, counts[model.MailTemplateIdentityAccountDisabled])
	require.Equal(t, 1, counts[model.MailTemplateIdentityAccountEnabled])
	require.Equal(t, 2, counts[model.MailTemplateIdentitySessionsRevokedByAdmin])
	require.NotNil(t, initialTargetLogin.Session, "integration session fixture was not established")
	require.NotNil(t, second.Session, "integration session fixture was not established")

	startIntegrationServer(t, helper)
	deliveries := waitForRecoveryDeliveries(t, helper, 4)
	require.Len(t, deliveries, 4, "captured deliveries")
	for _, delivery := range deliveries {
		require.Len(t, delivery.Recipients, 1)
		require.Equal(t, target.Email, delivery.Recipients[0])
		for _, forbidden := range [][]byte{
			[]byte("mail11-admin"), []byte("account disabled by administrator"), []byte("session revoked by administrator"),
			[]byte("203.0.113.42"), []byte(adminLogin.Tokens.AccessToken),
		} {
			require.False(t, bytes.Contains(delivery.Data, forbidden), "security notice exposed private value %q", forbidden)
		}
	}
}
