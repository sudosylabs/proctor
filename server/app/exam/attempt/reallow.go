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
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ReallowCommand asks to close one active Attempt Suspension and return the
// Attempt to active, fenced by ExpectedAttemptRevision. It carries the
// manager's private reason and is idempotent by IdempotencyKey.
type ReallowCommand struct {
	ExamID                  model.ExamID
	SittingID               model.ExamSittingID
	AttemptID               model.ExamAttemptID
	SuspensionID            model.AttemptSuspensionID
	ExpectedAttemptRevision int64
	PrivateReason           string
	IdempotencyKey          string
}

// ReallowResult is the re-allowed Attempt and its closed Attempt Suspension,
// with the Exam, Sitting, Class, and candidate they belong to.
type ReallowResult struct {
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	Attempt         model.ExamAttempt
	Suspension      store.ExamAttemptSuspensionView
	Replayed        bool
}

// ReallowExamAttempt idempotently closes the active Attempt Suspension and
// returns the Attempt to active, fenced by ExpectedAttemptRevision. It
// authorizes Sitting management on the exact Sitting before reading the
// Attempt and preserves all evidence.
func (s *Service) ReallowExamAttempt(
	ctx context.Context,
	inv invocation.Invocation,
	command ReallowCommand,
) (_ ReallowResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil {
		return ReallowResult{}, apperror.New("authentication.token_invalid")
	}

	if !command.ExamID.IsValid() ||
		!command.SittingID.IsValid() ||
		!command.AttemptID.IsValid() ||
		!command.SuspensionID.IsValid() ||
		command.ExpectedAttemptRevision < 1 ||
		!validReallowReason(command.PrivateReason) {
		return ReallowResult{}, apperror.Invalid("exam.attempt.invalid", "reallow")
	}

	commandIdempotency, err := prepareReallowIdempotency(inv, command)
	if err != nil {
		return ReallowResult{}, err
	}

	// Authorize the exact Sitting before reading the Attempt so existence is
	// not probeable; later exits complete the audit attempt as failed.
	management, err := s.managers.AuthorizeSittingManage(ctx, inv, command.SittingID)
	override := management.Override
	if err != nil {
		return ReallowResult{}, err
	}

	snapshot, err := s.sittings.Resolve(ctx, command.SittingID)
	if err != nil {
		return ReallowResult{}, storeError(storeErrors, err)
	}
	if !snapshot.ConsistentWithOwningClass(command.SittingID) {
		return ReallowResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("incomplete Sitting ownership projection"))
	}

	action := model.ActionExamSittingManage
	if override {
		action = model.ActionExamSittingManageOverride
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    snapshot.Sitting.ClassID.String(),
		Operation:  store.ExamAttemptReallowOperation,
		Value: map[string]any{
			"exam_id":                   command.ExamID.String(),
			"exam_sitting_id":           command.SittingID.String(),
			"exam_attempt_id":           command.AttemptID.String(),
			"suspension_id":             command.SuspensionID.String(),
			"expected_attempt_revision": command.ExpectedAttemptRevision,
		},
	})
	if err != nil {
		return ReallowResult{}, err
	}
	if snapshot.Sitting.ExamID != command.ExamID {
		return ReallowResult{}, s.failAudit(ctx, auditID, apperror.New("exam.attempt.not_found"))
	}

	managerSnapshot, err := s.attempts.Get(ctx, command.ExamID, command.AttemptID)
	if err != nil {
		return ReallowResult{}, s.failAudit(ctx, auditID, err)
	}

	if !managerSnapshot.ConsistentWith(command.ExamID) {
		return ReallowResult{}, s.failAudit(
			ctx,
			auditID,
			apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent re-allow Attempt projection")),
		)
	}

	if managerSnapshot.Attempt.SittingID != command.SittingID || managerSnapshot.Attempt.CandidateUserID == principal.UserID {
		return ReallowResult{}, s.failAudit(ctx, auditID, apperror.New("exam.attempt.not_found"))
	}

	at := model.TimeUTC(s.now())
	stored, err := s.attempts.ReallowAttempt(ctx, &store.ExamAttemptReallow{
		ExamID:           command.ExamID,
		SittingID:        command.SittingID,
		AttemptID:        command.AttemptID,
		SuspensionID:     command.SuspensionID,
		ActorUserID:      principal.UserID,
		ManagerOverride:  override,
		ExpectedRevision: command.ExpectedAttemptRevision,
		PrivateReason:    command.PrivateReason,
		AuditEventID:     auditID,
		AuditAt:          at,
	}, commandIdempotency)
	if err != nil {
		return ReallowResult{}, s.failAudit(ctx, auditID, err)
	}

	if !stored.ConsistentWith(
		command.ExamID,
		command.SittingID,
		snapshot.Sitting.ClassID,
		command.AttemptID,
		command.ExpectedAttemptRevision,
		command.SuspensionID,
		principal.UserID,
	) {
		return ReallowResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Attempt re-allow outcome"))
	}

	result := ReallowResult{
		ExamID:          stored.ExamID,
		SittingID:       stored.SittingID,
		ClassID:         stored.ClassID,
		CandidateUserID: stored.CandidateUserID,
		Attempt:         *stored.Attempt,
		Suspension:      *stored.Suspension,
		Replayed:        stored.Replayed,
	}
	if !result.Replayed {
		if effectErr := s.publishAttemptReallowed(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_reallowed", effectErr)
		}
	}

	return result, nil
}

func validReallowReason(reason string) bool {
	return utf8.ValidString(reason) &&
		reason == strings.TrimSpace(reason) &&
		utf8.RuneCountInString(reason) >= 1 &&
		utf8.RuneCountInString(reason) <= model.AttemptSuspensionPrivateReasonMaximumRunes &&
		len(reason) <= 4000
}
