// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// userDirectoryFake serves pages of matching Users, total in all, in pages of
// the query's limit, and records each query; a set err refuses every query.
type userDirectoryFake struct {
	total   int
	queries []user.SearchUsersQuery
	err     error
}

func (f *userDirectoryFake) SearchUserPage(_ context.Context, _ invocation.Invocation, query user.SearchUsersQuery) (paging.Page[*model.User], error) {
	f.queries = append(f.queries, query)
	if f.err != nil {
		return paging.Page[*model.User]{}, f.err
	}
	served := 0
	for _, previous := range f.queries[:len(f.queries)-1] {
		served += previous.Limit
	}
	page := paging.Page[*model.User]{}
	for index := served; index < f.total && len(page.Items) < query.Limit; index++ {
		page.Items = append(page.Items, &model.User{ID: model.NewUserID(), Username: "user"})
	}
	page.HasMore = served+len(page.Items) < f.total
	return page, nil
}

type auditDrafterFake struct{}

func (auditDrafterFake) PrepareAtScope(context.Context, invocation.Invocation, model.Action, model.Resource, model.RoleScopeType, string, string, map[string]any) (*model.AuditEvent, error) {
	return &model.AuditEvent{Status: model.AuditStatusAttempt}, nil
}

// previewImportStoreFake serves one import and records whether a preview
// row was read through it.
type previewImportStoreFake struct {
	store.OnboardingImportStore
	value *store.OnboardingImport
}

func (f previewImportStoreFake) GetOnboardingImport(context.Context, model.OnboardingImportID) (*store.OnboardingImport, error) {
	return f.value, nil
}

// previewUserListFake answers the User directory read an execution check
// makes: the listed Users are visible, every other User is not.
type previewUserListFake struct {
	store.UserStore
	visible map[model.UserID]bool
	options []store.UserListOptions
}

func (f *previewUserListFake) List(_ context.Context, options store.UserListOptions) ([]*model.User, error) {
	f.options = append(f.options, options)
	if f.visible[options.ID] {
		return []*model.User{{ID: options.ID}}, nil
	}
	return nil, nil
}

func previewPrincipal(strength model.AuthenticationStrength, at time.Time) model.Principal {
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: strength,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at}
	if strength == model.AuthenticationMultiFactor {
		principal.MFACompletedAt = model.OptionalTimeFrom(at)
	}
	return principal
}

func TestAcademicAdministrationPreviewFilterAdmitsOnlyTheOperationsOwnFields(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	classID, institutionID := model.NewClassID().String(), model.NewInstitutionID().String()
	valid := CreateAcademicAdministrationPreviewCommand{Operation: AcademicAdministrationClassEnroll, ScopeType: model.RoleScopeClass, ScopeID: classID,
		StartAt: model.OptionalTimeFrom(at), EndAt: model.OptionalTimeFrom(at.Add(time.Hour)),
		Filter: AcademicAdministrationUserFilter{Affiliations: []model.AffiliationKind{model.AffiliationStudent, model.AffiliationStudent}}}
	filter, err := academicAdministrationPreviewFilter(valid, at)
	require.NoError(t, err)
	require.Equal(t, []model.AffiliationKind{model.AffiliationStudent}, filter.Affiliations, "repeats are dropped")

	for name, test := range map[string]struct {
		mutate func(*CreateAcademicAdministrationPreviewCommand)
		field  string
	}{
		"transfer": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.Operation = AcademicAdministrationClassTransfer
		}, "operation"},
		"unknown operation": {func(command *CreateAcademicAdministrationPreviewCommand) { command.Operation = "user.delete" }, "operation"},
		"another scope": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.ScopeType, command.ScopeID = model.RoleScopeInstitution, institutionID
		}, "scope_id"},
		"start on an end": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.Operation, command.EndAt = AcademicAdministrationClassEnd, model.OptionalTime{}
		}, "start_at"},
		"end before start": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.EndAt = model.OptionalTimeFrom(at.Add(-time.Hour))
		}, "end_at"},
		"end before creation without a start": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.StartAt, command.EndAt = model.OptionalTime{}, model.OptionalTimeFrom(at.Add(-time.Minute))
		}, "end_at"},
		"end at creation without a start": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.StartAt, command.EndAt = model.OptionalTime{}, model.OptionalTimeFrom(at)
		}, "end_at"},
		"affiliation kind on an enrollment": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.AffiliationKind = model.AffiliationStudent
		}, "affiliation_kind"},
		"Role on an enrollment": {func(command *CreateAcademicAdministrationPreviewCommand) { command.RoleID = model.NewRoleID() }, "role_id"},
		"Role Binding without a Role": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.Operation = AcademicAdministrationRoleBindingCreate
		}, "role_id"},
		"long search term": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.Filter.Query = strings.Repeat("a", academicAdministrationFilterQueryMaximumRunes+1)
		}, "q"},
		"unknown state": {func(command *CreateAcademicAdministrationPreviewCommand) { command.Filter.State = "archived" }, "state"},
		"never signed in with a bound": {func(command *CreateAcademicAdministrationPreviewCommand) {
			command.Filter.NeverSignedIn, command.Filter.LastActiveAfter = true, at
		}, "never_signed_in"},
		"too many Roles": {func(command *CreateAcademicAdministrationPreviewCommand) {
			for range store.MaximumUserListRoles + 1 {
				command.Filter.RoleIDs = append(command.Filter.RoleIDs, model.NewRoleID())
			}
		}, "role_id"},
	} {
		t.Run(name, func(t *testing.T) {
			command := valid
			command.Filter.RoleIDs = nil
			test.mutate(&command)
			_, filterErr := academicAdministrationPreviewFilter(command, at)
			failure, ok := apperror.As(filterErr)
			require.True(t, ok, "error = %v", filterErr)
			require.Equal(t, "request.invalid", failure.Code())
			require.Equal(t, test.field, failure.Fields()["field"])
		})
	}
}

