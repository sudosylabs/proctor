// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestConfigureBrowserPolicyUsesInstallationPin(t *testing.T) {
	for _, origin := range []string{"http://institution.example", "http://unrelated.example", "http://institution.example:8080"} {
		t.Run(origin, func(t *testing.T) {
			fixture := newAuthoringFixture(t)
			fixture.persistence.actorIsManager = true
			fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
			policy, err := model.NewBrowserPolicy(true, "start", []model.BrowserPolicyRule{{RuleID: "start", Origin: origin, PathPrefix: "/", HostMatch: model.BrowserPolicyHostExact, BlockedNavigationOutcome: model.BrowserPolicyBlockedNavigationRecord, InstitutionHTTPException: true}})
			require.NoError(t, err)
			result, err := fixture.service.ConfigureExamDraftBrowserPolicy(context.Background(), fixture.inv, ConfigureDraftBrowserPolicyCommand{ExamID: fixture.examID, ExpectedDraftRevision: 1, Policy: policy, IdempotencyKey: "browser-pin"})
			if origin == "http://institution.example" {
				require.NoError(t, err, "matching exception: %#v", result)
				require.True(t, result.Draft.BrowserPolicy.Enabled, "matching exception: %#v", result)
			} else {
				require.Error(t, err, "foreign exception reached application effects")
				require.Empty(t, fixture.publisher.Calls(), "foreign exception reached application effects")
				require.Empty(t, *fixture.order, "foreign exception reached application effects")
			}
		})
	}
}
