// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAuthorizeViewSelectsCurrentManagerOrExplicitOverride(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		manager bool
		member  bool
		want    model.Action
		denied  bool
	}{
		{name: "current manager", manager: true, member: true, want: model.ActionExamView},
		{name: "non-manager override", want: model.ActionExamViewOverride},
		{name: "revoked membership override", manager: true, want: model.ActionExamViewOverride},
		{name: "denied non-manager", want: model.ActionExamViewOverride, denied: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAuthoringFixture(t)
			if test.denied {
				// exam.view does not stand in for the override a non-manager needs.
				fixture = newAuthoringFixture(t, model.ActionExamView)
			}
			fixture.persistence.actorIsManager = test.manager
			if test.member {
				fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
			}
			err := fixture.service.AuthorizeExamView(context.Background(), fixture.inv, fixture.examID)
			status := model.AuditStatusSuccess
			if test.denied {
				require.True(t, apperror.Is(err, "authorization.denied"), "AuthorizeExamView() error = %v", err)
				status = model.AuditStatusFail
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []string{decided(test.want, fixture.examResource(), status)}, fixture.decisions(), "authorization")
		})
	}
}

func TestGetSelectsManagerOrOverrideAuthorization(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		manager bool
		action  model.Action
		order   []string
	}{
		{"manager", true, model.ActionExamView, []string{"store.access", "membership", "grants", "decision:exam.view:success", "store.get"}},
		{"override", false, model.ActionExamViewOverride, []string{"store.access", "grants", "decision:exam.view.override:success", "store.get"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAuthoringFixture(t)
			fixture.persistence.actorIsManager = test.manager
			if test.manager {
				fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
			}
			view, err := fixture.service.GetExam(context.Background(), fixture.inv, fixture.examID)
			require.NoError(t, err)
			require.Equal(t, fixture.examID, view.Exam.ID, "view = %#v", view)
			require.Equal(t, []string{decided(test.action, fixture.examResource(), model.AuditStatusSuccess)}, fixture.decisions(), "authorization")
			// Contract: authorization precedes reading the Exam. Only a Manager's
			// current membership is consulted.
			require.Equal(t, test.order, *fixture.order)
		})
	}
}

func TestGetRequiresOverrideWhenManagerMembershipWasRevoked(t *testing.T) {
	t.Parallel()
	// The caller holds exam.view, which a Manager without membership cannot use.
	fixture := newAuthoringFixture(t, model.ActionExamView)
	fixture.persistence.actorIsManager = true
	_, err := fixture.service.GetExam(context.Background(), fixture.inv, fixture.examID)
	// The read conceals the denial as absence.
	require.Equal(t, "resource.not_found", faultCode(err), "GetExam() error = %v", err)
	require.Equal(t, []string{decided(model.ActionExamViewOverride, fixture.examResource(), model.AuditStatusFail)}, fixture.decisions(), "action")
	require.Equal(t, []string{"store.access", "membership", "grants", "decision:exam.view.override:fail"}, *fixture.order)
}

func TestGetStopsWhenCurrentManagerMembershipCannotBeRead(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	failure := errors.New("membership unavailable")
	fixture.memberships.err = failure
	_, err := fixture.service.GetExam(context.Background(), fixture.inv, fixture.examID)
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.unavailable", fault.Code())
	require.ErrorIs(t, err, failure)
	require.Empty(t, fixture.decisions(), "authorized without current membership")
	require.Equal(t, []string{"store.access", "membership"}, *fixture.order)
}
