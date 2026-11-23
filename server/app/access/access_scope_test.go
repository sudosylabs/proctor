// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type accessAcademicPeriodStoreFake struct {
	store.AcademicPeriodStore

	period  *model.AcademicPeriod
	periods map[string]*model.AcademicPeriod
	gets    int
	err     error
}

func (s *accessAcademicPeriodStoreFake) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	s.gets++
	if s.err != nil {
		return nil, s.err
	}
	if period := s.periods[id.String()]; period != nil {
		return period, nil
	}
	if s.period == nil {
		return nil, store.NewErrNotFound("academic_period", "missing")
	}
	return s.period, nil
}

func TestAccessControlAcademicPeriodPreflightDeniesBeforeInspection(t *testing.T) {
	t.Parallel()

	institutionID := model.NewInstitutionID()
	unitID := model.NewAcademicUnitID()
	periods := &accessAcademicPeriodStoreFake{err: errors.New("academic period target must not be read")}
	units := &accessAcademicUnitStoreFake{err: errors.New("academic period owner must not be read")}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: units, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: periods})
	require.NoError(t, err)
	access, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{}, Bindings: &accessRoleBindingStoreFake{}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}
	inv := invocation.New(principal, model.RequestMetadata{})
	resource := model.Resource{Type: model.ResourceAcademicPeriod, ID: model.NewAcademicPeriodID().String()}
	err = access.authorizeCurrentState(context.Background(), principal, model.ActionAcademicPeriodView, resource, inv.RequestMetadata())
	require.True(t, apperror.Is(err, "authorization.denied"), "target preflight error = %v, want authorization.denied", err)
	period := &model.AcademicPeriod{
		ID: model.NewAcademicPeriodID(), Owner: model.NewAcademicUnitAcademicPeriodOwner(unitID),
		CreatedAt: model.NowUTC(), UpdatedAt: model.NowUTC(), Revision: 1,
		Name: "period", DisplayName: "Period", StartsAt: model.TimeFromMillis(1), EndsAt: model.TimeFromMillis(2),
	}
	err = access.AuthorizeAcademicPeriodOwner(context.Background(), inv, model.ActionAcademicPeriodManage, period)
	require.True(t, apperror.Is(err, "authorization.denied"), "owner preflight error = %v, want authorization.denied", err)
	require.Equal(t, 0, periods.gets, "preflight inspected target or owner: period gets")
	require.Equal(t, 0, units.listAncestors, "preflight inspected target or owner: period gets")
}

func TestAccessControlMembershipPreflightDeniesBeforeOpaqueTargetInspection(t *testing.T) {
	t.Parallel()

	institutionID := model.NewInstitutionID()
	classes := &accessClassStoreFake{}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: &accessAcademicUnitStoreFake{}, Classes: classes, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	access, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{}, Bindings: &accessRoleBindingStoreFake{}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}
	inv := invocation.New(principal, model.RequestMetadata{})
	err = access.AuthorizePreflight(
		context.Background(), inv, model.ActionClassMembersManage, model.ResourceClass,
	)
	require.True(t, apperror.Is(err, "authorization.denied"), "preflight error = %v, want authorization.denied", err)
	require.Equal(t, 0, classes.gets, "preflight inspected")
}

