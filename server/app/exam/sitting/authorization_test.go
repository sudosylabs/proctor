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
	sittingResource := model.Resource{Type: model.ResourceExamSitting, ID: fixture.sittingID.String()}
	fixture.requireDecision(t, model.ActionExamSittingView, sittingResource, model.AuditStatusSuccess)

	fixture.memberships.items = nil
	require.NoError(t, fixture.service.AuthorizeSittingView(context.Background(), fixture.inv, fixture.sittingID))
	fixture.requireDecision(t, model.ActionExamSittingViewOverride, sittingResource, model.AuditStatusSuccess)
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
			inv := f.inv
			if test.denyPermission {
				inv = f.stranger
			}
			if test.failMembership {
				f.memberships.err = errors.New("membership unavailable")
			}
			unit, err := f.service.AuthorizeBrowserActivityView(context.Background(), inv, f.sittingID)
			wantAllowed := !test.removeManager && !test.denyPermission
			require.Equal(t, wantAllowed, err == nil, "AuthorizeBrowserActivityView() = %s, %v", unit, err)
			if wantAllowed {
				require.Equal(t, f.unitID, unit.AcademicUnitID, "wrong authorized unit")
			}
			if !wantAllowed {
				require.Empty(t, unit.AcademicUnitID, "denial returned a scope")
			}
			// Every outcome, the relationship denial included, is one recorded
			// decision on the dedicated action and the exact Sitting.
			status := model.AuditStatusSuccess
			if !wantAllowed {
				status = model.AuditStatusFail
			}
			require.Len(t, f.control.Audits.Decisions(), 1, "recorded decisions")
			f.requireDecision(t, model.ActionExamAttemptBrowserActivityView,
				model.Resource{Type: model.ResourceExamSitting, ID: f.sittingID.String()}, status)
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
	f.requireDecision(t, model.ActionExamAttemptBrowserActivityView,
		model.Resource{Type: model.ResourceExamSitting, ID: f.sittingID.String()}, model.AuditStatusFail)
	auditErr := errors.New("audit unavailable")
	f.control.Audits.FailDecisions(auditErr)
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
	sittingResource := model.Resource{Type: model.ResourceExamSitting, ID: fixture.sittingID.String()}
	fixture.requireDecision(t, model.ActionExamSittingManage, sittingResource, model.AuditStatusSuccess)
	fixture.memberships.items = nil
	override, err = fixture.service.AuthorizeSittingManage(context.Background(), fixture.inv, fixture.sittingID)
	require.NoError(t, err, "override manage override")
	require.True(t, override.Override, "override manage override")
	fixture.requireDecision(t, model.ActionExamSittingManageOverride, sittingResource, model.AuditStatusSuccess)
}

func TestAuthorizeSubmissionViewUsesCanonicalSubmissionResourceAndCurrentManagerRelationship(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	submissionID := model.NewSubmissionID()

	require.NoError(t, fixture.service.AuthorizeSubmissionView(context.Background(), fixture.inv, fixture.examID, submissionID))
	submission := model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()}
	fixture.requireDecision(t, model.ActionSubmissionView, submission, model.AuditStatusSuccess)

	fixture.memberships.items = nil
	require.NoError(t, fixture.service.AuthorizeSubmissionView(context.Background(), fixture.inv, fixture.examID, submissionID))
	fixture.requireDecision(t, model.ActionSubmissionViewOverride, submission, model.AuditStatusSuccess)
}

func TestAuthorizeSubmissionReviewAndReleaseUseDistinctCurrentPermissions(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	submissionID := model.NewSubmissionID()
	submission := model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()}

	override, err := fixture.service.AuthorizeSubmissionReview(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "ordinary Review authorization override")
	require.False(t, override.Override, "ordinary Review authorization override")
	fixture.requireDecision(t, model.ActionSubmissionReview, submission, model.AuditStatusSuccess)
	override, err = fixture.service.AuthorizeSubmissionRelease(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "ordinary release authorization override")
	require.False(t, override.Override, "ordinary release authorization override")
	fixture.requireDecision(t, model.ActionSubmissionRelease, submission, model.AuditStatusSuccess)

	fixture.memberships.items = nil
	override, err = fixture.service.AuthorizeSubmissionReview(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "Review override authorization override")
	require.True(t, override.Override, "Review override authorization override")
	fixture.requireDecision(t, model.ActionSubmissionReviewOverride, submission, model.AuditStatusSuccess)
	override, err = fixture.service.AuthorizeSubmissionRelease(context.Background(), fixture.inv, fixture.examID, submissionID)
	require.NoError(t, err, "release override authorization override")
	require.True(t, override.Override, "release override authorization override")
	fixture.requireDecision(t, model.ActionSubmissionReleaseOverride, submission, model.AuditStatusSuccess)
}