func TestAcademicAdministrationPreviewRefusesMoreUsersThanOnePreviewHolds(t *testing.T) {
	t.Parallel()
	inv := invocation.New(previewPrincipal(model.AuthenticationMultiFactor, time.Now()), model.RequestMetadata{})
	filter := store.AcademicAdministrationFilter{Operation: string(AcademicAdministrationUserDisable)}

	exact := &userDirectoryFake{total: store.AcademicAdministrationPreviewMaximumRows}
	users, err := (&Service{directory: exact}).matchAcademicAdministrationPreviewUsers(context.Background(), inv, filter)
	require.NoError(t, err)
	require.Len(t, users, store.AcademicAdministrationPreviewMaximumRows)

	over := &userDirectoryFake{total: store.AcademicAdministrationPreviewMaximumRows + 1}
	_, err = (&Service{directory: over}).matchAcademicAdministrationPreviewUsers(context.Background(), inv, filter)
	require.True(t, apperror.Is(err, "academic_administration_preview.too_many_users"), "error = %v", err)
	for _, query := range over.queries {
		require.Equal(t, store.UserOrderUsername, query.Order, "the frozen order is the directory's username order")
	}
}

func TestAcademicAdministrationPreviewRequiresAStrongRecentSessionBeforeAuthorizing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	authorizer := &authorizerFake{}
	service := &Service{imports: previewImportStoreFake{}, directory: &userDirectoryFake{}, drafts: auditDrafterFake{}, authorizer: authorizer,
		recentAuthenticationTTL: 15 * time.Minute, now: func() time.Time { return now }}
	command := CreateAcademicAdministrationPreviewCommand{Operation: AcademicAdministrationUserDisable, ScopeType: model.RoleScopeInstitution,
		ScopeID: model.NewInstitutionID().String()}

	for name, test := range map[string]struct {
		principal model.Principal
		code      string
	}{
		"single factor": {previewPrincipal(model.AuthenticationSingleFactor, now), "authentication.strong_required"},
		"stale strong":  {previewPrincipal(model.AuthenticationMultiFactor, now.Add(-time.Hour)), "authentication.reauthentication_required"},
		"personal access": {func() model.Principal {
			p := previewPrincipal(model.AuthenticationMultiFactor, now)
			p.CredentialType = model.CredentialPersonalAccessToken
			return p
		}(), "authentication.token_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			inv := invocation.New(test.principal, model.RequestMetadata{})
			_, err := service.CreateAcademicAdministrationPreview(context.Background(), inv, command)
			require.True(t, apperror.Is(err, apperror.Code(test.code)), "error = %v", err)
			_, err = service.CommitAcademicAdministrationPreview(context.Background(), inv, CommitAcademicAdministrationPreviewCommand{
				ID: model.NewOnboardingImportID(), ExpectedRevision: 1, PreviewDigest: strings.Repeat("a", 64), IdempotencyKey: "commit"})
			require.True(t, apperror.Is(err, apperror.Code(test.code)), "commit error = %v", err)
		})
	}
	require.Empty(t, authorizer.actions, "nothing was authorized for a weak Session")
}