func TestAccessControlAcademicPeriodScopeInheritanceAndInstitutionView(t *testing.T) {
	t.Parallel()
	institutionID := model.NewInstitutionID()
	rootID, childID, siblingID := model.NewAcademicUnitID(), model.NewAcademicUnitID(), model.NewAcademicUnitID()
	unitPeriodID, siblingPeriodID, institutionPeriodID := model.NewAcademicPeriodID(), model.NewAcademicPeriodID(), model.NewAcademicPeriodID()
	now := model.NowUTC()
	periods := map[string]*model.AcademicPeriod{
		unitPeriodID.String(): {
			ID: unitPeriodID, Owner: model.NewAcademicUnitAcademicPeriodOwner(childID), CreatedAt: now, UpdatedAt: now, Revision: 1,
			Name: "unit", DisplayName: "Unit", StartsAt: model.TimeFromMillis(1), EndsAt: model.TimeFromMillis(2),
		},
		siblingPeriodID.String(): {
			ID: siblingPeriodID, Owner: model.NewAcademicUnitAcademicPeriodOwner(siblingID), CreatedAt: now, UpdatedAt: now, Revision: 1,
			Name: "sibling", DisplayName: "Sibling", StartsAt: model.TimeFromMillis(1), EndsAt: model.TimeFromMillis(2),
		},
		institutionPeriodID.String(): {
			ID: institutionPeriodID, Owner: model.NewInstitutionAcademicPeriodOwner(institutionID), CreatedAt: now, UpdatedAt: now, Revision: 1,
			Name: "institution", DisplayName: "Institution", StartsAt: model.TimeFromMillis(1), EndsAt: model.TimeFromMillis(2),
		},
	}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		rootID.String():    {{ID: rootID, InstitutionID: institutionID}},
		childID.String():   {{ID: childID, InstitutionID: institutionID}, {ID: rootID, InstitutionID: institutionID}},
		siblingID.String(): {{ID: siblingID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{periods: periods}})
	require.NoError(t, err)
	userID, roleID := model.NewUserID(), model.NewRoleID()
	access, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{{ID: roleID, Permissions: []string{string(model.ActionAcademicPeriodView), string(model.ActionAcademicPeriodManage)}}}}, Bindings: &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{{RoleID: roleID, UserID: userID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: rootID.String()}}}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	principal := model.Principal{
		UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: now, ClientType: model.SessionClientWeb,
	}
	unitResource := model.Resource{Type: model.ResourceAcademicPeriod, ID: unitPeriodID.String()}
	allowed, err := access.Can(context.Background(), principal, model.ActionAcademicPeriodView, unitResource)
	require.NoError(t, err, "descendant view")
	require.True(t, allowed, "descendant view")
	allowed, err = access.Can(context.Background(), principal, model.ActionAcademicPeriodManage, unitResource)
	require.NoError(t, err, "descendant manage")
	require.True(t, allowed, "descendant manage")
	siblingResource := model.Resource{Type: model.ResourceAcademicPeriod, ID: siblingPeriodID.String()}
	allowed, err = access.Can(context.Background(), principal, model.ActionAcademicPeriodView, siblingResource)
	require.NoError(t, err, "cross-subtree view")
	require.False(t, allowed, "cross-subtree view")
	inv := invocation.New(principal, model.RequestMetadata{})
	require.NoError(t, access.authorizeCurrentState(context.Background(), principal, model.ActionAcademicPeriodView, unitResource, inv.RequestMetadata()))
	err = access.authorizeCurrentState(context.Background(), principal, model.ActionAcademicPeriodView, siblingResource, inv.RequestMetadata())
	require.True(t, apperror.Is(err, "authorization.denied"), "preflight plus terminal cross-subtree view = %v", err)
	institutionResource := model.Resource{Type: model.ResourceAcademicPeriod, ID: institutionPeriodID.String()}
	allowed, err = access.Can(context.Background(), principal, model.ActionAcademicPeriodView, institutionResource)
	require.NoError(t, err, "applicable institution view")
	require.True(t, allowed, "applicable institution view")
	allowed, err = access.Can(context.Background(), principal, model.ActionAcademicPeriodManage, institutionResource)
	require.NoError(t, err, "institution manage from unit")
	require.False(t, allowed, "institution manage from unit")
}

func TestAccessControlGrantsExamViewThroughAcademicUnitScope(t *testing.T) {
	t.Parallel()
	institutionID := model.NewInstitutionID()
	unitID, examID, userID, roleID := model.NewAcademicUnitID(), model.NewExamID(), model.NewUserID(), model.NewRoleID()
	exam, err := model.NewExam(examID, unitID, model.NewUserID(), model.NowUTC())
	require.NoError(t, err)
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		unitID.String(): {{ID: unitID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{exam: exam}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	access, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{{ID: roleID, Permissions: []string{string(model.ActionExamView)}}}}, Bindings: &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{{RoleID: roleID, UserID: userID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}}}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	principal := model.Principal{
		UserID: userID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, SessionID: model.NewSessionID(),
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}
	allowed, err := access.Can(context.Background(), principal, model.ActionExamView, model.Resource{Type: model.ResourceExam, ID: examID.String()})
	require.NoError(t, err, "exam view")
	require.True(t, allowed, "exam view")
}

