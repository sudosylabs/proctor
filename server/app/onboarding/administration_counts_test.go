// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// administrationCountMaximum is the bound the administration summary passes
// to each onboarding count, the parent's administrationCountMaximum.
const administrationCountMaximum = 1000

func administrationCountsSession() invocation.Invocation {
	return invocation.New(model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewSessionCredentialID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now()}, model.RequestMetadata{})
}

type openImportCountStoreFake struct {
	store.OnboardingImportStore
	visibility store.OnboardingImportVisibilityScope
	maximum    int
}

func (f *openImportCountStoreFake) CountOpenOnboardingImports(_ context.Context, visibility store.OnboardingImportVisibilityScope, maximum int) (int, error) {
	f.visibility, f.maximum = visibility, maximum
	return 4, nil
}

func TestOpenOnboardingImportCountUsesTheBatchViewScopes(t *testing.T) {
	t.Parallel()
	imports := &openImportCountStoreFake{}
	caller := administrationCountsSession()
	authorization := accesstest.New(t, accesstest.Options{Grants: []accesstest.Grant{{UserID: caller.Principal().UserID,
		Actions: []model.Action{model.ActionOnboardingBatchView}}}})
	service := &Service{imports: imports, authorizer: authorization.Authorizer}
	total, err := service.countOpenOnboardingImports(context.Background(), caller, administrationCountMaximum)
	require.NoError(t, err)
	require.Equal(t, 4, total)
	decisions := authorization.Audits.Decisions()
	require.Len(t, decisions, 1, "the count records one scoped decision")
	require.Equal(t, string(model.ActionOnboardingBatchView), decisions[0].Action)
	require.Equal(t, model.RoleScopeInstitution, decisions[0].ScopeType)
	require.Equal(t, model.AuditStatusSuccess, decisions[0].Status)
	require.Equal(t, store.OnboardingImportVisibilityScope{InstitutionWide: true}, imports.visibility, "the import scopes are the onboarding_batch.view scopes")
	require.Equal(t, administrationCountMaximum, imports.maximum)

	// Another caller holds no onboarding_batch.view.
	_, err = service.countOpenOnboardingImports(context.Background(), administrationCountsSession(), administrationCountMaximum)
	require.True(t, apperror.Is(err, "authorization.denied"), "a denial reaches the summary, which omits the count: %v", err)
}

func TestInvitationListCountsTheFirstPageAndPendingSummary(t *testing.T) {
	t.Parallel()
	now := model.TimeFromMillis(1_800_000_000_000)
	classID := model.NewClassID()
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{ID: model.NewInvitationID(),
		TargetEmail: "student@example.edu", ClassID: classID, AcademicPeriodID: model.NewAcademicPeriodID(),
		IntendedStartsAt: now, InviterUserID: model.NewUserID(), ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: now})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation, countTotal: 9}
	caller := administrationCountsSession()
	authorization := permissiveAccess(t, accesstest.Options{}, []model.UserID{caller.Principal().UserID})
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, authorization, newTestComposer(t), now.Add(time.Hour))

	first, err := service.ListInvitations(context.Background(), caller, ListInvitationsQuery{State: model.InvitationPending, Limit: 25})
	require.NoError(t, err)
	require.Equal(t, paging.Total{Counted: true, Total: 9}, first.Total)
	require.Equal(t, persistence.listOptions, persistence.countOptions, "the count reads the list's visibility and filters")

	later, err := service.ListInvitations(context.Background(), caller, ListInvitationsQuery{Limit: 25, AfterAt: now, AfterID: model.NewInvitationID()})
	require.NoError(t, err)
	require.Equal(t, paging.Total{}, later.Total)

	before := len(authorization.Audits.Decisions())
	pending, err := service.countPendingInvitations(context.Background(), caller, administrationCountMaximum)
	require.NoError(t, err)
	require.Equal(t, 9, pending)
	require.Equal(t, []model.Action{model.ActionInvitationView}, decidedActions(authorization)[before:], "the summary authorizes like the list")
	require.Equal(t, store.InvitationListOptions{Visibility: store.InvitationVisibilityScope{InstitutionWide: true}, State: model.InvitationPending,
		Unexpired: true, Limit: 1}, persistence.countOptions)
	require.Equal(t, administrationCountMaximum, persistence.countMaximum)
}
