// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// This conformance structure is adapted from Mattermost's per-model store
// tests. See server/NOTICE for exact provenance.

package storetest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestRoleBindingStore(t *testing.T, ss store.Store) {
	t.Run("ExpiryDeadlines", func(t *testing.T) { testRoleBindingExpiryDeadlines(t, ss) })
	t.Run("ActiveIntervalPrecision", func(t *testing.T) { testRoleBindingActiveIntervalPrecision(t, ss) })
	t.Run("VisibleSummariesByUsers", func(t *testing.T) { testRoleBindingVisibleSummariesByUsers(t, ss) })
	t.Run("TeachersByClasses", func(t *testing.T) { testRoleBindingTeachersByClasses(t, ss) })
	t.Run("ClassScopeNeedsUnitMember", func(t *testing.T) { testRoleBindingClassScopeNeedsUnitMember(t, ss) })
	t.Run("CurrentByRoles", func(t *testing.T) { testRoleBindingCurrentByRoles(t, ss) })
	t.Run("HoldersByRoles", func(t *testing.T) { testRoleBindingHoldersByRoles(t, ss) })
	t.Run("AuditedMutations", func(t *testing.T) {
		ctx := context.Background()
		institution := saveInstitution(t, ctx, ss)
		user := saveUser(t, ctx, ss)
		role, err := SaveRole(ctx, ss, &model.Role{Name: "audited-binding-role", DisplayName: "Audited", Permissions: []string{string(model.ActionClassView)}})
		require.NoError(t, err)
		at := model.NowUTC()
		_, err = ss.RoleBinding().Save(ctx, &store.RoleBindingCreation{
			Binding: &model.RoleBinding{
				UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
				ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(at),
				GrantedByUserID: user.ID, GrantedVia: model.RoleBindingGrantedManually,
			},
			AuditEventID: model.NewAuditEventID(), AuditAt: at,
		})
		require.Error(t, err, "Save succeeded without audit attempt")
		attempt, err := ss.Audit().Save(ctx, &model.AuditEvent{
			Action:    string(model.ActionRoleManage),
			Resource:  model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()},
			ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(),
			Status: model.AuditStatusAttempt, NodeID: "test-node",
		})
		require.NoError(t, err)
		staleUpdatedAt := role.UpdatedAt
		stalePermissions := append([]string(nil), role.Permissions...)
		role.DisplayName = "Audited Updated"
		role, err = UpdateRole(ctx, ss, role)
		require.NoError(t, err)
		_, err = ss.RoleBinding().Save(ctx, &store.RoleBindingCreation{
			Binding: &model.RoleBinding{
				UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
				ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(at),
				GrantedByUserID: user.ID, GrantedVia: model.RoleBindingGrantedManually,
			},
			ExpectedRoleUpdatedAt: staleUpdatedAt, ExpectedRolePermissions: stalePermissions,
			AuditEventID: attempt.ID, AuditAt: at,
		})
		require.True(t, store.IsConflict(err), "Save(stale role snapshot) error")
		attempt, err = ss.Audit().Save(ctx, &model.AuditEvent{
			Action: string(model.ActionRoleManage), Resource: model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()},
			ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), Status: model.AuditStatusAttempt, NodeID: "test-node",
		})
		require.NoError(t, err)
		createNotice := classMemberPreparedMail(t, &model.ClassMember{UserID: user.ID},
			model.MailTemplateAuthorizationInstitutionRoleAssigned, model.TimeUTC(at))
		saved, err := ss.RoleBinding().Save(ctx, &store.RoleBindingCreation{
			Binding: &model.RoleBinding{
				UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
				ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(at),
				GrantedByUserID: user.ID, GrantedVia: model.RoleBindingGrantedManually,
			},
			ExpectedRoleUpdatedAt: role.UpdatedAt, ExpectedRolePermissions: append([]string(nil), role.Permissions...),
			Recipient: new(*user), Notice: createNotice,
			AuditEventID: attempt.ID, AuditAt: at,
		})
		require.NoError(t, err)
		require.Equal(t, model.RoleBindingGrantedManually, saved.GrantedVia, "the grant keeps how it was made")
		require.Equal(t, user.ID, saved.GrantedByUserID, "the grant keeps who made it")
		require.NoError(t, requireClassMemberMail(t, ctx, ss, createNotice, model.MailTemplateAuthorizationInstitutionRoleAssigned))
		endAttempt, err := ss.Audit().Save(ctx, &model.AuditEvent{
			Action:    string(model.ActionRoleManage),
			Resource:  model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()},
			ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(),
			Status: model.AuditStatusAttempt, NodeID: "test-node",
		})
		require.NoError(t, err)
		endNotice := classMemberPreparedMail(t, &model.ClassMember{UserID: user.ID},
			model.MailTemplateAuthorizationInstitutionRoleEnded, model.TimeUTC(at.Add(1*time.Millisecond)))
		ended, err := ss.RoleBinding().End(ctx, &store.RoleBindingEnd{
			ID: saved.ID, EndAt: at.Add(1 * time.Millisecond), Recipient: new(*user), Notice: endNotice,
			AuditEventID: endAttempt.ID, AuditAt: at.Add(1 * time.Millisecond),
		})
		require.NoError(t, err)
		require.NoError(t, requireClassMemberMail(t, ctx, ss, endNotice, model.MailTemplateAuthorizationInstitutionRoleEnded))
		require.True(t, at.Add(1*time.Millisecond).Equal(ended.EndsAt.Time))
	})
	t.Run("ScopedUserProjection", func(t *testing.T) {
		ctx := context.Background()
		institution := saveInstitution(t, ctx, ss)
		root := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "binding-root")
		child := saveAcademicUnit(t, ctx, ss, institution.ID.String(), root.ID.String(), "binding-child")
		sibling := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "binding-sibling")
		programme := saveProgramme(t, ctx, ss, child.ID.String(), "binding-programme")
		level := saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "binding-level")
		period := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "binding-period", 1_800_000_000_000)
		class := saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "binding-class")
		user := saveUser(t, ctx, ss)
		role, err := SaveRole(ctx, ss, &model.Role{Name: "binding-scope-reader", DisplayName: "Reader", Permissions: []string{string(model.ActionClassView)}})
		require.NoError(t, err)
		start := model.GetMillis()
		bindings := make([]*model.RoleBinding, 0, 4)
		for _, scope := range []struct {
			kind model.RoleScopeType
			id   string
		}{
			{model.RoleScopeInstitution, institution.ID.String()},
			{model.RoleScopeAcademicUnit, child.ID.String()},
			{model.RoleScopeAcademicUnit, sibling.ID.String()},
			{model.RoleScopeClass, class.ID.String()},
		} {
			saved, saveErr := SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: user.ID, RoleID: role.ID, ScopeType: scope.kind, ScopeID: scope.id, StartsAt: model.TimeFromMillis(start)})
			require.NoError(t, saveErr)
			bindings = append(bindings, saved)
		}
		visible, err := ss.RoleBinding().ListVisibleByUser(ctx, user.ID, store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{root.ID}})
		require.NoError(t, err)
		seen := map[model.RoleBindingID]bool{}
		for _, binding := range visible {
			seen[binding.ID] = true
		}
		require.Len(t, visible, 2)
		require.True(t, seen[bindings[1].ID])
		require.True(t, seen[bindings[3].ID])
		require.False(t, seen[bindings[0].ID])
		require.False(t, seen[bindings[2].ID])
	})
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	user := saveUserWithPassword(t, ctx, ss)
	role, err := SaveRole(ctx, ss, &model.Role{
		Name: "class-reader", DisplayName: "Class Reader",
		Permissions: []string{string(model.ActionClassView)},
	})
	require.NoError(t, err)
	start := model.NowUTC()
	binding, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
		UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(start),
	})
	require.NoError(t, err)
	require.True(t, binding.ID.IsValid())
	got, err := ss.RoleBinding().Get(ctx, binding.ID)
	require.NoError(t, err)
	byUser, err := ss.RoleBinding().ListByUser(ctx, user.ID)
	require.NoError(t, err)
	byScope, err := ss.RoleBinding().ListByScope(
		ctx, model.RoleScopeInstitution, institution.ID.String(),
	)
	require.NoError(t, err)
	active, err := ss.RoleBinding().ListActiveByUser(ctx, user.ID, model.TimeUTC(start.Add(1*time.Millisecond)))
	require.NoError(t, err)
	require.Equal(t, binding.ID, got.ID, "binding queries = %#v/%d/%d", got, len(byUser), len(byScope))
	require.Len(t, byUser, 1, "binding queries = %#v/%d/%d", got, len(byUser), len(byScope))
	require.Len(t, byScope, 1, "binding queries = %#v/%d/%d", got, len(byUser), len(byScope))
	require.Len(t, active, 1, "binding queries = %#v/%d/%d", got, len(byUser), len(byScope))
	_, err = SaveRoleBinding(ctx, ss, &model.RoleBinding{
		UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(start.Add(1 * time.Millisecond)),
	})
	require.True(t, store.IsConflict(err), "overlapping Save() error")
	ended, err := EndRoleBinding(ctx, ss, binding.ID, start.Add(2*time.Millisecond))
	require.NoError(t, err)
	require.True(t, start.Add(2*time.Millisecond).Equal(ended.EndsAt.Time))
	active, err = ss.RoleBinding().ListActiveByUser(ctx, user.ID, model.TimeUTC(start.Add(3*time.Millisecond)))
	require.NoError(t, err)
	require.Empty(t, active)
	_, err = SaveRoleBinding(ctx, ss, &model.RoleBinding{
		UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeClass,
		ScopeID: model.NewID(), StartsAt: model.TimeUTC(start),
	})
	var reference *store.ErrReference
	require.ErrorAs(t, err, &reference)
	require.Equal(t, "role_bindings_class_scope_fkey", reference.Constraint)

	adminRole, err := SaveRole(ctx, ss, &model.Role{
		Name: model.SystemAdministratorRoleName, DisplayName: "System Administrator",
		Permissions: model.AllActions(), BuiltIn: true,
	})
	require.NoError(t, err)
	firstAdmin, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
		UserID: user.ID, RoleID: adminRole.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(start),
	})
	require.NoError(t, err)
	_, err = EndRoleBinding(ctx, ss, firstAdmin.ID, start.Add(10*time.Millisecond))
	require.True(t, store.IsConflict(err), "End(last system administrator) error")
	secondUser := saveUserWithPassword(t, ctx, ss)
	secondAdmin, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
		UserID: secondUser.ID, RoleID: adminRole.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(start),
	})
	require.NoError(t, err)
	_, err = EndRoleBinding(ctx, ss, firstAdmin.ID, start.Add(10*time.Millisecond))
	require.NoError(t, err)
	thirdUser := saveUserWithPassword(t, ctx, ss)
	thirdAdmin, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
		UserID: thirdUser.ID, RoleID: adminRole.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), StartsAt: model.TimeUTC(start),
	})
	require.NoError(t, err)
	startRace := make(chan struct{})
	endErrors := make(chan error, 2)
	var workers sync.WaitGroup
	for _, id := range []string{secondAdmin.ID.String(), thirdAdmin.ID.String()} {
		id := id
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-startRace
			_, endErr := EndRoleBinding(ctx, ss, model.RoleBindingID(id), start.Add(20*time.Millisecond))
			endErrors <- endErr
		}()
	}
	close(startRace)
	workers.Wait()
	close(endErrors)
	succeeded, conflicted := 0, 0
	for endErr := range endErrors {
		switch {
		case endErr == nil:
			succeeded++
		case store.IsConflict(endErr):
			conflicted++
		default:
			require.FailNow(t, fmt.Sprintf("concurrent administrator End() error = %v", endErr))
		}
	}
	require.Equal(t, 1, succeeded, "concurrent administrator End() results = success")
	require.Equal(t, 1, conflicted, "concurrent administrator End() results = success")
}

