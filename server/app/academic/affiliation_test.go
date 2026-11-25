// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type affiliationStoreFake struct {
	store.AffiliationStore
	events      *[]string
	current     *model.Affiliation
	values      []*model.Affiliation
	createInput *store.AffiliationCreation
	endInput    *store.AffiliationEnd
	endErr      error
}

func (s *affiliationStoreFake) Get(context.Context, model.AffiliationID) (*model.Affiliation, error) {
	*s.events = append(*s.events, "get-affiliation")
	return s.current, nil
}
func (s *affiliationStoreFake) ListByUser(context.Context, model.UserID) ([]*model.Affiliation, error) {
	*s.events = append(*s.events, "list-affiliations")
	return s.values, nil
}
func (s *affiliationStoreFake) Create(_ context.Context, input *store.AffiliationCreation) (*model.Affiliation, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return input.Affiliation, nil
}
func (s *affiliationStoreFake) End(_ context.Context, input *store.AffiliationEnd) (*model.Affiliation, error) {
	*s.events = append(*s.events, "store-end")
	s.endInput = input
	if s.endErr != nil {
		return nil, s.endErr
	}
	ended := *s.current
	ended.EndsAt = model.OptionalTimeFrom(input.EndAt)
	ended.Revision = input.ExpectedRevision + 1
	return &ended, nil
}

