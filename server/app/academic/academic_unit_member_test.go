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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type academicUnitMemberStoreFake struct {
	store.AcademicUnitMemberStore
	pageOptions  store.AcademicUnitMemberPageOptions
	countOptions store.AcademicUnitMemberPageOptions
	pageResult   *store.AcademicUnitMemberPage
	events       *[]string
	current      *model.AcademicUnitMember
	createInput  *store.AcademicUnitMemberCreation
	endInput     *store.AcademicUnitMemberEnd
}

func (s *academicUnitMemberStoreFake) Get(context.Context, model.AcademicUnitMemberID) (*model.AcademicUnitMember, error) {
	*s.events = append(*s.events, "get-member")
	return s.current, nil
}
func (s *academicUnitMemberStoreFake) Create(_ context.Context, input *store.AcademicUnitMemberCreation) (*model.AcademicUnitMember, error) {
	*s.events = append(*s.events, "store-create")
	s.createInput = input
	return input.Member, nil
}
func (s *academicUnitMemberStoreFake) End(_ context.Context, input *store.AcademicUnitMemberEnd) (*model.AcademicUnitMember, error) {
	*s.events = append(*s.events, "store-end")
	s.endInput = input
	ended := *s.current
	ended.EndsAt = model.OptionalTimeFrom(input.EndAt)
	ended.Revision = input.ExpectedRevision + 1
	return &ended, nil
}

func TestAcademicUnitMemberCreateUsesAuthorizationWithoutGrantingPermission(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	unitID, userID := model.NewID(), model.NewID()
	access := academicUnitAccess(t, &events, nil, unitGrant(principal.UserID, model.AcademicUnitID(unitID), model.ActionAcademicUnitMembersManage))
	persistence := &academicUnitMemberStoreFake{events: &events}
	publisher := &realtimetest.Publisher{}
	composer, rendered := newMail(t, &events)
	clockCalls := 0
	service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: persistence, user: relationshipUserStoreTestFake{}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time {
		clockCalls++
		return time.UnixMilli(500)
	}, NewID: model.NewID, Publisher: publisher})
	created, err := service.CreateAcademicUnitMember(context.Background(), caller, CreateAcademicUnitMemberCommand{AcademicUnitID: model.AcademicUnitID(unitID), UserID: model.UserID(userID), StartAt: 100})
	require.NoError(t, err)
	require.Equal(t, unitID, created.AcademicUnitID.String())
	require.Equal(t, userID, created.UserID.String())
	require.False(t, created.EndsAt.Valid)
	require.False(t, persistence.createInput.Member.EndsAt.Valid)
	require.Equal(t, 1, clockCalls, "clock calls/creation/audit time")
	require.True(t, model.TimeUTC(created.CreatedAt).Equal(persistence.createInput.AuditAt), "clock calls/creation/audit time")
	// Contract: the new member's cached authorization is dropped, once, and
	// the unit's viewers learn its member count moved.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.InvalidateAuthorization, UserID: model.UserID(userID)},
		academicUnitEventCall("academic_unit_updated", model.AcademicUnitID(unitID)),
	}, publisher.Calls())
	require.Equal(t, model.UserID(userID), onlyAttempt(t, access).SubjectUserID, "the membership names the member as its subject")
	require.NotNil(t, persistence.createInput.Notice, "creation mail = %#v", persistence.createInput)
	require.Equal(t, int64(1), persistence.createInput.Recipient.Revision, "creation mail = %#v", persistence.createInput)
	require.Equal(t, 1, rendered.Count(), "creation mail = %#v", persistence.createInput)
	require.Equal(t, model.MailTemplateAcademicUnitAssigned, persistence.createInput.Notice.Occurrence.TemplateKey, "creation mail = %#v", persistence.createInput)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"grants", "decision:academic_unit.members.manage:success", "render-mail", "audit-begin:academic_unit.members.manage", "store-create"}, events)
}