func testRoleBindingActiveIntervalPrecision(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	user := saveUser(t, ctx, ss)
	role, err := SaveRole(ctx, ss, &model.Role{
		Name: "precise-binding-reader", DisplayName: "Precise Binding Reader",
		Permissions: []string{string(model.ActionClassView)},
	})
	require.NoError(t, err)
	start := time.Date(2026, 9, 5, 12, 0, 0, 123200000, time.FixedZone("offset", 3600))
	end := start.Add(time.Second + 500*time.Microsecond)
	binding, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
		UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), StartsAt: start, EndsAt: model.OptionalTimeFrom(end),
	})
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		at     time.Time
		active bool
	}{
		{"before start", start.Add(-time.Microsecond), false},
		{"at start", start, true},
		{"before end", end.Add(-time.Microsecond), true},
		{"submicrosecond before end", end.Add(-time.Nanosecond), true},
		{"at end", end, false},
		{"after end in same millisecond", end.Add(time.Microsecond), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			active, err := ss.RoleBinding().ListActiveByUser(ctx, user.ID, test.at)
			require.NoError(t, err)
			if test.active {
				require.Len(t, active, 1, "active bindings at %v", test.at)
				require.Equal(t, binding.ID, active[0].ID, "active bindings at %v", test.at)
			} else {
				require.Empty(t, active, "active bindings at %v", test.at)
			}
		})
	}
}