func TestAffiliationEndFutureRangePreservesRejection(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.Affiliation{
		ID: model.NewAffiliationID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Revision: 2, UserID: model.UserID(model.NewID()), Kind: model.AffiliationTeacher, StartsAt: model.TimeFromMillis(1_000),
	}
	persistence := &affiliationStoreFake{events: &events, current: current, endErr: store.NewErrConflict("affiliation", "affiliation_end_time", nil)}
	access, caller := affiliationAccess(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{affiliation: persistence, classMember: &affiliationEnrollmentFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	_, err := service.EndAffiliation(context.Background(), caller, EndAffiliationCommand{ID: current.ID})
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "resource.not_found", failure.Code())
	require.Equal(t, "affiliation", failure.Fields()["resource"])
	require.True(t, time.UnixMilli(500).UTC().Equal(persistence.endInput.EndAt))
}

// affiliationAccess grants the caller Institution-wide user.manage over the
// active Users the test's User store holds.
func affiliationAccess(t *testing.T, events *[]string) (*accesstest.Fixture, invocation.Invocation) {
	t.Helper()
	principal, caller := newCaller()
	return accessWith(t, events, accesstest.Options{
		Grants: []accesstest.Grant{institutionGrant(principal.UserID, model.ActionUserManage)},
		Users:  relationshipUserStoreTestFake{},
	}), caller
}

type affiliationEnrollmentFake struct {
	store.ClassMemberStore
	events *[]string
	values []*model.ClassMember
}

func (s *affiliationEnrollmentFake) ListByUser(context.Context, model.UserID) ([]*model.ClassMember, error) {
	*s.events = append(*s.events, "list-enrollments")
	return s.values, nil
}

func TestAffiliationCreateKeepsKindsNonExclusiveAndAudited(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewID()
	persistence := &affiliationStoreFake{events: &events}
	clockCalls := 0
	access, caller := affiliationAccess(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{affiliation: persistence, classMember: &affiliationEnrollmentFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time {
		clockCalls++
		return time.UnixMilli(int64(500 + clockCalls))
	}, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	for _, kind := range []model.AffiliationKind{model.AffiliationStudent, model.AffiliationTeacher} {
		created, err := service.CreateAffiliation(context.Background(), caller, CreateAffiliationCommand{UserID: model.UserID(userID), Kind: kind, StartAt: 100})
		require.NoError(t, err)
		require.Equal(t, kind, created.Kind)
		require.Equal(t, userID, persistence.createInput.Affiliation.UserID.String())
		require.True(t, model.TimeUTC(created.CreatedAt).Equal(persistence.createInput.AuditAt), "creation/audit time")
	}
	require.Equal(t, 2, clockCalls, "clock calls")
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	once := []string{"grants", "decision:user.manage:success", "audit-begin:user.manage", "store-create"}
	require.Equal(t, append(once, once...), events)
}

func TestAffiliationEndRejectsActiveStudentEnrollment(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.Affiliation{
		ID: model.NewAffiliationID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Revision: 3, UserID: model.UserID(model.NewID()), Kind: model.AffiliationStudent, StartsAt: model.TimeFromMillis(100),
	}
	access, caller := affiliationAccess(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{affiliation: &affiliationStoreFake{events: &events, current: current}, classMember: &affiliationEnrollmentFake{events: &events, values: []*model.ClassMember{{}}}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	_, err := service.EndAffiliation(context.Background(), caller, EndAffiliationCommand{ID: current.ID})
	require.True(t, apperror.Is(err, "affiliation.student_has_active_enrollment"), "End() error = %v", err)
	require.Equal(t, []string{"get-affiliation", "grants", "decision:user.manage:success", "list-enrollments"}, events)
}

func TestAffiliationRetainedEndBypassesLaterStudentEnrollment(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.Affiliation{ID: model.NewAffiliationID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(200),
		Revision: 3, UserID: model.NewUserID(), Kind: model.AffiliationStudent, StartsAt: model.TimeFromMillis(100)}
	persistence := &affiliationStoreFake{events: &events, current: current}
	access, caller := affiliationAccess(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{affiliation: persistence, classMember: &affiliationEnrollmentFake{events: &events, values: []*model.ClassMember{{ID: model.NewClassMemberID()}}}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	result, err := service.EndAffiliation(context.Background(), caller,
		EndAffiliationCommand{ID: current.ID, IdempotencyKey: "row", Batch: batchitem.New(batchitem.Fields{RetainedOutcome: true})})
	require.NoError(t, err, "retained end")
	require.NotNil(t, result, "retained end")
	require.NotNil(t, persistence.endInput, "retained end")
	for _, event := range events {
		require.NotEqual(t, "list-enrollments", event, "retained outcome inspected later enrollment state: %v", events)
	}
}

func TestAffiliationEndCarriesExpectedRevision(t *testing.T) {
	t.Parallel()
	events := []string{}
	current := &model.Affiliation{
		ID: model.NewAffiliationID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Revision: 4, UserID: model.UserID(model.NewID()), Kind: model.AffiliationTeacher, StartsAt: model.TimeFromMillis(100),
	}
	persistence := &affiliationStoreFake{events: &events, current: current}
	access, caller := affiliationAccess(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{affiliation: persistence, classMember: &affiliationEnrollmentFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	ended, err := service.EndAffiliation(context.Background(), caller, EndAffiliationCommand{ID: current.ID})
	require.NoError(t, err)
	require.Equal(t, current.Revision, persistence.endInput.ExpectedRevision, "end input/result = %#v / %#v", persistence.endInput, ended)
	require.Equal(t, current.Revision+1, ended.Revision, "end input/result = %#v / %#v", persistence.endInput, ended)
}

// disabledUserStoreTestFake resolves every user as a disabled account.
type disabledUserStoreTestFake struct{ relationshipUserStoreTestFake }

func (s disabledUserStoreTestFake) Get(ctx context.Context, id model.UserID) (*model.User, error) {
	user, err := s.relationshipUserStoreTestFake.Get(ctx, id)
	if user != nil {
		user.DisabledAt = model.OptionalTimeFromMillis(600)
	}
	return user, err
}

func TestAffiliationListReadsADisabledAccount(t *testing.T) {
	t.Parallel()
	events := []string{}
	userID := model.NewUserID()
	listed := &model.Affiliation{ID: model.NewAffiliationID(), UserID: userID, Kind: model.AffiliationStudent}
	persistence := &affiliationStoreFake{events: &events, values: []*model.Affiliation{listed}}
	principal, caller := newCaller()
	access := accessWith(t, &events, accesstest.Options{
		Grants: []accesstest.Grant{institutionGrant(principal.UserID, model.ActionUserView, model.ActionUserManage)},
		Users:  disabledUserStoreTestFake{},
	})
	service := mustNew(Dependencies{Store: &catalogFake{affiliation: persistence, classMember: &affiliationEnrollmentFake{events: &events}},
		Authorizer: access.Authorizer, Auditor: access.Auditor, Now: time.Now, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})

	affiliations, err := service.ListAffiliations(context.Background(), caller, userID)
	require.NoError(t, err, "an administrator still reads a disabled account's affiliations")
	require.Equal(t, []*model.Affiliation{listed}, affiliations)

	_, err = service.CreateAffiliation(context.Background(), caller, CreateAffiliationCommand{UserID: userID, Kind: model.AffiliationTeacher, StartAt: 100})
	require.Error(t, err, "changes to a disabled account stay refused")
}
