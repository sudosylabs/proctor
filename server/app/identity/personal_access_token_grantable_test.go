// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// grantableAcademicUnitStoreFake serves both the resolver's ancestry reads and
// the PAT service's unit reads from one hierarchy.
type grantableAcademicUnitStoreFake struct {
	store.AcademicUnitStore
	units     map[model.AcademicUnitID]*model.AcademicUnit
	ancestors map[string][]*model.AcademicUnit
}

func (s *grantableAcademicUnitStoreFake) Get(_ context.Context, id model.AcademicUnitID) (*model.AcademicUnit, error) {
	unit, ok := s.units[id]
	if !ok {
		return nil, store.NewErrNotFound("academic_unit", id.String())
	}
	return unit, nil
}

func (s *grantableAcademicUnitStoreFake) ListAncestors(_ context.Context, id model.AcademicUnitID) ([]*model.AcademicUnit, error) {
	return s.ancestors[id.String()], nil
}

type grantableScopeFixture struct {
	now           time.Time
	userID        model.UserID
	institutionID model.InstitutionID
	parentID      model.AcademicUnitID
	childID       model.AcademicUnitID
	siblingID     model.AcademicUnitID
	units         *grantableAcademicUnitStoreFake
	institutions  *personalAccessTokenInstitutionStoreFake
	access        *access.Authorizer
}

func newGrantableScopeFixture(t *testing.T) grantableScopeFixture {
	t.Helper()
	fixture := grantableScopeFixture{
		now:           time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		userID:        model.NewUserID(),
		institutionID: model.NewInstitutionID(),
		parentID:      model.NewAcademicUnitID(),
		childID:       model.NewAcademicUnitID(),
		siblingID:     model.NewAcademicUnitID(),
	}
	parent := &model.AcademicUnit{ID: fixture.parentID, InstitutionID: fixture.institutionID, DisplayName: "School of Science"}
	child := &model.AcademicUnit{ID: fixture.childID, InstitutionID: fixture.institutionID, ParentID: fixture.parentID, DisplayName: "Computing"}
	sibling := &model.AcademicUnit{ID: fixture.siblingID, InstitutionID: fixture.institutionID, DisplayName: "School of Arts"}
	fixture.units = &grantableAcademicUnitStoreFake{
		units: map[model.AcademicUnitID]*model.AcademicUnit{parent.ID: parent, child.ID: child, sibling.ID: sibling},
		ancestors: map[string][]*model.AcademicUnit{
			parent.ID.String():  {parent},
			child.ID.String():   {child, parent},
			sibling.ID.String(): {sibling},
		},
	}
	institution := &model.Institution{ID: fixture.institutionID}
	fixture.institutions = &personalAccessTokenInstitutionStoreFake{institution: institution}
	resolver, err := access.NewScopeResolver(access.ScopeResolverDependencies{
		Institutions: &accessInstitutionStoreFake{institution: institution}, AcademicUnits: fixture.units,
		Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{},
		Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{},
		Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{},
	})
	require.NoError(t, err)
	institutionRole, unitRole, childRole := model.NewRoleID(), model.NewRoleID(), model.NewRoleID()
	fixture.access, err = access.NewAuthorizer(access.AuthorizerDependencies{
		Roles: &accessRoleStoreFake{roles: []*model.Role{
			{ID: institutionRole, Permissions: []string{string(model.ActionAuditView), string(model.ActionExamManage)}},
			{ID: unitRole, Permissions: []string{
				string(model.ActionExamView), string(model.ActionClassManage),
				string(model.ActionInstitutionManage), string(model.ActionRoleBindingManage),
			}},
			{ID: childRole, Permissions: []string{string(model.ActionProgrammeView)}},
		}},
		Bindings: &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{
			{UserID: fixture.userID, RoleID: institutionRole, ScopeType: model.RoleScopeInstitution, ScopeID: fixture.institutionID.String()},
			{UserID: fixture.userID, RoleID: unitRole, ScopeType: model.RoleScopeAcademicUnit, ScopeID: fixture.parentID.String()},
			{UserID: fixture.userID, RoleID: childRole, ScopeType: model.RoleScopeAcademicUnit, ScopeID: fixture.childID.String()},
		}},
		Resolver: resolver, Audit: accessDecisionAuditFake{}, Now: func() time.Time { return fixture.now },
	})
	require.NoError(t, err)
	return fixture
}