// testRoleBindingTeachersByClasses pins that a Class's teachers are the
// distinct current holders of an unarchived role bound at the Class itself,
// by name, counted past the limit, for a whole page in one read.
func testRoleBindingClassScopeNeedsUnitMember(t *testing.T, ss store.Store) {
	ctx := context.Background()
	token := model.NewID()[:12]
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "member-unit-"+token)
	otherUnit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "member-other-"+token)
	programme := saveProgramme(t, ctx, ss, unit.ID.String(), "member-programme-"+token)
	level := saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "member-level-"+token)
	period := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "member-period-"+token, 1_800_000_000_000)
	class := saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "member-class-"+token)
	role, err := SaveRole(ctx, ss, &model.Role{Name: "member-teaching-" + strings.ToLower(token), DisplayName: "Teaching", Permissions: []string{string(model.ActionExamManage)}})
	require.NoError(t, err)
	start := model.TimeUTC(model.NowUTC().Add(-time.Second))
	grant := func(user *model.User, startsAt time.Time) error {
		t.Helper()
		auditID, at, auditErr := beginFixtureAudit(ctx, ss)
		require.NoError(t, auditErr)
		_, saveErr := ss.RoleBinding().Save(ctx, &store.RoleBindingCreation{
			Binding: &model.RoleBinding{
				UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeClass, ScopeID: class.ID.String(), StartsAt: startsAt,
				GrantedByUserID: user.ID, GrantedVia: model.RoleBindingGrantedManually,
			},
			ExpectedRoleUpdatedAt: role.UpdatedAt, ExpectedRolePermissions: role.Permissions,
			AuditEventID: auditID, AuditAt: at,
		})
		return saveErr
	}
	member := func(user *model.User, unitID model.AcademicUnitID, startsAt time.Time) {
		t.Helper()
		_, saveErr := seeder(ss).SeedAcademicUnitMember(ctx, &model.AcademicUnitMember{AcademicUnitID: unitID, UserID: user.ID, StartsAt: startsAt})
		require.NoError(t, saveErr)
	}
	requireRefused := func(err error, message string) {
		t.Helper()
		var conflict *store.ErrConflict
		require.ErrorAs(t, err, &conflict, message)
		require.Equal(t, "role_bindings_class_scope_member", conflict.Constraint, message)
	}

	stranger := saveUser(t, ctx, ss)
	requireRefused(grant(stranger, start), "an account outside the unit")

	elsewhere := saveUser(t, ctx, ss)
	member(elsewhere, otherUnit.ID, start)
	requireRefused(grant(elsewhere, start), "a member of another unit")

	later := saveUser(t, ctx, ss)
	member(later, unit.ID, start.Add(time.Hour))
	requireRefused(grant(later, start), "a member whose membership starts after the binding")
	require.NoError(t, grant(later, start.Add(time.Hour)), "a binding that starts with the membership")

	teacher := saveUser(t, ctx, ss)
	member(teacher, unit.ID, start)
	require.NoError(t, grant(teacher, start), "a current member of the class's unit")
}