func TestAcademicUnitMemberEndCarriesRevision(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	current := &model.AcademicUnitMember{
		ID: model.NewAcademicUnitMemberID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Revision: 3, AcademicUnitID: model.AcademicUnitID(model.NewID()), UserID: model.UserID(model.NewID()),
		StartsAt: model.TimeFromMillis(100),
	}
	access := academicUnitAccess(t, &events, nil, unitGrant(principal.UserID, current.AcademicUnitID, model.ActionAcademicUnitMembersManage))
	persistence := &academicUnitMemberStoreFake{events: &events, current: current}
	publisher := &realtimetest.Publisher{}
	composer, rendered := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: persistence, user: relationshipUserStoreTestFake{}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: publisher})
	ended, err := service.EndAcademicUnitMember(context.Background(), caller, EndAcademicUnitMemberCommand{ID: current.ID})
	require.NoError(t, err)
	require.Equal(t, current.Revision, persistence.endInput.ExpectedRevision, "end input/result = %#v / %#v", persistence.endInput, ended)
	require.Equal(t, current.Revision+1, ended.Revision, "end input/result = %#v / %#v", persistence.endInput, ended)
	// Contract: the former member's cached authorization is dropped, once, and
	// the unit's viewers learn its member count moved.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.InvalidateAuthorization, UserID: current.UserID},
		academicUnitEventCall("academic_unit_updated", current.AcademicUnitID),
	}, publisher.Calls())
	require.NotNil(t, persistence.endInput.Notice, "end mail = %#v", persistence.endInput)
	require.Equal(t, int64(1), persistence.endInput.Recipient.Revision, "end mail = %#v", persistence.endInput)
	require.Equal(t, 1, rendered.Count(), "end mail = %#v", persistence.endInput)
	require.Equal(t, model.MailTemplateAcademicUnitAssignmentEnded, persistence.endInput.Notice.Occurrence.TemplateKey, "end mail = %#v", persistence.endInput)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The first grants read is the preflight,
	// which records no decision when the caller holds the action somewhere.
	require.Equal(t, []string{"grants", "get-member", "grants", "decision:academic_unit.members.manage:success", "render-mail", "audit-begin:academic_unit.members.manage", "store-end"}, events)
}

func TestAcademicUnitMemberEndDenialDoesNotInspectOpaqueMemberID(t *testing.T) {
	t.Parallel()
	events := []string{}
	_, caller := newCaller()
	access := academicUnitAccess(t, &events, nil)
	composer, _ := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: &academicUnitMemberStoreFake{events: &events}, user: relationshipUserStoreTestFake{}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: time.Now, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	_, err := service.EndAcademicUnitMember(context.Background(), caller, EndAcademicUnitMemberCommand{ID: model.NewAcademicUnitMemberID()})
	require.True(t, apperror.Is(err, "authorization.denied"), "End() error = %v, want authorization.denied", err)
	require.Equal(t, []string{"grants", "decision:academic_unit.members.manage:fail"}, events, "events")
}

func TestAcademicUnitMemberEndConcealsCrossScopeTarget(t *testing.T) {
	t.Parallel()
	events := []string{}
	principal, caller := newCaller()
	current := &model.AcademicUnitMember{
		ID: model.NewAcademicUnitMemberID(), AcademicUnitID: model.NewAcademicUnitID(), UserID: model.NewUserID(),
		CreatedAt: model.NowUTC(), UpdatedAt: model.NowUTC(), StartsAt: model.NowUTC(), Revision: 1,
	}
	// The caller manages members of another unit only.
	access := academicUnitAccess(t, &events, []*model.AcademicUnit{{ID: current.AcademicUnitID, Name: "target"}},
		unitGrant(principal.UserID, model.NewAcademicUnitID(), model.ActionAcademicUnitMembersManage))
	composer, _ := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{academicUnitMember: &academicUnitMemberStoreFake{events: &events, current: current}, user: relationshipUserStoreTestFake{}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: time.Now, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	_, err := service.EndAcademicUnitMember(context.Background(), caller, EndAcademicUnitMemberCommand{ID: current.ID})
	require.True(t, apperror.Is(err, "resource.not_found"), "End() error = %v, want concealed resource.not_found", err)
}

func (s *academicUnitMemberStoreFake) ListPageByAcademicUnit(_ context.Context, options store.AcademicUnitMemberPageOptions) (*store.AcademicUnitMemberPage, error) {
	*s.events = append(*s.events, "list-page")
	s.pageOptions = options
	if s.pageResult != nil {
		return s.pageResult, nil
	}
	return &store.AcademicUnitMemberPage{}, nil
}
func (s *academicUnitMemberStoreFake) CountPageByAcademicUnit(_ context.Context, options store.AcademicUnitMemberPageOptions, maximum int) (int, error) {
	*s.events = append(*s.events, "count-page")
	s.countOptions = options
	if s.pageResult == nil {
		return 0, nil
	}
	return min(len(s.pageResult.Members), maximum), nil
}
