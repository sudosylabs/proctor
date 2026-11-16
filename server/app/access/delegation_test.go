// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestDelegationCeilingRequiresOwnedActionAndContainingScope(t *testing.T) {
	t.Parallel()

	institutionID := model.NewInstitutionID()
	rootID, childID, siblingID := model.NewAcademicUnitID(), model.NewAcademicUnitID(), model.NewAcademicUnitID()
	userID, roleID := model.NewUserID(), model.NewRoleID()
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		rootID.String():    {{ID: rootID, InstitutionID: institutionID}},
		childID.String():   {{ID: childID, InstitutionID: institutionID}, {ID: rootID, InstitutionID: institutionID}},
		siblingID.String(): {{ID: siblingID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	access, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{{ID: roleID, Permissions: []string{
		string(model.ActionProgrammeManage), string(model.ActionRoleBindingManage),
	}}}}, Bindings: &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{{
		UserID: userID, RoleID: roleID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: rootID.String(),
	}}}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	principal := model.Principal{
		UserID: userID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, SessionID: model.NewSessionID(),
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}

	for name, test := range map[string]struct {
		actions []string
		target  model.AcademicUnitID
		want    bool
	}{
		"ordinary descendant":  {[]string{string(model.ActionProgrammeManage)}, childID, true},
		"protected descendant": {[]string{string(model.ActionRoleBindingManage)}, childID, true},
		"protected same scope": {[]string{string(model.ActionRoleBindingManage)}, rootID, false},
		"sibling":              {[]string{string(model.ActionProgrammeManage)}, siblingID, false},
		"unowned action":       {[]string{string(model.ActionClassManage)}, childID, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, delegationErr := access.canDelegateActionsAtScope(
				context.Background(), principal, test.actions,
				model.RoleScopeAcademicUnit, test.target.String(),
			)
			require.NoError(t, delegationErr, "delegation")
			require.Equal(t, test.want, got, "delegation")
		})
	}

	principal.CredentialType = model.CredentialPersonalAccessToken
	principal.SessionID = ""
	principal.AuthenticationMethod = "personal_access_token"
	principal.AuthenticationStrength = ""
	principal.AuthenticatedAt = time.Time{}
	principal.ClientType = model.SessionClientCLI
	principal.CredentialScopes = []string{string(model.ActionProgrammeManage)}
	principal.AcademicUnitID = rootID
	allowed, delegationErr := access.canDelegateActionsAtScope(
		context.Background(), principal, []string{string(model.ActionProgrammeManage)},
		model.RoleScopeAcademicUnit, childID.String(),
	)
	require.NoError(t, delegationErr, "PAT descendant delegation")
	require.True(t, allowed, "PAT descendant delegation")
	principal.CredentialScopes = []string{string(model.ActionClassView)}
	allowed, delegationErr = access.canDelegateActionsAtScope(
		context.Background(), principal, []string{string(model.ActionProgrammeManage)},
		model.RoleScopeAcademicUnit, childID.String(),
	)
	require.NoError(t, delegationErr, "PAT action ceiling")
	require.False(t, allowed, "PAT action ceiling")
}