func testRoleBindingTeachersByClasses(t *testing.T, ss store.Store) {
	ctx := context.Background()
	token := model.NewID()[:12]
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "teachers-unit-"+token)
	programme := saveProgramme(t, ctx, ss, unit.ID.String(), "teachers-programme-"+token)
	level := saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "teachers-level-"+token)
	period := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "teachers-period-"+token, 1_800_000_000_000)
	class := saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "teachers-class-"+token)
	other := saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "teachers-other-"+token)
	empty := saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "teachers-empty-"+token)
	role := func(name string, permission model.Action) *model.Role {
		saved, err := SaveRole(ctx, ss, &model.Role{Name: "teachers-" + name + "-" + strings.ToLower(token), DisplayName: name, Permissions: []string{string(permission)}})
		require.NoError(t, err)
		return saved
	}
	teaching := role("teaching", model.ActionExamManage)
	marking := role("marking", model.ActionSubmissionReview)
	retired := role("retired", model.ActionExamManage)
	observer := role("observer", model.ActionClassView)
	named := func(name string) *model.User {
		user := newUser()
		user.DisplayName = name + " " + token
		saved, err := createUser(t, ctx, ss, user)
		require.NoError(t, err)
		return saved
	}
	ada, brook, cleo, dev, eli := named("Ada"), named("Brook"), named("Cleo"), named("Dev"), named("Eli")
	start := model.NowUTC().Add(-time.Second)
	bind := func(user *model.User, role *model.Role, scopeType model.RoleScopeType, scopeID string) *model.RoleBinding {
		saved, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
			UserID: user.ID, RoleID: role.ID, ScopeType: scopeType, ScopeID: scopeID, StartsAt: model.TimeUTC(start),
		})
		require.NoError(t, err)
		return saved
	}
	// Dev holds two roles at the Class and is one teacher.
	bind(dev, teaching, model.RoleScopeClass, class.ID.String())
	bind(dev, marking, model.RoleScopeClass, class.ID.String())
	bind(brook, teaching, model.RoleScopeClass, class.ID.String())
	bind(cleo, marking, model.RoleScopeClass, class.ID.String())
	bind(ada, teaching, model.RoleScopeClass, class.ID.String())
	// Not teachers of the Class: an ended binding, an archived role, a
	// binding at the Class's unit, and a binding at another Class.
	ended := bind(eli, teaching, model.RoleScopeClass, class.ID.String())
	_, err := EndRoleBinding(ctx, ss, ended.ID, start.Add(10*time.Millisecond))
	require.NoError(t, err)
	bind(eli, retired, model.RoleScopeClass, class.ID.String())
	bind(eli, observer, model.RoleScopeClass, class.ID.String())
	_, err = ArchiveRole(ctx, ss, retired.ID, model.NowUTC())
	require.NoError(t, err)
	bind(eli, teaching, model.RoleScopeAcademicUnit, unit.ID.String())
	bind(eli, teaching, model.RoleScopeClass, other.ID.String())

	read := func(limit int) []store.ClassTeachers {
		t.Helper()
		teachers, readErr := ss.RoleBinding().ListTeachersByClasses(ctx, store.ClassTeacherOptions{
			ClassIDs: []model.ClassID{class.ID, other.ID, empty.ID}, ActiveAt: model.NowUTC(), Limit: limit,
		})
		require.NoError(t, readErr)
		return teachers
	}
	got := read(3)
	byClass := map[model.ClassID]store.ClassTeachers{}
	for _, teachers := range got {
		byClass[teachers.ClassID] = teachers
	}
	require.Len(t, got, 2, "a Class with no teacher is omitted")
	require.Equal(t, store.ClassTeachers{ClassID: class.ID, UserIDs: []model.UserID{ada.ID, brook.ID, cleo.ID}, More: 1}, byClass[class.ID],
		"the first three distinct current teachers by name, and the one past them")
	require.Equal(t, store.ClassTeachers{ClassID: other.ID, UserIDs: []model.UserID{eli.ID}}, byClass[other.ID])

	none, err := ss.RoleBinding().ListTeachersByClasses(ctx, store.ClassTeacherOptions{ActiveAt: model.NowUTC(), Limit: 3})
	require.NoError(t, err)
	require.Empty(t, none)

	var invalid *store.ErrInvalidInput
	for name, options := range map[string]store.ClassTeacherOptions{
		"missing clock": {ClassIDs: []model.ClassID{class.ID}, Limit: 3},
		"zero limit":    {ClassIDs: []model.ClassID{class.ID}, ActiveAt: model.NowUTC()},
		"invalid class": {ClassIDs: []model.ClassID{"not-an-id"}, ActiveAt: model.NowUTC(), Limit: 3},
	} {
		t.Run(name, func(t *testing.T) {
			_, readErr := ss.RoleBinding().ListTeachersByClasses(ctx, options)
			require.ErrorAs(t, readErr, &invalid)
		})
	}
}

