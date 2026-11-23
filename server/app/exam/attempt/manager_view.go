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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ManagedAttempt is a managed view of one attempt. ManagerEnd says who ended
// it and why when a manager did, and only to a caller who may manage the
// sitting.
type ManagedAttempt struct {
	store.ExamAttemptManagerSnapshot
	ManagerEnd *ManagerEnd
}

// GetExamAttempt returns a managed view of one attempt.
func (s *Service) GetExamAttempt(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
) (_ ManagedAttempt, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	snapshot, err := s.getExamAttempt(ctx, inv, examID, sittingID, attemptID)
	if err != nil {
		return ManagedAttempt{}, err
	}

	manageAuthorized, err := s.sittingManageAuthorized(ctx, inv, sittingID)
	if err != nil {
		return ManagedAttempt{}, err
	}
	ends, err := s.managerEnds(ctx, sittingID, []model.ExamAttemptID{attemptID}, manageAuthorized)
	if err != nil {
		return ManagedAttempt{}, err
	}

	managed := ManagedAttempt{ExamAttemptManagerSnapshot: snapshot}
	if end, ended := ends[attemptID]; ended {
		managed.ManagerEnd = &end
	}
	return managed, nil
}

func (s *Service) getExamAttempt(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
) (store.ExamAttemptManagerSnapshot, error) {
	if !examID.IsValid() || !sittingID.IsValid() || !attemptID.IsValid() {
		return store.ExamAttemptManagerSnapshot{}, apperror.Invalid("exam.attempt.invalid", "attempt")
	}
	// Authorize before any attempt read so a caller without view authority
	// cannot tell existing, mismatched and missing attempts apart.
	if err := s.managers.AuthorizeSittingView(ctx, inv, sittingID); err != nil {
		return store.ExamAttemptManagerSnapshot{}, err
	}

	sitting, err := s.sittings.Resolve(ctx, sittingID)
	if err != nil {
		return store.ExamAttemptManagerSnapshot{}, storeError(storeErrors, err)
	}

	if sitting.Sitting.ExamID != examID {
		return store.ExamAttemptManagerSnapshot{}, apperror.New("exam.attempt.not_found")
	}

	snapshot, err := s.attempts.Get(ctx, examID, attemptID)
	if err != nil {
		return store.ExamAttemptManagerSnapshot{}, storeError(storeErrors, err)
	}

	if !snapshot.SuspensionActiveOrAbsent() {
		return store.ExamAttemptManagerSnapshot{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent managed Attempt projection"))
	}

	if snapshot.Attempt.SittingID != sittingID {
		return store.ExamAttemptManagerSnapshot{}, apperror.New("exam.attempt.not_found")
	}

	if snapshot.Attempt.CandidateUserID == inv.Principal().UserID {
		resource := model.Resource{Type: model.ResourceExamSitting, ID: snapshot.Attempt.SittingID.String()}
		auditID, auditErr := s.auditor.Begin(ctx, access.Mutation{
			Invocation: inv,
			Action:     model.ActionExamSittingView,
			Resource:   resource,
			ScopeType:  model.RoleScopeAcademicUnit,
			ScopeID:    sitting.AcademicUnitID.String(),
			Operation:  "view_managed_attempt",
			Value: map[string]any{
				"exam_id":         snapshot.Attempt.ExamID.String(),
				"exam_sitting_id": snapshot.Attempt.SittingID.String(),
				"exam_attempt_id": snapshot.Attempt.ID.String(),
			},
		})
		if auditErr != nil {
			return store.ExamAttemptManagerSnapshot{}, auditErr
		}
		if auditErr = s.auditor.Fail(ctx, auditID, "exam.attempt.not_found"); auditErr != nil {
			return store.ExamAttemptManagerSnapshot{}, auditErr
		}

		return store.ExamAttemptManagerSnapshot{}, apperror.New("exam.attempt.not_found")
	}

	clone := cloneManagerSnapshot(snapshot)
	if clone == nil {
		return store.ExamAttemptManagerSnapshot{}, apperror.New("exam.attempt.unavailable")
	}

	return *clone, nil
}

// ListManagedAttemptsQuery selects one page of a sitting's attempts.
// BeforeCreatedAt and BeforeAttemptID are the keyset cursor of the last
// attempt seen.
type ListManagedAttemptsQuery struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	States          []model.ExamAttemptState
	BeforeCreatedAt time.Time
	BeforeAttemptID model.ExamAttemptID
	Limit           int
}

// ManagedAttemptPage is one page of snapshots; HasMore reports that a later
// page exists. ManagerEnds holds who ended each attempt a manager ended, for
// a caller who may manage the sitting.
type ManagedAttemptPage struct {
	Items       []store.ExamAttemptManagerSnapshot
	ManagerEnds map[model.ExamAttemptID]ManagerEnd
	HasMore     bool
}

// SittingCandidateStatusesQuery selects one page of per-candidate statuses
// after AfterCandidateUserID.
type SittingCandidateStatusesQuery struct {
	ExamID               model.ExamID
	SittingID            model.ExamSittingID
	AfterCandidateUserID model.UserID
	Limit                int
}

// SittingCandidateStatusesPage is one page of per-candidate statuses; HasMore
// reports that a later page exists. Details holds each candidate's board
// details.
type SittingCandidateStatusesPage struct {
	store.SittingCandidateStatusPage
	Details map[model.UserID]CandidateBoardDetail
	HasMore bool
}