func (f grantableScopeFixture) service(t *testing.T, authorization resourceAuthorizer) *Service {
	t.Helper()
	service, err := personalAccessTokenTestService(&Service{
		personalAccessTokens: &personalAccessTokenStoreFake{saveAt: f.now}, users: &personalAccessTokenUserStoreFake{},
		academicUnits: f.units, institutions: f.institutions, securityAudit: &personalAccessTokenAuditorFake{},
		authorizer: authorization, mail: &personalAccessTokenMailPreparerFake{},
		personalAccessTokenPolicy: PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 1000},
		recentAuthenticationTTL:   15 * time.Minute, newCredential: func() string { return "credential" },
		now: func() time.Time { return f.now },
	})
	require.NoError(t, err)
	return service
}

func TestGrantablePersonalAccessTokenScopesFollowDelegationAuthority(t *testing.T) {
	t.Parallel()

	fixture := newGrantableScopeFixture(t)
	service := fixture.service(t, fixture.access)
	caller := invocation.New(personalAccessTokenSessionPrincipal(fixture.userID, fixture.now), model.RequestMetadata{})

	catalogue, err := service.ListGrantablePersonalAccessTokenScopes(context.Background(), caller, ListGrantablePersonalAccessTokenScopesQuery{})
	require.NoError(t, err)
	require.False(t, catalogue.AcademicUnitsHasMore)
	require.Len(t, catalogue.AcademicUnits, 2)
	// Ordered by display name; the sibling is not a delegable root.
	require.Equal(t, fixture.childID, catalogue.AcademicUnits[0].ID)
	require.Equal(t, fixture.parentID, catalogue.AcademicUnits[1].ID)

	got := map[model.Action]PersonalAccessTokenGrantableScope{}
	for _, scope := range catalogue.Scopes {
		got[scope.Action] = scope
	}
	both := []model.AcademicUnitID{fixture.parentID, fixture.childID}
	slices.Sort(both)
	require.Equal(t, map[model.Action]PersonalAccessTokenGrantableScope{
		model.ActionAuditView:     {Action: model.ActionAuditView, ResourceTypes: []model.ResourceType{model.ResourceInstitution}, InstitutionWide: true, AcademicUnitIDs: both},
		model.ActionExamManage:    {Action: model.ActionExamManage, ResourceTypes: []model.ResourceType{model.ResourceExam}, InstitutionWide: true, AcademicUnitIDs: both},
		model.ActionExamView:      {Action: model.ActionExamView, ResourceTypes: []model.ResourceType{model.ResourceExam}, AcademicUnitIDs: both},
		model.ActionClassManage:   {Action: model.ActionClassManage, ResourceTypes: []model.ResourceType{model.ResourceClass, model.ResourceProgrammeLevel}, AcademicUnitIDs: both},
		model.ActionProgrammeView: {Action: model.ActionProgrammeView, ResourceTypes: []model.ResourceType{model.ResourceAcademicUnit, model.ResourceProgramme}, AcademicUnitIDs: []model.AcademicUnitID{fixture.childID}},
	}, got, "institution.manage has no unit authority and role_binding.manage is PAT-forbidden")
}

// TestGrantablePersonalAccessTokenScopesNeverOfferWhatCreationRefuses is the
// catalogue's safety contract: every offered placement is accepted by
// CreatePersonalAccessToken for the same principal, and unoffered placements
// of an offered action are refused.
func TestGrantablePersonalAccessTokenScopesNeverOfferWhatCreationRefuses(t *testing.T) {
	t.Parallel()

	fixture := newGrantableScopeFixture(t)
	service := fixture.service(t, fixture.access)
	caller := invocation.New(personalAccessTokenSessionPrincipal(fixture.userID, fixture.now), model.RequestMetadata{})
	catalogue, err := service.ListGrantablePersonalAccessTokenScopes(context.Background(), caller, ListGrantablePersonalAccessTokenScopesQuery{})
	require.NoError(t, err)
	require.NotEmpty(t, catalogue.Scopes)

	create := func(action model.Action, unitID model.AcademicUnitID) error {
		_, createErr := service.CreatePersonalAccessToken(context.Background(), caller, CreatePersonalAccessTokenCommand{
			Description: "catalogue check", Scopes: []string{string(action)}, AcademicUnitID: unitID,
			ExpiresAt: fixture.now.Add(time.Hour).UnixMilli(),
		})
		return createErr
	}
	units := []model.AcademicUnitID{fixture.parentID, fixture.childID, fixture.siblingID}
	for _, scope := range catalogue.Scopes {
		name := fmt.Sprintf("%s institution", scope.Action)
		if scope.InstitutionWide {
			require.NoError(t, create(scope.Action, ""), name)
		} else {
			require.True(t, apperror.Is(create(scope.Action, ""), "personal_access_token.invalid"), name)
		}
		for _, unitID := range units {
			name := fmt.Sprintf("%s at %s", scope.Action, unitID)
			offered := scope.InstitutionWide
			for _, id := range scope.AcademicUnitIDs {
				offered = offered || id == unitID
			}
			if offered {
				require.NoError(t, create(scope.Action, unitID), name)
				continue
			}
			require.True(t, apperror.Is(create(scope.Action, unitID), "personal_access_token.invalid"), name)
		}
	}
	for _, action := range []model.Action{model.ActionInstitutionManage, model.ActionClassView} {
		require.True(t, apperror.Is(create(action, fixture.parentID), "personal_access_token.invalid"), "unoffered %s", action)
	}
}