func testRoleBindingVisibleSummariesByUsers(t *testing.T, ss store.Store) {
	ctx := context.Background()
	token := model.NewID()[:12]
	institution := saveInstitution(t, ctx, ss)
	root := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "summary-root-"+token)
	child := saveAcademicUnit(t, ctx, ss, institution.ID.String(), root.ID.String(), "summary-child-"+token)
	sibling := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "summary-sibling-"+token)
	programme := saveProgramme(t, ctx, ss, child.ID.String(), "summary-programme-"+token)
	level := saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "summary-level-"+token)
	period := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "summary-period-"+token, 1_800_000_000_000)
	class := saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "summary-class-"+token)
	role := func(name string) *model.Role {
		saved, err := SaveRole(ctx, ss, &model.Role{Name: "summary-" + strings.ToLower(name) + "-" + strings.ToLower(token), DisplayName: name, Permissions: []string{string(model.ActionClassView)}})
		require.NoError(t, err)
		return saved
	}
	reader, reviewer, marker := role("Reader"), role("Reviewer"), role("Marker")
	user := saveUser(t, ctx, ss)
	other := saveUser(t, ctx, ss)
	start := model.NowUTC().Add(-time.Second)
	offset := 0
	bind := func(user *model.User, role *model.Role, scopeType model.RoleScopeType, scopeID string) *model.RoleBinding {
		offset++
		saved, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{
			UserID: user.ID, RoleID: role.ID, ScopeType: scopeType, ScopeID: scopeID,
			StartsAt: model.TimeUTC(start.Add(time.Duration(offset) * time.Millisecond)),
		})
		require.NoError(t, err)
		return saved
	}
	bind(user, reader, model.RoleScopeInstitution, institution.ID.String())
	bind(user, reader, model.RoleScopeAcademicUnit, child.ID.String())
	bind(user, reviewer, model.RoleScopeAcademicUnit, sibling.ID.String())
	bind(user, reviewer, model.RoleScopeClass, class.ID.String())
	bind(user, marker, model.RoleScopeAcademicUnit, child.ID.String())
	endedBinding := bind(user, marker, model.RoleScopeAcademicUnit, root.ID.String())
	_, err := EndRoleBinding(ctx, ss, endedBinding.ID, start.Add(100*time.Millisecond))
	require.NoError(t, err)
	activeAt := model.TimeUTC(start.Add(time.Second))
	summary := func(role *model.Role, scopeType model.RoleScopeType, scopeID, scopeName string) store.RoleBindingSummary {
		return store.RoleBindingSummary{RoleID: role.ID, DisplayName: role.DisplayName, ScopeType: scopeType, ScopeID: scopeID, ScopeName: scopeName}
	}
	read := func(visibility store.UserVisibilityScope, limit int) []store.UserRoleBindingSummary {
		t.Helper()
		visibility.ActiveAt = activeAt
		summaries, readErr := ss.RoleBinding().ListVisibleSummariesByUsers(ctx, store.RoleBindingSummaryOptions{
			UserIDs: []model.UserID{user.ID, other.ID}, Visibility: visibility, Limit: limit,
		})
		require.NoError(t, readErr)
		return summaries
	}

	require.Equal(t, []store.UserRoleBindingSummary{{UserID: user.ID, More: 2, Bindings: []store.RoleBindingSummary{
		summary(reader, model.RoleScopeInstitution, institution.ID.String(), institution.DisplayName),
		summary(reader, model.RoleScopeAcademicUnit, child.ID.String(), child.DisplayName),
		summary(reviewer, model.RoleScopeAcademicUnit, sibling.ID.String(), sibling.DisplayName),
	}}}, read(store.UserVisibilityScope{InstitutionWide: true}, 3),
		"Institution-wide: the oldest three of five current bindings, named; the ended binding and a User with none are absent")
	require.Equal(t, []store.UserRoleBindingSummary{{UserID: user.ID, More: 1, Bindings: []store.RoleBindingSummary{
		summary(reader, model.RoleScopeAcademicUnit, child.ID.String(), child.DisplayName),
		summary(reviewer, model.RoleScopeClass, class.ID.String(), class.DisplayName),
	}}}, read(store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{root.ID}}, 2),
		"a unit scope lists and counts only bindings in its subtree and its Classes; the Institution and sibling bindings are not counted")
	require.Equal(t, []store.UserRoleBindingSummary{{UserID: user.ID, Bindings: []store.RoleBindingSummary{
		summary(reviewer, model.RoleScopeClass, class.ID.String(), class.DisplayName),
	}}}, read(store.UserVisibilityScope{ClassIDs: []model.ClassID{class.ID}}, 3), "a Class scope reads its own bindings")
	require.Empty(t, read(store.UserVisibilityScope{}, 3), "no scope reads nothing")
	exact, err := ss.RoleBinding().ListVisibleSummariesByUsers(ctx, store.RoleBindingSummaryOptions{
		UserIDs:    []model.UserID{user.ID, other.ID},
		Visibility: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{child.ID}, ActiveAt: activeAt},
		ScopeType:  model.RoleScopeAcademicUnit, ScopeID: child.ID.String(), Limit: 1,
	})
	require.NoError(t, err)
	require.Equal(t, []store.UserRoleBindingSummary{{UserID: user.ID, More: 1, Bindings: []store.RoleBindingSummary{
		summary(reader, model.RoleScopeAcademicUnit, child.ID.String(), child.DisplayName),
	}}}, exact, "an exact scope keeps only the unit's own bindings, not its Classes'")

	var invalid *store.ErrInvalidInput
	for name, options := range map[string]store.RoleBindingSummaryOptions{
		"missing clock": {UserIDs: []model.UserID{user.ID}, Visibility: store.UserVisibilityScope{InstitutionWide: true}, Limit: 3},
		"zero limit":    {UserIDs: []model.UserID{user.ID}, Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: activeAt}},
		"invalid user":  {UserIDs: []model.UserID{"not-an-id"}, Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: activeAt}, Limit: 3},
		"partial scope": {UserIDs: []model.UserID{user.ID}, Visibility: store.UserVisibilityScope{InstitutionWide: true, ActiveAt: activeAt}, ScopeType: model.RoleScopeAcademicUnit, Limit: 3},
	} {
		t.Run(name, func(t *testing.T) {
			_, readErr := ss.RoleBinding().ListVisibleSummariesByUsers(ctx, options)
			require.ErrorAs(t, readErr, &invalid)
		})
	}
}