func TestAccessControlGrantsExamCreateOverrideThroughInstitutionScope(t *testing.T) {
	t.Parallel()
	institutionID := model.NewInstitutionID()
	unitID, userID, roleID := model.NewAcademicUnitID(), model.NewUserID(), model.NewRoleID()
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		unitID.String(): {{ID: unitID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	access, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{{ID: roleID, Permissions: []string{string(model.ActionExamCreateOverride)}}}}, Bindings: &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{{RoleID: roleID, UserID: userID, ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String()}}}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	principal := model.Principal{
		UserID: userID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, SessionID: model.NewSessionID(),
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}
	allowed, err := access.Can(context.Background(), principal, model.ActionExamCreateOverride, model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()})
	require.NoError(t, err, "exam create override")
	require.True(t, allowed, "exam create override")
}

func TestAccessControlUsesNativeRoleBindingDecisionTime(t *testing.T) {
	t.Parallel()

	institutionID, unitID := model.NewInstitutionID(), model.NewAcademicUnitID()
	userID, roleID := model.NewUserID(), model.NewRoleID()
	start := time.Date(2026, 9, 5, 12, 0, 0, 123200000, time.FixedZone("offset", 3600))
	end := start.Add(time.Second + 500*time.Microsecond)
	binding := &model.RoleBinding{
		RoleID: roleID, UserID: userID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institutionID.String(), StartsAt: start, EndsAt: model.OptionalTimeFrom(end),
	}
	principal := model.Principal{
		UserID: userID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, SessionID: model.NewSessionID(),
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: start.Add(-time.Hour), ClientType: model.SessionClientWeb,
	}
	for _, test := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "before start", at: start.Add(-time.Microsecond)},
		{name: "at start", at: start, want: true},
		{name: "before end", at: end.Add(-time.Microsecond), want: true},
		{name: "submicrosecond before end", at: end.Add(-time.Nanosecond), want: true},
		{name: "at end", at: end},
		{name: "after end within millisecond", at: end.Add(time.Microsecond)},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
				unitID.String(): {{ID: unitID, InstitutionID: institutionID}},
			}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
			require.NoError(t, err)
			bindings := &accessRoleBindingStoreFake{lookup: func(at time.Time) ([]*model.RoleBinding, error) {
				if binding.IsActiveAt(at) {
					return []*model.RoleBinding{binding}, nil
				}
				return nil, nil
			}}
			authorization, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{{ID: roleID, Permissions: []string{string(model.ActionAcademicUnitView)}}}}, Bindings: bindings, Resolver: resolver, Audit: decisionAuditor()})
			require.NoError(t, err)
			authorization.now = func() time.Time { return test.at }
			allowed, err := authorization.Can(context.Background(), principal, model.ActionAcademicUnitView,
				model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()})
			require.NoError(t, err, "Can")
			require.Equal(t, test.want, allowed, "Can")
			scope, err := authorization.GrantedScopesAt(context.Background(), principal,
				model.ActionAcademicUnitView, model.ResourceAcademicUnit, test.at)
			require.NoError(t, err, "authorizedScopesAt = %#v, %v; want institution-wide", scope, err)
			require.Equal(t, test.want, scope.InstitutionWide, "authorizedScopesAt = %#v, %v; want institution-wide", scope, err)
			wantAt := model.TimeUTC(test.at)
			require.Equal(t, []time.Time{wantAt, wantAt}, bindings.activeAt, "binding decision times = %v, want %v twice", bindings.activeAt, wantAt)
		})
	}
}

