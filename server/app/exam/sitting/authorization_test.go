// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuthorizeViewRechecksCurrentSittingRelationship(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}

	require.NoError(t, fixture.service.AuthorizeSittingView(context.Background(), fixture.inv, fixture.sittingID))
	require.Equal(t, fixture.sittingID, fixture.persistence.resolveID, "subscription authorization = resolve")
	require.Equal(t, model.ActionExamSittingView, fixture.authorizer.action, "subscription authorization = resolve")
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: fixture.sittingID.String()}, fixture.authorizer.resource, "subscription authorization = resolve")

	fixture.memberships.items = nil
	require.NoError(t, fixture.service.AuthorizeSittingView(context.Background(), fixture.inv, fixture.sittingID))
	require.Equal(t, model.ActionExamSittingViewOverride, fixture.authorizer.action)
}

func TestBrowserActivityRequiresCurrentExactManagerAndDedicatedPermission(t *testing.T) {
	for _, test := range []struct {
		name                                                      string
		removeManager, removeUnit, denyPermission, failMembership bool
	}{
		{name: "exact manager"}, {name: "administrator grant without manager", removeManager: true},
		{name: "manager without exact unit", removeUnit: true}, {name: "manager without action", denyPermission: true},
		{name: "unrelated unit membership is not consulted", failMembership: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: f.sitting(t)}
			f.access.snapshot.ActorIsManager = !test.removeManager
			if test.removeUnit {
				f.memberships.items = nil
			}
			if test.denyPermission {
				f.authorizer.err = errors.New("permission denied")
			}
			if test.failMembership {
				f.memberships.err = errors.New("membership unavailable")
			}
			unit, err := f.service.AuthorizeBrowserActivityView(context.Background(), f.inv, f.sittingID)
			wantAllowed := !test.removeManager && !test.denyPermission
			require.Equal(t, wantAllowed, err == nil, "AuthorizeBrowserActivityView() = %s, %v", unit, err)
			if wantAllowed {
				require.Equal(t, f.unitID, unit.AcademicUnitID, "wrong authorized unit")
			}
			if !wantAllowed {
				require.Empty(t, unit.AcademicUnitID, "denial returned a scope")
			}
			require.Equal(t, model.ActionExamAttemptBrowserActivityView, f.authorizer.action, "used an unrelated action or resource")
			require.Equal(t, f.sittingID.String(), f.authorizer.resource.ID, "used an unrelated action or resource")
			require.Equal(t, test.removeManager, f.authorizer.denied, "relationship denial was not audited")
		})
	}
}

func TestBrowserActivityRechecksMembershipBetweenPagesAndFailsClosedOnAudit(t *testing.T) {
	f := newFixture(t)
	f.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: f.sitting(t)}
	_, err := f.service.AuthorizeBrowserActivityView(context.Background(), f.inv, f.sittingID)
	require.NoError(t, err)
	f.access.snapshot.ActorIsManager = false
	_, err = f.service.AuthorizeBrowserActivityView(context.Background(), f.inv, f.sittingID)
	require.Error(t, err, "later page retained removed Manager authority")
	require.True(t, f.authorizer.denied, "later page retained removed Manager authority")
	auditErr := errors.New("audit unavailable")
	f.authorizer.denyErr = auditErr
	_, err = f.service.AuthorizeBrowserActivityView(context.Background(), f.inv, f.sittingID)
	require.ErrorIs(t, err, auditErr)
}

func TestAuthorizeManageReportsOrdinaryAndOverrideDecision(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}
	override, err := fixture.service.AuthorizeSittingManage(context.Background(), fixture.inv, fixture.sittingID)
	require.NoError(t, err, "ordinary manage override")
	require.False(t, override.Override, "ordinary manage override")
	require.Equal(t, model.ActionExamSittingManage, fixture.authorizer.action, "ordinary manage override")
	fixture.memberships.items = nil
	override, err = fixture.service.AuthorizeSittingManage(context.Background(), fixture.inv, fixture.sittingID)
	require.NoError(t, err, "override manage override")
	require.True(t, override.Override, "override manage override")
	require.Equal(t, model.ActionExamSittingManageOverride, fixture.authorizer.action, "override manage override")
}

func TestAuthorizeSubmissionViewUsesCanonicalSubmissionResourceAndCurrentManagerRelationship(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	submissionID := model.NewSubmissionID()

	require.NoError(t, fixture.service.AuthorizeSubmissionView(context.Background(), fixture.inv, fixture.examID, submissionID))
	require.Equal(t, model.ActionSubmissionView, fixture.authorizer.action, "ordinary Submission authorization")
	require.Equal(t, model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()}, fixture.authorizer.resource, "ordinary Submission authorization")

	fixture.memberships.items = nil
	require.NoError(t, fixture.service.AuthorizeSubmissionView(context.Background(), fixture.inv, fixture.examID, submissionID))
	require.Equal(t, model.ActionSubmissionViewOverride, fixture.authorizer.action)
}

func TestAuthorizeSubmissionReviewAndReleaseUseDistinctCurrentPermissions(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	submissionID := model.NewSubmissionID()

	override, err := fixture.service.AuthorizeSubmissionReview(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "ordinary Review authorization override")
	require.False(t, override.Override, "ordinary Review authorization override")
	require.Equal(t, model.ActionSubmissionReview, fixture.authorizer.action, "ordinary Review authorization override")
	override, err = fixture.service.AuthorizeSubmissionRelease(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "ordinary release authorization override")
	require.False(t, override.Override, "ordinary release authorization override")
	require.Equal(t, model.ActionSubmissionRelease, fixture.authorizer.action, "ordinary release authorization override")

	fixture.memberships.items = nil
	override, err = fixture.service.AuthorizeSubmissionReview(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "Review override authorization override")
	require.True(t, override.Override, "Review override authorization override")
	require.Equal(t, model.ActionSubmissionReviewOverride, fixture.authorizer.action, "Review override authorization override")
	override, err = fixture.service.AuthorizeSubmissionRelease(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "release override authorization override")
	require.True(t, override.Override, "release override authorization override")
	require.Equal(t, model.ActionSubmissionReleaseOverride, fixture.authorizer.action, "release override authorization override")
}