// ListSittingCandidateStatuses excludes the caller. It requires sitting view;
// whether the caller also holds sitting management decides if re-allow is
// offered.
func (s *Service) ListSittingCandidateStatuses(
	ctx context.Context,
	inv invocation.Invocation,
	query SittingCandidateStatusesQuery,
) (_ SittingCandidateStatusesPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil ||
		!query.ExamID.IsValid() ||
		!query.SittingID.IsValid() ||
		query.Limit < 1 ||
		query.Limit > 200 ||
		!query.AfterCandidateUserID.IsZero() && !query.AfterCandidateUserID.IsValid() {
		return SittingCandidateStatusesPage{}, apperror.Invalid("exam.attempt.invalid", "candidate_statuses")
	}

	if err := s.managers.AuthorizeSittingView(ctx, inv, query.SittingID); err != nil {
		return SittingCandidateStatusesPage{}, err
	}

	reallowAuthorized, err := s.sittingManageAuthorized(ctx, inv, query.SittingID)
	if err != nil {
		return SittingCandidateStatusesPage{}, err
	}

	stored, err := s.attempts.ListSittingCandidateStatuses(ctx, store.SittingCandidateStatusListOptions{
		ExamID:                 query.ExamID,
		SittingID:              query.SittingID,
		ExcludeCandidateUserID: principal.UserID,
		AfterCandidateUserID:   query.AfterCandidateUserID,
		ReallowAuthorized:      reallowAuthorized,
		Limit:                  query.Limit + 1,
	})
	if err != nil {
		return SittingCandidateStatusesPage{}, storeError(storeErrors, err)
	}

	page := SittingCandidateStatusesPage{SittingCandidateStatusPage: *stored, HasMore: len(stored.Items) > query.Limit}
	page.Items = append([]store.SittingCandidateStatusItem(nil), stored.Items[:min(len(stored.Items), query.Limit)]...)
	for i := range page.Items {
		if page.Items[i].NativeSecurity != nil {
			if page.Items[i].NativeSecurity.Validate() != nil {
				return SittingCandidateStatusesPage{}, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
			}

			value := page.Items[i].NativeSecurity.Clone()
			page.Items[i].NativeSecurity = &value
		}
	}

	if page.Details, err = s.candidateBoardDetails(ctx, page.SittingCandidateStatusPage, reallowAuthorized); err != nil {
		return SittingCandidateStatusesPage{}, err
	}

	return page, nil
}

// sittingManageAuthorized reports whether the caller may also manage the
// sitting. A denial is an answer, not a failure.
func (s *Service) sittingManageAuthorized(ctx context.Context, inv invocation.Invocation, sittingID model.ExamSittingID) (bool, error) {
	if _, err := s.managers.AuthorizeSittingManage(ctx, inv, sittingID); err != nil {
		var fault *apperror.Error
		if !errors.As(err, &fault) || fault.Code() != "authorization.denied" {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

// ListExamAttempts requires sitting view and excludes the caller's own
// attempt.
func (s *Service) ListExamAttempts(
	ctx context.Context,
	inv invocation.Invocation,
	query ListManagedAttemptsQuery,
) (_ ManagedAttemptPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.ExamID.IsValid() ||
		!query.SittingID.IsValid() ||
		query.Limit < 1 ||
		query.Limit > 200 ||
		(query.BeforeCreatedAt.IsZero() != query.BeforeAttemptID.IsZero()) {
		return ManagedAttemptPage{}, apperror.Invalid("exam.attempt.invalid", "attempt_list")
	}

	if err := s.managers.AuthorizeSittingView(ctx, inv, query.SittingID); err != nil {
		return ManagedAttemptPage{}, err
	}
	manageAuthorized, err := s.sittingManageAuthorized(ctx, inv, query.SittingID)
	if err != nil {
		return ManagedAttemptPage{}, err
	}

	rows, err := s.attempts.List(ctx, store.ExamAttemptManagerListOptions{
		ExamID:                 query.ExamID,
		SittingID:              query.SittingID,
		ExcludeCandidateUserID: inv.Principal().UserID,
		States:                 append([]model.ExamAttemptState(nil), query.States...),
		BeforeCreatedAt:        model.TimeUTC(query.BeforeCreatedAt),
		BeforeAttemptID:        query.BeforeAttemptID,
		Limit:                  query.Limit + 1,
	})
	if err != nil {
		return ManagedAttemptPage{}, storeError(storeErrors, err)
	}

	page := ManagedAttemptPage{
		Items:   make([]store.ExamAttemptManagerSnapshot, 0, min(len(rows), query.Limit)),
		HasMore: len(rows) > query.Limit,
	}
	for index, row := range rows {
		if index == query.Limit {
			break
		}

		if !row.SuspensionActiveOrAbsent() {
			return ManagedAttemptPage{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent managed Attempt page"))
		}

		page.Items = append(page.Items, *cloneManagerSnapshot(&row))
	}

	ids := make([]model.ExamAttemptID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.Attempt.ID)
	}
	if page.ManagerEnds, err = s.managerEnds(ctx, query.SittingID, ids, manageAuthorized); err != nil {
		return ManagedAttemptPage{}, err
	}

	return page, nil
}

func cloneManagerSnapshot(snapshot *store.ExamAttemptManagerSnapshot) *store.ExamAttemptManagerSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	if snapshot.Attempt != nil {
		value := *snapshot.Attempt
		clone.Attempt = &value
	}
	if snapshot.Workspace != nil {
		value := *snapshot.Workspace
		clone.Workspace = &value
	}
	if snapshot.LatestParticipation != nil {
		value := *snapshot.LatestParticipation
		clone.LatestParticipation = &value
	}
	if snapshot.CurrentConnection != nil {
		value := *snapshot.CurrentConnection
		clone.CurrentConnection = &value
	}
	if snapshot.ActiveSuspension != nil {
		value := *snapshot.ActiveSuspension
		clone.ActiveSuspension = &value
	}
	return &clone
}