func testRoleBindingExpiryDeadlines(t *testing.T, ss store.Store) {
	ctx := t.Context()
	institution := saveInstitution(t, ctx, ss)
	role, err := SaveRole(ctx, ss, &model.Role{
		Name: "expiry-reader", DisplayName: "Expiry Reader", Permissions: []string{string(model.ActionClassView)},
	})
	require.NoError(t, err)
	bindings := ss.RoleBinding()
	page, err := bindings.ListExpiries(ctx, store.RoleBindingExpiryCursor{}, 1)
	require.NoError(t, err)
	// Use the same database clock as the scanner, with no timing wait.
	deadline := page.CheckedAt.Add(-time.Second)
	var expired []model.RoleBindingID
	for _, end := range []time.Time{deadline, deadline, page.CheckedAt.Add(time.Hour)} {
		user := saveUser(t, ctx, ss)
		binding, saveErr := SaveRoleBinding(ctx, ss, &model.RoleBinding{
			UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
			ScopeID: institution.ID.String(), StartsAt: deadline.Add(-time.Hour), EndsAt: model.OptionalTimeFrom(end),
		})
		require.NoError(t, saveErr)
		if end.Equal(deadline) {
			expired = append(expired, binding.ID)
		}
	}

	page, err = bindings.ListExpiries(ctx, store.RoleBindingExpiryCursor{At: deadline.Add(-time.Microsecond)}, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	first := page.Items[0]
	page, err = bindings.ListExpiries(ctx, first.Cursor, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	second := page.Items[0]
	require.ElementsMatch(t, expired, []model.RoleBindingID{first.Cursor.ID, second.Cursor.ID})
	require.True(t, first.Cursor.At.Equal(second.Cursor.At))
	require.NotEqual(t, first.UserID, second.UserID)

	page, err = bindings.ListExpiries(ctx, store.RoleBindingExpiryCursor{At: deadline}, 5)
	require.NoError(t, err)
	require.Empty(t, page.Items, "scanned time and the future deadlines do not reappear")
}
