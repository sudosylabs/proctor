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
		name      string
		manager   bool
		member    bool
		want      model.Action
		authorize error
	}{
		{name: "current manager", manager: true, member: true, want: model.ActionExamView},
		{name: "non-manager override", want: model.ActionExamViewOverride},
		{name: "revoked membership override", manager: true, want: model.ActionExamViewOverride},
		{name: "denied non-manager", want: model.ActionExamViewOverride, authorize: errors.New("denied")},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAuthoringFixture(t)
			fixture.persistence.actorIsManager = test.manager
			if test.member {
				fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
			}
			fixture.authorizer.err = test.authorize
			err := fixture.service.AuthorizeExamView(context.Background(), fixture.invocation, fixture.examID)
			require.ErrorIs(t, err, test.authorize)
			require.Equal(t, test.want, fixture.authorizer.action, "authorization")
			require.Equal(t, model.Resource{Type: model.ResourceExam, ID: fixture.examID.String()}, fixture.authorizer.resource, "authorization")
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
		{"manager", true, model.ActionExamView, []string{"store.access", "membership", "authorize", "store.get"}},
		{"override", false, model.ActionExamViewOverride, []string{"store.access", "authorize", "store.get"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAuthoringFixture(t)
			fixture.persistence.actorIsManager = test.manager
			if test.manager {
				fixture.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: fixture.unitID, UserID: fixture.userID}}
			}
			view, err := fixture.service.GetExam(context.Background(), fixture.invocation, fixture.examID)
			require.NoError(t, err)
			require.Equal(t, fixture.examID, view.Exam.ID, "view/auth = %#v / %s %#v", view, fixture.authorizer.action, fixture.authorizer.resource)
			require.Equal(t, test.action, fixture.authorizer.action, "view/auth = %#v / %s %#v", view, fixture.authorizer.action, fixture.authorizer.resource)
			require.Equal(t, model.ResourceExam, fixture.authorizer.resource.Type, "view/auth = %#v / %s %#v", view, fixture.authorizer.action, fixture.authorizer.resource)
			// Contract: authorization precedes reading the Exam. Only a Manager's
			// current membership is consulted.
			require.Equal(t, test.order, *fixture.order)
		})
	}
}

func TestGetRequiresOverrideWhenManagerMembershipWasRevoked(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	denied := errors.New("override denied")
	fixture.authorizer.err = denied
	_, err := fixture.service.GetExam(context.Background(), fixture.invocation, fixture.examID)
	require.ErrorIs(t, err, denied)
	require.Equal(t, model.ActionExamViewOverride, fixture.authorizer.action, "action")
	require.Equal(t, []string{"store.access", "membership", "authorize"}, *fixture.order)
}

func TestGetStopsWhenCurrentManagerMembershipCannotBeRead(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	fixture.persistence.actorIsManager = true
	failure := errors.New("membership unavailable")
	fixture.memberships.err = failure
	_, err := fixture.service.GetExam(context.Background(), fixture.invocation, fixture.examID)
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.unavailable", fault.Code())
	require.ErrorIs(t, err, failure)
	require.Empty(t, fixture.authorizer.action, "authorized without current membership")
	require.Equal(t, []string{"store.access", "membership"}, *fixture.order)
}