func TestAccessScopeConstraintsAreBoundedAndRespectPATCeiling(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 13, 0, 0, 0, time.UTC)
	institutionID := model.NewInstitutionID()
	rootID := model.NewAcademicUnitID()
	childID := model.NewAcademicUnitID()
	roleID := model.RoleID(model.NewID())
	userID := model.NewUserID()
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		childID.String(): {{ID: rootID, InstitutionID: institutionID}, {ID: childID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	authorization, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{{ID: roleID, Permissions: []string{string(model.ActionAcademicUnitView)}}}}, Bindings: &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{{RoleID: roleID, UserID: userID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: childID.String()}}}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	authorization.now = func() time.Time { return now }
	principal := model.Principal{
		UserID: userID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType:       model.CredentialPersonalAccessToken,
		AuthenticationMethod: "personal_access_token", ClientType: model.SessionClientCLI,
		CredentialScopes: []string{string(model.ActionAcademicUnitView)}, AcademicUnitID: rootID,
	}
	query, err := authorization.GrantedScopes(
		context.Background(), principal, model.ActionAcademicUnitView,
		model.ResourceAcademicUnit,
	)
	require.NoError(t, err)
	require.Len(t, query.AcademicUnitRootIDs, 1)
	require.Equal(t, childID, query.AcademicUnitRootIDs[0])
	require.False(t, query.InstitutionWide)
	principal.CredentialScopes = []string{string(model.ActionClassView)}
	query, err = authorization.GrantedScopes(context.Background(), principal, model.ActionAcademicUnitView, model.ResourceAcademicUnit)
	require.NoError(t, err, "ceiling query=%#v err", query)
	require.False(t, query.InstitutionWide, "ceiling query=%#v err", query)
	require.Empty(t, query.AcademicUnitRootIDs, "ceiling query=%#v err", query)
	_, err = authorization.GrantedScopes(context.Background(), principal, model.ActionAcademicUnitView, model.ResourceClass)
	require.True(t, apperror.Is(err, "authorization.request.invalid"), "compatibility error = %v", err)
}

func TestUserVisibilityScopeKeepsRelationshipAndClassMemberAuthoritySeparate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 18, 12, 0, 0, 123456789, time.FixedZone("offset", 3600))
	institutionID := model.NewInstitutionID()
	unitID := model.NewAcademicUnitID()
	userID := model.NewUserID()
	userViewRoleID, classMembersRoleID := model.NewRoleID(), model.NewRoleID()
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: &accessAcademicUnitStoreFake{ancestors: map[string][]*model.AcademicUnit{
		unitID.String(): {{ID: unitID, InstitutionID: institutionID}},
	}}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	bindings := &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{
		{RoleID: userViewRoleID, UserID: userID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()},
		{RoleID: classMembersRoleID, UserID: userID, ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String()},
	}}
	authorization, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{
		{ID: userViewRoleID, Permissions: []string{string(model.ActionUserView)}},
		{ID: classMembersRoleID, Permissions: []string{string(model.ActionClassMembersView)}},
	}}, Bindings: bindings, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	clockCalls := 0
	authorization.now = func() time.Time {
		clockCalls++
		return now.Add(time.Duration(clockCalls-1) * time.Microsecond)
	}
	principal := model.Principal{
		UserID: userID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, SessionID: model.NewSessionID(),
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: now, ClientType: model.SessionClientWeb,
	}

	visibility, err := authorization.userVisibilityScope(context.Background(), principal)
	require.NoError(t, err)
	require.False(t, visibility.InstitutionWide)
	require.True(t, visibility.ClassMemberInstitutionWide)
	require.Equal(t, []model.AcademicUnitID{unitID}, visibility.AcademicUnitRootIDs)
	require.Empty(t, visibility.ClassMemberAcademicUnitRootIDs)
	require.Empty(t, visibility.ClassIDs)
	require.True(t, visibility.ActiveAt.Equal(model.TimeUTC(now)))
	wantAt := model.TimeUTC(now)
	require.Equal(t, 1, clockCalls, "clock calls/binding decision times = %d/%v, want one clock call and %v twice", clockCalls, bindings.activeAt, wantAt)
	require.Equal(t, []time.Time{wantAt, wantAt}, bindings.activeAt, "clock calls/binding decision times = %d/%v, want one clock call and %v twice", clockCalls, bindings.activeAt, wantAt)
}

