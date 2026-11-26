// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestDesktopReleasesListTheCatalogueNewestFirst(t *testing.T) {
	t.Parallel()
	service, _, _, inv := desktopCompatibilityServiceFixture(t)
	released := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	builds := []model.DesktopBuildTuple{
		testDesktopBuildTuple("1.2.3", "darwin-old", model.DesktopPlatformDarwin, model.DesktopArchitectureARM64, 1),
		testDesktopBuildTuple("1.4.0", "darwin-new", model.DesktopPlatformDarwin, model.DesktopArchitectureARM64, 1),
		testDesktopBuildTuple("1.3.0", "linux-only", model.DesktopPlatformLinux, model.DesktopArchitectureX64, 1),
	}
	for index := range builds {
		builds[index].ReleasedAt = released.Add(time.Duration(index) * time.Hour)
	}
	require.NoError(t, service.requireDesktopCompatibility(builds))

	releases, err := service.ListDesktopReleases(context.Background(), inv)
	require.NoError(t, err)
	require.Len(t, releases, 3)
	require.Equal(t, []string{"darwin-new", "linux-only", "darwin-old"},
		[]string{releases[0].Build.DesktopBuildID, releases[1].Build.DesktopBuildID, releases[2].Build.DesktopBuildID})
	require.Equal(t, []bool{true, true, false}, []bool{releases[0].Newest, releases[1].Newest, releases[2].Newest},
		"newest is per platform and architecture")
	require.True(t, builds[1].ReleasedAt.Equal(releases[0].Build.ReleasedAt))

	require.NoError(t, service.requireDesktopCompatibility(nil))
	empty, err := service.ListDesktopReleases(context.Background(), inv)
	require.NoError(t, err)
	require.Empty(t, empty, "the production catalogue is empty until signed releases exist")
}

func TestDesktopReleasesAndOpenSittingsNeedTheirGrants(t *testing.T) {
	t.Parallel()
	service, persistence, fixture, inv := desktopCompatibilityServiceFixture(t)
	persistence.openSittings = 4
	count, err := service.CountOpenSittings(context.Background(), inv, 1000)
	require.NoError(t, err)
	require.Equal(t, 4, count)
	decisions := fixture.Audits.Decisions()
	require.Len(t, decisions, 1, "the count records one decision")
	require.Equal(t, string(model.ActionAccessPolicyView), decisions[0].Action,
		"the count is audited as a read; a recorded manage decision reads as a policy change")

	outsider := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: time.UnixMilli(10_000), ClientType: model.SessionClientWeb}
	scoped := accessWith(t, nil, accesstest.Options{Grants: []accesstest.Grant{{
		UserID: outsider.UserID, Actions: []model.Action{model.ActionDesktopCompatibilityPolicyManage},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: model.NewAcademicUnitID().String(),
	}}})
	service = withAccess(service, scoped)
	service.institutions = &accessPolicyInstitutionFake{institution: scoped.Institution}
	outsiderInv := invocation.New(outsider, model.RequestMetadata{RequestID: "request-2"})
	_, err = service.CountOpenSittings(context.Background(), outsiderInv, 1000)
	require.True(t, apperror.Is(err, "authorization.denied"), "a unit-scoped grant is not Institution-wide: %v", err)
	_, err = service.ListDesktopReleases(context.Background(), outsiderInv)
	require.True(t, apperror.Is(err, "authorization.denied"), "the list needs access_policy.view: %v", err)

	viewer := accessWith(t, nil, accesstest.Options{Grants: []accesstest.Grant{
		institutionGrant(outsider.UserID, model.ActionAccessPolicyView),
		{UserID: outsider.UserID, Actions: []model.Action{model.ActionDesktopCompatibilityPolicyManage},
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: model.NewAcademicUnitID().String()},
	}})
	service = withAccess(service, viewer)
	service.institutions = &accessPolicyInstitutionFake{institution: viewer.Institution}
	_, err = service.CountOpenSittings(context.Background(), outsiderInv, 1000)
	require.True(t, apperror.Is(err, "authorization.denied"), "viewing the policy is not enough to count: %v", err)
	for _, decision := range viewer.Audits.Decisions() {
		require.NotEqual(t, string(model.ActionDesktopCompatibilityPolicyManage), decision.Action, "the manage check records nothing")
	}
}

func TestDesktopCompatibilityPolicyChangePublishesOnceAfterCommit(t *testing.T) {
	t.Parallel()
	service, persistence, _, inv := desktopCompatibilityServiceFixture(t)
	publisher := &realtimetest.Publisher{}
	service.publisher = publisher
	settings := model.DesktopCompatibilityPolicySettings{
		RevokedDesktopBuilds: []model.DesktopBuildRevocationRequest{{BuildID: "darwin-current", Reason: "Crashes on wake"}},
		Availability:         model.DesktopAvailabilityReady,
	}
	updated := persistence.policy.Clone()
	require.NoError(t, updated.Replace(1, settings, inv.Principal().UserID, time.UnixMilli(11_000)))
	persistence.result = &store.DesktopCompatibilityPolicyReplacementResult{Policy: updated, Changed: true}

	view, err := service.ReplaceDesktopCompatibilityPolicy(context.Background(), inv, ReplaceDesktopCompatibilityPolicyCommand{
		ExpectedRevision: 1, Settings: settings, IdempotencyKey: "desktop-policy-event",
	})
	require.NoError(t, err)
	require.Contains(t, view.RevokedBuilds, "darwin-current", "a revoked catalogue build is described")
	calls := publisher.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "desktop_compatibility_policy.changed", calls[0].Event.Name)
	require.Equal(t, model.ActionDesktopCompatibilityPolicyManage, calls[0].Event.Action, "only the policy's managers receive it")

	persistence.result = &store.DesktopCompatibilityPolicyReplacementResult{Policy: updated, Changed: true, Replayed: true}
	_, err = service.ReplaceDesktopCompatibilityPolicy(context.Background(), inv, ReplaceDesktopCompatibilityPolicyCommand{
		ExpectedRevision: 1, Settings: settings, IdempotencyKey: "desktop-policy-event",
	})
	require.NoError(t, err)
	require.Len(t, publisher.Calls(), 1, "a replay publishes nothing")
}
