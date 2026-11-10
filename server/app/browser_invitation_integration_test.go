//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/testlib"
)

// TestBrowserInvitationStartPresentsTheInvitedAddressAndClass proves the
// public start projection names the target mailbox and the student Class from
// authoritative PostgreSQL state without returning the claim.
func TestBrowserInvitationStartPresentsTheInvitedAddressAndClass(t *testing.T) {
	dataSource := requireAuthenticationDatabase(t)
	persistence := openAuthenticationStore(t, dataSource)
	helper := testlib.Setup(t,
		testlib.WithConfig(func(cfg *config.Config) { cfg.Server.PublicURL = "https://proctor.example.edu" }),
		testlib.WithStore(persistence),
	)
	bootstrap, err := helper.App.Identity.BootstrapInstallation(context.Background(), invocation.Invocation{}, identity.BootstrapInstallationCommand{
		InstitutionName: "invitation-university", InstitutionDisplayName: "Invitation University",
		AdministratorUsername: "invitation-admin", AdministratorEmail: "invitation-admin@example.edu",
		AdministratorDisplayName: "Invitation Admin", AdministratorLocale: "en", AdministratorTimezone: "UTC",
		Password: "bootstrap correct horse battery staple", BootstrapSecret: testlib.BootstrapSecret, Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	inviter, err := helper.App.Identity.CreateLocalUser(context.Background(), identity.CreateLocalUserCommand{User: &model.User{
		Username: "class-inviter", Email: "class-inviter@example.edu", DisplayName: "Class Inviter",
	}, Password: "inviter correct horse battery staple"})
	require.NoError(t, err)
	claim, _ := seedExternalAdmissionInvitation(t, persistence, bootstrap.Institution, inviter, "invited.student@example.edu")

	response := performJSONRequest(helper.Handler(), http.MethodPost, "/api/v1/auth/browser/invitations", map[string]any{"claim": claim}, "")
	require.Equal(t, http.StatusCreated, response.Code, "start = %d: %s", response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), claim, "the start projection returned the claim")
	var started struct {
		Purpose          string `json:"purpose"`
		Requirement      string `json:"requirement"`
		Email            string `json:"email"`
		ClassDisplayName string `json:"class_display_name"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &started))
	require.Equal(t, "student_class", started.Purpose)
	require.Equal(t, "account", started.Requirement)
	require.Equal(t, "invited.student@example.edu", started.Email)
	require.Equal(t, "External Admission", started.ClassDisplayName)
}