func TestAcademicAdministrationPreviewIsReadOnlyByItsCreatorThroughItsOwnSurface(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	creator := previewPrincipal(model.AuthenticationMultiFactor, now)
	value := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportAcademicAdministrationFilter,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String(),
		ActorUserID: creator.UserID, Principal: creator, Filter: &store.AcademicAdministrationFilter{Operation: "user.disable"}}
	service := &Service{imports: previewImportStoreFake{value: value}, directory: &userDirectoryFake{}, drafts: auditDrafterFake{},
		authorizer: &authorizerFake{}, identity: &academicAdministrationBatchCommandsFake{}, recentAuthenticationTTL: 15 * time.Minute,
		now: func() time.Time { return now }}

	other := invocation.New(previewPrincipal(model.AuthenticationMultiFactor, now), model.RequestMetadata{})
	_, err := service.readAcademicAdministrationPreview(context.Background(), other, value.ID)
	require.True(t, apperror.Is(err, "resource.not_found"), "another User reads the preview as absent: %v", err)
	_, err = service.CommitAcademicAdministrationPreview(context.Background(), other, CommitAcademicAdministrationPreviewCommand{
		ID: value.ID, ExpectedRevision: 1, PreviewDigest: strings.Repeat("a", 64), IdempotencyKey: "commit"})
	require.True(t, apperror.Is(err, "resource.not_found"), "another User's commit finds no preview, as its reads do: %v", err)
	_, _, err = service.GetOnboardingImport(context.Background(), invocation.New(creator, model.RequestMetadata{}), value.ID)
	require.True(t, apperror.Is(err, "resource.not_found"), "the onboarding import surface never shows a filter preview: %v", err)
	read, err := service.readAcademicAdministrationPreview(context.Background(), invocation.New(creator, model.RequestMetadata{}), value.ID)
	require.NoError(t, err)
	require.Equal(t, value.ID, read.ID)
}

// TestAcademicAdministrationPreviewRefusesAFilterTheCreatorMayNoLongerApply
// re-admits the stored filter through the User directory whenever the
// creator acts on the preview: once the directory refuses it (here an
// activity filter after Institution-wide user.view was lost), every read and
// the commit are refused, and execution leaves no row to run.
func TestAcademicAdministrationPreviewRefusesAFilterTheCreatorMayNoLongerApply(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	creator := previewPrincipal(model.AuthenticationMultiFactor, now)
	value := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportAcademicAdministrationFilter,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String(),
		ActorUserID: creator.UserID, Principal: creator, PreviewDigest: strings.Repeat("a", 64), Revision: 2,
		Filter: &store.AcademicAdministrationFilter{Operation: string(AcademicAdministrationUserDisable), NeverSignedIn: true}}
	directory := &userDirectoryFake{err: apperror.Invalid("request.invalid", "never_signed_in")}
	service := &Service{imports: previewImportStoreFake{value: value}, directory: directory, drafts: auditDrafterFake{},
		authorizer: &authorizerFake{}, identity: &academicAdministrationBatchCommandsFake{}, recentAuthenticationTTL: 15 * time.Minute,
		now: func() time.Time { return now }}
	inv := invocation.New(creator, model.RequestMetadata{})
	lost := apperror.Code("academic_administration_preview.authorization_lost")

	_, err := service.GetAcademicAdministrationPreview(context.Background(), inv, value.ID)
	require.True(t, apperror.Is(err, lost), "summary read: %v", err)
	_, err = service.ListAcademicAdministrationPreviewRows(context.Background(), inv, ListAcademicAdministrationPreviewRowsQuery{
		ID: value.ID, Class: store.AcademicAdministrationPreviewWillChange, Limit: 10})
	require.True(t, apperror.Is(err, lost), "rows read: %v", err)
	_, err = service.CommitAcademicAdministrationPreview(context.Background(), inv, CommitAcademicAdministrationPreviewCommand{
		ID: value.ID, ExpectedRevision: value.Revision, PreviewDigest: value.PreviewDigest, IdempotencyKey: "commit"})
	require.True(t, apperror.Is(err, lost), "commit: %v", err)

	guard, err := service.newAcademicAdministrationFilterRowGuard(context.Background(), inv, value)
	require.NoError(t, err)
	code, err := service.checkAcademicAdministrationFilterRow(context.Background(), value, guard,
		store.OnboardingImportRow{UserID: model.NewUserID(), Operation: value.Filter.Operation})
	require.NoError(t, err)
	require.Equal(t, "authorization.denied", code, "a remaining row fails once the filter is no longer admitted")
	for _, query := range directory.queries {
		require.True(t, query.NeverSignedIn, "the stored filter itself is admitted")
		require.Equal(t, 1, query.Limit)
	}
}

