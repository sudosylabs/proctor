// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DesktopSetupState says whether a candidate's Desktop is ready for a sitting
// that has not opened yet.
type DesktopSetupState string

const (
	// DesktopSetupReady: the newest registration's build would be admitted.
	DesktopSetupReady DesktopSetupState = "ready"
	// DesktopSetupNotSetUp: the candidate has no Desktop registration.
	DesktopSetupNotSetUp DesktopSetupState = "not_set_up"
	// DesktopSetupBlockedRelease: the newest registration's build would be
	// refused, so the candidate must update.
	DesktopSetupBlockedRelease DesktopSetupState = "blocked_release"
)

// DesktopSetup is a candidate's readiness before the sitting opens. It names
// the platform and release only, never the device or build.
type DesktopSetup struct {
	State    DesktopSetupState
	Platform model.DesktopPlatform
	Release  string
}

// CandidateBoardDetail is what the board shows about a candidate beyond the
// status. DesktopSetup is set only before the sitting opens. The panel fields
// are for callers who may manage the sitting, which PanelVisible reports.
type CandidateBoardDetail struct {
	DesktopSetup                    *DesktopSetup
	PanelVisible                    bool
	DeviceName                      string
	ChangedFileCount                int64
	FocusLossCount                  int64
	CurrentCorrectionAcknowledgedAt model.OptionalTime
}

// candidateBoardDetails reads the details of one page of candidates in one
// query, and assesses their Desktop builds in one policy read.
func (s *Service) candidateBoardDetails(
	ctx context.Context,
	page store.SittingCandidateStatusPage,
	manageAuthorized bool,
) (map[model.UserID]CandidateBoardDetail, error) {
	ids := make([]model.UserID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.Candidate.UserID)
	}

	stored, err := s.attempts.ListSittingCandidateDetails(ctx, store.SittingCandidateDetailOptions{
		ExamID: page.ExamID, SittingID: page.SittingID, CandidateUserIDs: ids,
	})
	if err != nil {
		return nil, storeError(storeErrors, err)
	}

	beforeOpening := page.SittingState == model.ExamSittingScheduled
	var builds []model.DesktopBuildTuple
	if beforeOpening {
		for _, detail := range stored {
			if detail.DesktopRegistration != nil {
				builds = append(builds, *detail.DesktopRegistration)
			}
		}
	}
	admitted, err := s.desktopBuilds.AssessDesktopBuilds(ctx, builds)
	if err != nil {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(err)
	}
	if len(admitted) != len(builds) {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(errors.New("Desktop build assessment is incomplete"))
	}

	details := make(map[model.UserID]CandidateBoardDetail, len(ids))
	next := 0
	for _, detail := range stored {
		board := CandidateBoardDetail{CurrentCorrectionAcknowledgedAt: detail.CurrentCorrectionAcknowledgedAt}
		if beforeOpening {
			setup := &DesktopSetup{State: DesktopSetupNotSetUp}
			if registration := detail.DesktopRegistration; registration != nil {
				setup.Platform, setup.Release, setup.State = registration.Platform, registration.DesktopRelease, DesktopSetupBlockedRelease
				if admitted[next] {
					setup.State = DesktopSetupReady
				}
				next++
			}
			board.DesktopSetup = setup
		}
		if manageAuthorized {
			board.PanelVisible = true
			board.DeviceName, board.ChangedFileCount, board.FocusLossCount = detail.DeviceName, detail.ChangedFileCount, detail.FocusLossCount
		}
		details[detail.CandidateUserID] = board
	}

	return details, nil
}

// ManagerEnd is who ended an Attempt and why, for the sitting's managers.
type ManagerEnd = store.ExamAttemptManagerEnd

// managerEnds reads who ended each Attempt a manager ended, for a caller who
// may manage the sitting; it reads nothing for anyone else.
func (s *Service) managerEnds(
	ctx context.Context,
	sittingID model.ExamSittingID,
	attemptIDs []model.ExamAttemptID,
	manageAuthorized bool,
) (map[model.ExamAttemptID]ManagerEnd, error) {
	ends := map[model.ExamAttemptID]ManagerEnd{}
	if !manageAuthorized || len(attemptIDs) == 0 {
		return ends, nil
	}

	stored, err := s.attempts.ListManagerEnds(ctx, sittingID, attemptIDs)
	if err != nil {
		return nil, storeError(storeErrors, err)
	}
	for _, end := range stored {
		ends[end.AttemptID] = end
	}
	return ends, nil
}