func TestUserReadMatchesInstitutionWideClassMemberVisibility(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	institutionID := model.NewInstitutionID()
	viewerID, targetID := model.NewUserID(), model.NewUserID()
	roleID := model.NewRoleID()
	classID := model.NewClassID()
	users := &accessUserStoreFake{match: store.UserVisibilityMatch{
		ScopeType: model.RoleScopeClass, ScopeID: classID.String(),
	}}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}, AcademicUnits: &accessAcademicUnitStoreFake{}, Classes: &accessClassStoreFake{}, Users: users, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	authorization, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{roles: []*model.Role{{
		ID: roleID, Permissions: []string{string(model.ActionClassMembersView)},
	}}}, Bindings: &accessRoleBindingStoreFake{bindings: []*model.RoleBinding{{
		RoleID: roleID, UserID: viewerID, ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String(),
	}}}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	authorization.now = func() time.Time { return now }
	principal := model.Principal{
		UserID: viewerID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, SessionID: model.NewSessionID(),
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: now, ClientType: model.SessionClientWeb,
	}

	fullProfile, err := authorization.authorizeUserRead(
		context.Background(), invocation.New(principal, model.RequestMetadata{}), targetID, false,
	)
	require.NoError(t, err)
	require.False(t, fullProfile, "fullProfile=%t calls=%d visibility=%#v", fullProfile, users.matchCalls, users.matchVisibility)
	require.Equal(t, 1, users.matchCalls, "fullProfile=%t calls=%d visibility=%#v", fullProfile, users.matchCalls, users.matchVisibility)
	require.True(t, users.matchVisibility.ClassMemberInstitutionWide, "fullProfile=%t calls=%d visibility=%#v", fullProfile, users.matchCalls, users.matchVisibility)
	require.False(t, users.matchVisibility.InstitutionWide, "fullProfile=%t calls=%d visibility=%#v", fullProfile, users.matchCalls, users.matchVisibility)
}

func TestAccessControlPreservesIntrinsicSelfRead(t *testing.T) {
	t.Parallel()

	institution := &model.Institution{ID: model.NewInstitutionID()}
	user := &model.User{
		ID: model.NewUserID(), Username: "self-reader", Email: "self@example.edu",
		CreatedAt: model.NowUTC(), UpdatedAt: model.NowUTC(), Revision: 1,
	}
	resolver, err := NewScopeResolver(ScopeResolverDependencies{Institutions: &accessInstitutionStoreFake{institution: institution}, AcademicUnits: &accessAcademicUnitStoreFake{}, Classes: &accessClassStoreFake{}, Users: &accessUserStoreFake{user: user}, ClassMembers: &accessClassMemberStoreFake{}, Exams: &accessExamAuthoringStoreFake{}, Sittings: &accessExamSittingStoreFake{}, Submissions: &accessExamSubmissionStoreFake{}, AcademicPeriods: &accessAcademicPeriodStoreFake{}})
	require.NoError(t, err)
	access, err := NewAuthorizer(AuthorizerDependencies{Roles: &accessRoleStoreFake{}, Bindings: &accessRoleBindingStoreFake{}, Resolver: resolver, Audit: decisionAuditor()})
	require.NoError(t, err)
	principal := model.Principal{
		UserID: user.ID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess,
		SessionID:      model.SessionID(model.NewID()), AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt:        model.NowUTC(), ClientType: model.SessionClientWeb,
	}
	resource := model.Resource{Type: model.ResourceUser, ID: user.ID.String()}
	allowed, err := access.Can(context.Background(), principal, model.ActionUserView, resource)
	require.NoError(t, err, "self read")
	require.True(t, allowed, "self read")
	allowed, err = access.Can(context.Background(), principal, model.ActionUserManage, resource)
	require.NoError(t, err, "self management")
	require.False(t, allowed, "self management")
}

type accessInstitutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
	err         error
}

func (s *accessInstitutionStoreFake) Get(context.Context, model.InstitutionID) (*model.Institution, error) {
	return s.institution, s.err
}
func (s *accessInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, s.err
}