func TestAcademicAdministrationFilterRowCheckRefusesWhatThePreviewWouldNotAdmitNow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	actor := previewPrincipal(model.AuthenticationMultiFactor, now)
	visible, hidden := model.NewUserID(), model.NewUserID()
	users := &previewUserListFake{visible: map[model.UserID]bool{visible: true, actor.UserID: true}}
	current := &store.OnboardingImport{Mode: model.OnboardingImportAcademicAdministrationFilter, Principal: actor,
		Filter: &store.AcademicAdministrationFilter{Operation: string(AcademicAdministrationAffiliationAdd)}}
	service := &Service{users: users, now: func() time.Time { return now }}
	guard := &academicAdministrationFilterRowGuard{visibility: store.UserVisibilityScope{InstitutionWide: true}}

	for name, test := range map[string]struct {
		guard *academicAdministrationFilterRowGuard
		user  model.UserID
		code  string
	}{
		"visible":           {guard, visible, ""},
		"no longer visible": {guard, hidden, "authorization.denied"},
		"the caller":        {guard, actor.UserID, "authorization.denied"},
		"no visibility":     {&academicAdministrationFilterRowGuard{denied: true}, visible, "authorization.denied"},
	} {
		t.Run(name, func(t *testing.T) {
			code, err := service.checkAcademicAdministrationFilterRow(context.Background(), current, test.guard,
				store.OnboardingImportRow{UserID: test.user, Operation: current.Filter.Operation})
			require.NoError(t, err)
			require.Equal(t, test.code, code)
		})
	}
	for _, options := range users.options {
		require.True(t, options.IncludeDisabled, "a disabled User is still checked, so enabling one by filter works")
		require.Equal(t, 1, options.Limit)
	}
	_, err := service.checkAcademicAdministrationFilterRow(context.Background(), current, nil, store.OnboardingImportRow{UserID: visible})
	require.Error(t, err, "a row is never run without its page guard")
}

func TestAcademicAdministrationImportRowItemNamesOnlyWhatTheOperationOwns(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	userID, relationship, roleID := model.NewUserID(), model.NewID(), model.NewRoleID()
	row := store.OnboardingImportRow{RowNumber: 7, UserID: userID, RelationshipID: relationship, RoleID: roleID,
		AffiliationKind: model.AffiliationStaff, StartsAt: model.OptionalTimeFrom(at), EndsAt: model.OptionalTimeFrom(at.Add(time.Hour))}
	for operation, want := range map[AcademicAdministrationBatchOperation]AcademicAdministrationBatchItemCommand{
		AcademicAdministrationClassEnd:       {IdempotencyKey: "7", RelationshipID: relationship},
		AcademicAdministrationAffiliationEnd: {IdempotencyKey: "7", RelationshipID: relationship},
		AcademicAdministrationClassTransfer: {IdempotencyKey: "7", UserID: userID, RelationshipID: relationship,
			StartAt: at.UnixMilli(), EndAt: at.Add(time.Hour).UnixMilli()},
		AcademicAdministrationAffiliationAdd: {IdempotencyKey: "7", UserID: userID, AffiliationKind: model.AffiliationStaff,
			StartAt: at.UnixMilli(), EndAt: at.Add(time.Hour).UnixMilli()},
		AcademicAdministrationAcademicUnitMemberAdd: {IdempotencyKey: "7", UserID: userID, StartAt: at.UnixMilli()},
		AcademicAdministrationRoleBindingCreate: {IdempotencyKey: "7", UserID: userID, RoleID: roleID,
			StartAt: at.UnixMilli(), EndAt: at.Add(time.Hour).UnixMilli()},
		AcademicAdministrationUserSessionsRevoke: {IdempotencyKey: "7", UserID: userID},
	} {
		t.Run(string(operation), func(t *testing.T) {
			value := row
			value.Operation = string(operation)
			item := academicAdministrationImportRowItem(value)
			require.Equal(t, want, item)
			require.NoError(t, validateAcademicAdministrationBatchItem(operation, item))
		})
	}
}