func TestGrantablePersonalAccessTokenScopesRequireInteractiveSession(t *testing.T) {
	t.Parallel()

	fixture := newGrantableScopeFixture(t)
	service := fixture.service(t, fixture.access)
	principal := model.Principal{
		UserID: fixture.userID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticationMethod: "personal_access_token",
		ClientType: model.SessionClientCLI, CredentialScopes: []string{string(model.ActionExamView)},
	}
	_, err := service.ListGrantablePersonalAccessTokenScopes(
		context.Background(), invocation.New(principal, model.RequestMetadata{}), ListGrantablePersonalAccessTokenScopesQuery{},
	)
	require.True(t, apperror.Is(err, "authentication.session_required"), "error = %v", err)

	// A non-recent session may read the catalogue; creation alone requires recency.
	stale := personalAccessTokenSessionPrincipal(fixture.userID, fixture.now.Add(-24*time.Hour))
	_, err = service.ListGrantablePersonalAccessTokenScopes(
		context.Background(), invocation.New(stale, model.RequestMetadata{}), ListGrantablePersonalAccessTokenScopesQuery{},
	)
	require.NoError(t, err)
}

func TestGrantablePersonalAccessTokenScopesBoundListedAcademicUnits(t *testing.T) {
	t.Parallel()

	fixture := newGrantableScopeFixture(t)
	roots := make([]model.AcademicUnitID, 0, personalAccessTokenGrantableAcademicUnitLimit+2)
	fixture.units.units = map[model.AcademicUnitID]*model.AcademicUnit{}
	for index := range personalAccessTokenGrantableAcademicUnitLimit + 2 {
		id := model.NewAcademicUnitID()
		fixture.units.units[id] = &model.AcademicUnit{ID: id, DisplayName: fmt.Sprintf("Unit %03d", index)}
		roots = append(roots, id)
	}
	slices.Sort(roots)
	authorization := &personalAccessTokenScopeAuthorizerFake{delegation: access.PersonalAccessTokenDelegation{
		AcademicUnitRootIDs: roots,
		Actions: []access.PersonalAccessTokenActionDelegation{
			{Action: model.ActionExamView, AcademicUnitIDs: roots},
			{Action: model.ActionAuditView, InstitutionWide: true},
		},
	}}
	service := fixture.service(t, authorization)
	catalogue, err := service.ListGrantablePersonalAccessTokenScopes(
		context.Background(),
		invocation.New(personalAccessTokenSessionPrincipal(fixture.userID, fixture.now), model.RequestMetadata{}),
		ListGrantablePersonalAccessTokenScopesQuery{},
	)
	require.NoError(t, err)
	require.True(t, catalogue.AcademicUnitsHasMore)
	require.Len(t, catalogue.AcademicUnits, personalAccessTokenGrantableAcademicUnitLimit)
	require.Equal(t, "Unit 000", catalogue.AcademicUnits[0].DisplayName)
	require.Equal(t, "Unit 049", catalogue.AcademicUnits[len(catalogue.AcademicUnits)-1].DisplayName)
	require.Len(t, catalogue.Scopes, 2)
	require.Len(t, catalogue.Scopes[0].AcademicUnitIDs, personalAccessTokenGrantableAcademicUnitLimit, "scope units follow the listed roots")
	require.True(t, catalogue.Scopes[1].InstitutionWide)
	require.Empty(t, catalogue.Scopes[1].AcademicUnitIDs)
}
