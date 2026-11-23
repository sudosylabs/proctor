// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestManagerListNamesWhoEndedAnAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	useManagerCall(f)
	attempt, err := model.NewExamAttempt(model.NewExamAttemptID(), f.sitting.ExamID, f.sitting.ID, model.NewUserID(), f.revision.ID, f.at)
	require.NoError(t, err)
	f.persistence.managerRows = []store.ExamAttemptManagerSnapshot{{Attempt: attempt}}
	end := store.ExamAttemptManagerEnd{AttemptID: attempt.ID, ActorUserID: model.NewUserID(), Reason: "Left early.", EndedAt: f.at}
	f.persistence.managerEnds = []store.ExamAttemptManagerEnd{end}

	page, err := f.service.ListExamAttempts(context.Background(), f.inv, ListManagedAttemptsQuery{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, end, page.ManagerEnds[attempt.ID], "a sitting manager sees who ended the attempt and why")
	require.Equal(t, [][]model.ExamAttemptID{{attempt.ID}}, f.persistence.managerEndReads, "one read for the page")
}

func TestCandidateBoardDetailsShowDesktopSetupBeforeOpeningAndPanelToManagers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ready, blocked, missing := model.NewUserID(), model.NewUserID(), model.NewUserID()
	stale := f.desktopBuild
	stale.DesktopBuildID = "revoked-build"
	f.persistence.candidateDetails = []store.SittingCandidateDetail{
		{CandidateUserID: ready, DesktopRegistration: &f.desktopBuild, DeviceName: "Lab PC 14", ChangedFileCount: 3, FocusLossCount: 4},
		{CandidateUserID: blocked, DesktopRegistration: &stale},
		{CandidateUserID: missing},
	}
	page := store.SittingCandidateStatusPage{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, SittingState: model.ExamSittingScheduled,
		Items: []store.SittingCandidateStatusItem{
			{Candidate: store.SittingCandidateIdentity{UserID: ready}},
			{Candidate: store.SittingCandidateIdentity{UserID: blocked}},
			{Candidate: store.SittingCandidateIdentity{UserID: missing}},
		}}

	details, err := f.service.candidateBoardDetails(context.Background(), page, true)
	require.NoError(t, err)
	require.Equal(t, &DesktopSetup{State: DesktopSetupReady, Platform: f.desktopBuild.Platform, Release: f.desktopBuild.DesktopRelease}, details[ready].DesktopSetup)
	require.Equal(t, DesktopSetupBlockedRelease, details[blocked].DesktopSetup.State)
	require.Equal(t, DesktopSetupNotSetUp, details[missing].DesktopSetup.State)
	require.True(t, details[ready].PanelVisible)
	require.Equal(t, "Lab PC 14", details[ready].DeviceName)
	require.Equal(t, []model.UserID{ready, blocked, missing}, f.persistence.detailOptions.CandidateUserIDs, "one read for the page")

	viewerDetails, err := f.service.candidateBoardDetails(context.Background(), page, false)
	require.NoError(t, err)
	require.False(t, viewerDetails[ready].PanelVisible, "a viewer who can't manage sees no panel")
	require.Empty(t, viewerDetails[ready].DeviceName)

	page.SittingState = model.ExamSittingOpen
	openDetails, err := f.service.candidateBoardDetails(context.Background(), page, true)
	require.NoError(t, err)
	require.Nil(t, openDetails[ready].DesktopSetup, "Desktop setup is shown only before the sitting opens")
}