func TestAcademicAdministrationPreviewEndingClassification(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		matching     []string
		reason       string
		relationship string
	}{
		"none":    {nil, store.AcademicAdministrationPreviewNoChange, ""},
		"one":     {[]string{"a"}, "", "a"},
		"several": {[]string{"a", "b"}, academicAdministrationPreviewAmbiguous, ""},
	} {
		t.Run(name, func(t *testing.T) {
			reason, relationship, err := endingClassification(test.matching)
			require.NoError(t, err)
			require.Equal(t, test.reason, reason)
			require.Equal(t, test.relationship, relationship)
		})
	}
}

// previewClassMemberFake answers a User's current Class memberships.
type previewClassMemberFake struct {
	store.ClassMemberStore
	members map[model.UserID][]*model.ClassMember
}

func (f previewClassMemberFake) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.ClassMember, error) {
	return f.members[userID], nil
}

// previewAffiliationFake answers a User's current Affiliations.
type previewAffiliationFake struct {
	store.AffiliationStore
	affiliations map[model.UserID][]*model.Affiliation
}

func (f previewAffiliationFake) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.Affiliation, error) {
	return f.affiliations[userID], nil
}

// TestAcademicAdministrationPreviewEnrollmentSkipsOnlyWhatTheCallerSees
// keeps a Class enrollment preview from telling a Class-scoped caller that a
// User is enrolled in a Class whose members it may not see, or lacks a
// student Affiliation it cannot see: such a row will change, and its
// execution refuses the enrollment as an ordinary batch item does.
func TestAcademicAdministrationPreviewEnrollmentSkipsOnlyWhatTheCallerSees(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	actor := previewPrincipal(model.AuthenticationMultiFactor, now)
	period := model.NewAcademicPeriodID()
	target, seenClass, hiddenClass := model.NewClassID(), model.NewClassID(), model.NewClassID()
	inSeenClass, inHiddenClass, withoutStudent := model.NewUserID(), model.NewUserID(), model.NewUserID()
	classMembers := previewClassMemberFake{members: map[model.UserID][]*model.ClassMember{
		inSeenClass:   {{ClassID: seenClass, AcademicPeriodID: period}},
		inHiddenClass: {{ClassID: hiddenClass, AcademicPeriodID: period}},
	}}
	student := []*model.Affiliation{{Kind: model.AffiliationStudent}}
	affiliations := previewAffiliationFake{affiliations: map[model.UserID][]*model.Affiliation{
		inSeenClass: student, inHiddenClass: student, withoutStudent: {{Kind: model.AffiliationStaff}},
	}}
	current := &store.OnboardingImport{ScopeType: model.RoleScopeClass, ScopeID: target.String(), Principal: actor, ActorUserID: actor.UserID}

	for name, test := range map[string]struct {
		institutionUsers bool
		want             map[model.UserID]string
	}{
		"Institution-wide viewer": {true, map[model.UserID]string{
			inSeenClass:    academicAdministrationPreviewEnrollmentConflict,
			inHiddenClass:  academicAdministrationPreviewEnrollmentConflict,
			withoutStudent: academicAdministrationPreviewStudentAffiliationRequired,
		}},
		"Class-scoped viewer": {false, map[model.UserID]string{
			inSeenClass:    academicAdministrationPreviewEnrollmentConflict,
			inHiddenClass:  "",
			withoutStudent: "",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			authorizer := &authorizerFake{classMembersVisible: map[model.ClassID]bool{seenClass: true}}
			service := &Service{classMembers: classMembers, affiliations: affiliations, authorizer: authorizer, now: func() time.Time { return now }}
			classifier := &academicAdministrationPreviewClassifier{service: service, current: current, period: period,
				institutionUsers: test.institutionUsers, classMembersVisible: map[model.ClassID]bool{}}
			got := map[model.UserID]string{}
			for userID := range test.want {
				reason, relationship, err := classifier.classEnrollment(context.Background(), userID, now)
				require.NoError(t, err)
				require.Empty(t, relationship)
				got[userID] = reason
			}
			require.Equal(t, test.want, got)
		})
	}
}
