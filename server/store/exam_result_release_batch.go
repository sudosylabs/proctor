// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	ExamResultReleaseBatchOperation = "exam.integrity_review.release_batch"
	// A request stops processing items before its claim can expire.
	ExamResultReleaseBatchWorkTime = 2 * time.Minute
	ExamResultReleaseBatchLease    = 3 * time.Minute
)

// ExamResultReleaseBatchStart reserves the original ordered selection for
// this command, together with its unpersisted audit attempt.
type ExamResultReleaseBatchStart struct {
	ExamID        model.ExamID
	SittingID     model.ExamSittingID
	SubmissionIDs []model.SubmissionID
	AllFinalized  bool
	Audit         *model.AuditEvent
}

// ExamResultReleaseBatchClaim identifies one execution. A completed command
// has no Token and returns its retained Items.
type ExamResultReleaseBatchClaim struct {
	Token         string
	SubmissionIDs []model.SubmissionID
	Items         []ExamResultReleaseBatchItem
}

type ExamResultReleaseOutcome string

const (
	ExamResultReleased        ExamResultReleaseOutcome = "released"
	ExamResultAlreadyReleased ExamResultReleaseOutcome = "already_released"
	ExamResultNotFinalized    ExamResultReleaseOutcome = "not_finalized"
	ExamResultReleaseDenied   ExamResultReleaseOutcome = "denied"
	ExamResultReleaseFailed   ExamResultReleaseOutcome = "failed"
)

type ExamResultReleaseBatchItem struct {
	SubmissionID model.SubmissionID
	Outcome      ExamResultReleaseOutcome
	ErrorCode    string
}

type ExamResultReleaseBatchCompletion struct {
	Token string
	Items []ExamResultReleaseBatchItem
}
