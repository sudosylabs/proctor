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

// SubmissionListMaximum bounds one page of a submission list, and the
// submissions one bulk release names.
const SubmissionListMaximum = 200

// SubmissionListReviewState is where a submission's review stands: none
// until a manager opens one.
type SubmissionListReviewState string

const (
	SubmissionListReviewNone      SubmissionListReviewState = "none"
	SubmissionListReviewDraft     SubmissionListReviewState = "draft"
	SubmissionListReviewFinalized SubmissionListReviewState = "finalized"
)

// IsValid reports whether the state is none, draft or finalized.
func (s SubmissionListReviewState) IsValid() bool {
	return s == SubmissionListReviewNone || s == SubmissionListReviewDraft || s == SubmissionListReviewFinalized
}

// SubmissionListSort orders a submission list: by when the submission sealed
// or by the candidate's name, oldest or alphabetically first.
type SubmissionListSort string

const (
	SubmissionListBySubmitted SubmissionListSort = "submitted"
	SubmissionListByName      SubmissionListSort = "name"
)

// SubmissionListFilter narrows a list. ReviewStates and ReleaseStates keep
// any of their states and apply only where the reader may see reviews;
// HasFlags, when set, keeps submissions with or without integrity flags.
type SubmissionListFilter struct {
	ReviewStates  []SubmissionListReviewState
	ReleaseStates []model.SubmissionReviewReleaseState
	HasFlags      *bool
}

// SubmissionListCursor is the keyset of the last row seen, in the list's
// sort: SubmittedAt for the submitted sort, Name for the name sort.
type SubmissionListCursor struct {
	SubmittedAt  time.Time
	Name         string
	SubmissionID model.SubmissionID
}

// SubmissionListOptions selects one page of sealed submissions. A sitting
// list names ExamID and SittingID, and ReviewVisible says whether the reader
// may see reviews there. A list across exams leaves them zero and gives
// Visibility, the submission.view scopes, and ReviewVisibility, the
// submission.review scopes that decide per row whether its review shows.
type SubmissionListOptions struct {
	ExamID                 model.ExamID
	SittingID              model.ExamSittingID
	ReviewVisible          bool
	Visibility             *ExamListVisibility
	ReviewVisibility       *ExamListVisibility
	ExcludeCandidateUserID model.UserID
	// SubmissionIDs, when set, keeps only these submissions of the sitting.
	SubmissionIDs []model.SubmissionID
	Filter        SubmissionListFilter
	Sort          SubmissionListSort
	After         *SubmissionListCursor
	// IncludeCounts asks for Counts, which ignore Filter and After.
	IncludeCounts bool
	Limit         int
}

// SubmissionListReview is a submission's review as the list shows it, only
// to a reader who may see reviews.
type SubmissionListReview struct {
	ID                model.SubmissionReviewID
	State             model.SubmissionReviewState
	ReleaseState      model.SubmissionReviewReleaseState
	Revision          int64
	FinalizedByUserID model.UserID
	FinalizedAt       model.OptionalTime
	ReleasedAt        model.OptionalTime
}

// SubmissionListItem is one sealed submission with its candidate and
// integrity standing. Review is nil when there is none, and also when the
// reader may not see it: ReviewVisible tells them apart.
type SubmissionListItem struct {
	SubmissionID             model.SubmissionID
	ExamID                   model.ExamID
	ExamTitle                string
	SittingID                model.ExamSittingID
	AttemptID                model.ExamAttemptID
	Candidate                SittingCandidateIdentity
	SubmittedAt              time.Time
	ReceiptCode              model.SubmissionReceiptCode
	FlagCount                int64
	UnresolvedIntegrityCount int64
	IntegrityState           model.SubmissionIntegrityState
	ReviewVisible            bool
	Review                   *SubmissionListReview
}

// SubmissionListCounts counts every listed submission, other than the
// reader's own, whatever the filter. Reviewed and Released are set only when
// the reader may see reviews of every one of them.
type SubmissionListCounts struct {
	Total              int64
	Reviewed           *int64
	Released           *int64
	FlaggedSubmissions int64
	Flags              int64
}

// SubmissionListPage is one page; Counts is set when asked for.
type SubmissionListPage struct {
	Items  []SubmissionListItem
	Counts *SubmissionListCounts
}
