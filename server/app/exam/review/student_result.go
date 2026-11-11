// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/safemarkdown"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// GetStudentExamResult returns the caller's own released result for an
// Attempt, with sanitized student remarks.
func (s *Service) GetStudentExamResult(
	ctx context.Context,
	invocation invocation.Invocation,
	attemptID model.ExamAttemptID,
) (_ *model.StudentResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := invocation.Principal()
	if !attemptID.IsValid() || principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "student_result")
	}

	result, err := s.reviews.GetReleasedStudentResult(ctx, attemptID, principal.UserID)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}

	if result == nil ||
		result.ReviewID.IsZero() ||
		result.SubmissionID.IsZero() ||
		result.AttemptID != attemptID ||
		result.CandidateUserID != principal.UserID ||
		result.ReleasedAt.IsZero() {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent Student Result"))
	}

	clone := *result
	clone.StudentRemarksMarkdown = safemarkdown.Sanitize(clone.StudentRemarksMarkdown)
	return &clone, nil
}