type accessAcademicUnitStoreFake struct {
	store.AcademicUnitStore
	ancestors     map[string][]*model.AcademicUnit
	listAncestors int
	err           error
}

func (s *accessAcademicUnitStoreFake) ListAncestors(_ context.Context, id model.AcademicUnitID) ([]*model.AcademicUnit, error) {
	s.listAncestors++
	return s.ancestors[id.String()], s.err
}

type accessClassStoreFake struct {
	store.ClassStore
	gets int
}

func (s *accessClassStoreFake) Get(context.Context, model.ClassID) (*model.Class, error) {
	s.gets++
	return nil, store.NewErrNotFound("class", "")
}

type accessProgrammeStoreFake struct {
	store.ProgrammeStore
	programme *model.Programme
}

func (s *accessProgrammeStoreFake) Get(context.Context, model.ProgrammeID) (*model.Programme, error) {
	if s.programme == nil {
		return nil, store.NewErrNotFound("programme", "")
	}
	return s.programme, nil
}

type accessProgrammeLevelStoreFake struct {
	store.ProgrammeLevelStore
	level *model.ProgrammeLevel
}

func (s *accessProgrammeLevelStoreFake) Get(context.Context, model.ProgrammeLevelID) (*model.ProgrammeLevel, error) {
	if s.level == nil {
		return nil, store.NewErrNotFound("programme_level", "")
	}
	return s.level, nil
}

type accessUserStoreFake struct {
	store.UserStore
	user            *model.User
	match           store.UserVisibilityMatch
	matchVisibility store.UserVisibilityScope
	matchCalls      int
}

func (s *accessUserStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	if s.user == nil {
		return nil, store.NewErrNotFound("user", "")
	}
	return s.user, nil
}

func (s *accessUserStoreFake) MatchVisibility(_ context.Context, _ model.UserID, visibility store.UserVisibilityScope) (store.UserVisibilityMatch, error) {
	s.matchCalls++
	s.matchVisibility = visibility
	return s.match, nil
}

type accessClassMemberStoreFake struct{ store.ClassMemberStore }
type accessExamAuthoringStoreFake struct {
	store.ExamAuthoringStore
	exam *model.Exam
}

func (s *accessExamAuthoringStoreFake) Resolve(context.Context, model.ExamID) (*model.Exam, error) {
	return s.exam, nil
}

type accessExamSittingStoreFake struct {
	store.ExamSittingStore
	snapshot *store.ExamSittingSnapshot
}

type accessExamSubmissionStoreFake struct {
	store.ExamSubmissionStore
	authorization *store.ExamSubmissionAuthorization
}

func (s *accessExamSubmissionStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	if s.authorization == nil {
		return nil, store.NewErrNotFound("submission", "")
	}
	return s.authorization, nil
}

func (s *accessExamSittingStoreFake) Resolve(context.Context, model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if s.snapshot == nil {
		return nil, store.NewErrNotFound("exam_sitting", "")
	}
	return s.snapshot, nil
}

type accessRoleStoreFake struct {
	store.RoleStore
	roles []*model.Role
}

func (s *accessRoleStoreFake) GetByIDs(context.Context, []model.RoleID) ([]*model.Role, error) {
	return s.roles, nil
}

type accessRoleBindingStoreFake struct {
	store.RoleBindingStore
	bindings []*model.RoleBinding
	activeAt []time.Time
	lookup   func(time.Time) ([]*model.RoleBinding, error)
}

func (s *accessRoleBindingStoreFake) ListActiveByUser(_ context.Context, _ model.UserID, at time.Time) ([]*model.RoleBinding, error) {
	s.activeAt = append(s.activeAt, at)
	if s.lookup != nil {
		return s.lookup(at)
	}
	return s.bindings, nil
}

// decisionAuditor is a real Auditor over a store that keeps the last
// decision, so the authorizer records decisions as production does.
func decisionAuditor() *Auditor {
	return &Auditor{deps: AuditorDependencies{Audits: &savingAuditStore{}, NodeID: "test-node"}, now: time.Now}
}
