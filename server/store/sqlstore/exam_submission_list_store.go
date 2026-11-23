// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type submissionListRow struct {
	SubmissionID             string         `db:"submission_id"`
	ExamID                   string         `db:"exam_id"`
	ExamTitle                string         `db:"exam_title"`
	SittingID                string         `db:"exam_sitting_id"`
	AttemptID                string         `db:"exam_attempt_id"`
	CandidateUserID          string         `db:"candidate_user_id"`
	Username                 string         `db:"username"`
	DisplayName              string         `db:"display_name"`
	SubmittedAt              time.Time      `db:"submitted_at"`
	ReceiptCode              string         `db:"receipt_code"`
	FlagCount                int64          `db:"flag_count"`
	UnresolvedIntegrityCount int64          `db:"unresolved_integrity_count"`
	IntegrityState           string         `db:"integrity_state"`
	ReviewVisible            bool           `db:"review_visible"`
	ReviewID                 sql.NullString `db:"review_id"`
	ReviewState              sql.NullString `db:"review_state"`
	ReleaseState             sql.NullString `db:"release_state"`
	ReviewRevision           sql.NullInt64  `db:"review_revision"`
	FinalizedByUserID        sql.NullString `db:"finalized_by_user_id"`
	FinalizedAt              sql.NullTime   `db:"finalized_at"`
	ReleasedAt               sql.NullTime   `db:"released_at"`
}

type submissionListCountRow struct {
	Total              int64 `db:"total"`
	Reviewed           int64 `db:"reviewed"`
	Released           int64 `db:"released"`
	FlaggedSubmissions int64 `db:"flagged_submissions"`
	Flags              int64 `db:"flags"`
}

// submissionListNameMaximumBytes bounds a name keyset, which is a display
// name the store itself returned.
const submissionListNameMaximumBytes = 1024

// ListSubmissions returns one page of sealed submissions of one sitting, or
// of every exam the visibility reaches, with each candidate, flag count and
// review, ordered by the sort and then by submission ID. Counts read the same
// rows without the filter, in one more grouped query.
func (s *SQLExamIntegrityReviewStore) ListSubmissions(
	ctx context.Context,
	options store.SubmissionListOptions,
) (*store.SubmissionListPage, error) {
	if err := validateSubmissionListOptions(options); err != nil {
		return nil, err
	}

	cte, cteArgs := submissionListScope(options)
	reviewState := `CASE WHEN listed.review_id IS NULL THEN 'none' ELSE listed.review_state END`
	query := cte + `
		SELECT listed.*
		FROM submission_list listed
		WHERE TRUE`
	args := append([]any{}, cteArgs...)
	filter := options.Filter
	if len(filter.ReviewStates) > 0 {
		states := make([]string, len(filter.ReviewStates))
		for i, state := range filter.ReviewStates {
			states[i] = string(state)
		}
		query += ` AND listed.review_visible AND ` + reviewState + ` = ANY(?)`
		args = append(args, pq.Array(states))
	}
	if len(filter.ReleaseStates) > 0 {
		states := make([]string, len(filter.ReleaseStates))
		for i, state := range filter.ReleaseStates {
			states[i] = string(state)
		}
		query += ` AND listed.review_visible AND COALESCE(listed.release_state, 'withheld') = ANY(?)`
		args = append(args, pq.Array(states))
	}
	if filter.HasFlags != nil {
		query += ` AND (listed.flag_count > 0) = ?`
		args = append(args, *filter.HasFlags)
	}

	order := ` ORDER BY listed.submitted_at, listed.submission_id`
	if options.Sort == store.SubmissionListByName {
		order = ` ORDER BY lower(listed.display_name), listed.submission_id`
	}
	if after := options.After; after != nil {
		if options.Sort == store.SubmissionListByName {
			query += ` AND (lower(listed.display_name), listed.submission_id) > (lower(?), ?)`
			args = append(args, after.Name, after.SubmissionID.String())
		} else {
			query += ` AND (listed.submitted_at, listed.submission_id) > (?, ?)`
			args = append(args, model.TimeUTC(after.SubmittedAt), after.SubmissionID.String())
		}
	}
	query += order + ` LIMIT ?`
	args = append(args, options.Limit)

	var rows []submissionListRow
	if err := s.GetMaster().Select(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list Submissions: %w", err)
	}
	page := &store.SubmissionListPage{Items: make([]store.SubmissionListItem, 0, len(rows))}
	for _, row := range rows {
		item, err := row.item()
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, item)
	}

	if options.IncludeCounts {
		var counts submissionListCountRow
		if err := s.GetMaster().Get(ctx, &counts, cte+`
			SELECT COUNT(*) AS total,
				COUNT(*) FILTER (WHERE listed.review_state = 'finalized') AS reviewed,
				COUNT(*) FILTER (WHERE listed.release_state = 'released') AS released,
				COUNT(*) FILTER (WHERE listed.flag_count > 0) AS flagged_submissions,
				COALESCE(SUM(listed.flag_count), 0) AS flags
			FROM submission_list listed`, cteArgs...); err != nil {
			return nil, fmt.Errorf("count Submissions: %w", err)
		}
		page.Counts = &store.SubmissionListCounts{Total: counts.Total, FlaggedSubmissions: counts.FlaggedSubmissions, Flags: counts.Flags}
		if options.ReviewVisible {
			page.Counts.Reviewed, page.Counts.Released = &counts.Reviewed, &counts.Released
		}
	}
	return page, nil
}

// submissionListScope defines submission_list, the sealed submissions the
// options reach, other than the reader's own, with review_visible saying
// whether each row's review may be shown.
func submissionListScope(options store.SubmissionListOptions) (string, []any) {
	var ctes []string
	var cteArgs []any
	scope := `s.exam_sitting_id = ? AND s.exam_id = ?`
	scopeArgs := []any{options.SittingID.String(), options.ExamID.String()}
	reviewVisible := `?::boolean`
	reviewArgs := []any{options.ReviewVisible}
	if options.Visibility != nil {
		view := newExamManagerVisibility("submission_view", *options.Visibility, "e")
		ctes = append(ctes, view.ctes)
		cteArgs = append(cteArgs, view.cteArgs...)
		scope, scopeArgs = view.predicate, view.predicateArgs
		reviewVisible, reviewArgs = `FALSE`, nil
		if options.ReviewVisibility != nil {
			review := newExamManagerVisibility("submission_review", *options.ReviewVisibility, "e")
			ctes = append(ctes, review.ctes)
			cteArgs = append(cteArgs, review.cteArgs...)
			reviewVisible, reviewArgs = review.predicate, review.predicateArgs
		}
	}
	if len(options.SubmissionIDs) > 0 {
		ids := make([]string, len(options.SubmissionIDs))
		for i, id := range options.SubmissionIDs {
			ids[i] = id.String()
		}
		scope += ` AND submission.id = ANY(?)`
		scopeArgs = append(scopeArgs, pq.Array(ids))
	}

	prefix := `WITH `
	if len(ctes) > 0 {
		prefix = `WITH RECURSIVE ` + strings.Join(ctes, `, `) + `, `
	}
	cte := prefix + `submission_list AS (
			SELECT submission.id AS submission_id, e.id AS exam_id, d.title AS exam_title,
				s.exam_sitting_id, s.id AS exam_attempt_id, candidate.id AS candidate_user_id,
				candidate.username, candidate.display_name, submission.submitted_at, submission.receipt_code,
				(SELECT COUNT(*) FROM integrity_flags flag WHERE flag.exam_attempt_id = s.id) AS flag_count,
				CASE WHEN submission.integrity_retired_at IS NULL THEN submission.unresolved_integrity_count ELSE 0 END
					AS unresolved_integrity_count,
				CASE WHEN submission.integrity_retired_at IS NULL THEN submission.integrity_state ELSE 'retired' END
					AS integrity_state,
				` + reviewVisible + ` AS review_visible,
				review.id AS review_id, review.state AS review_state, review.release_state,
				review.revision AS review_revision, review.finalized_by_user_id, review.finalized_at, review.released_at
			FROM exam_submissions submission
			JOIN exam_attempts s ON s.id = submission.exam_attempt_id
			JOIN exams e ON e.id = s.exam_id
			JOIN exam_drafts d ON d.exam_id = e.id
			JOIN users candidate ON candidate.id = s.candidate_user_id
			LEFT JOIN submission_reviews review ON review.submission_id = submission.id
			WHERE submission.sealed
				AND candidate.id <> ?
				AND ` + scope + `
		)`
	cteArgs = append(cteArgs, reviewArgs...)
	cteArgs = append(cteArgs, options.ExcludeCandidateUserID.String())
	cteArgs = append(cteArgs, scopeArgs...)
	return cte, cteArgs
}

func validateSubmissionListOptions(options store.SubmissionListOptions) error {
	sitting := options.ExamID.IsValid() && options.SittingID.IsValid()
	across := options.ExamID.IsZero() && options.SittingID.IsZero() && options.Visibility != nil
	if sitting == across ||
		across && (options.ReviewVisible || len(options.SubmissionIDs) > 0 || options.IncludeCounts ||
			!validExamManagerVisibility(*options.Visibility) ||
			options.ReviewVisibility != nil && !validExamManagerVisibility(*options.ReviewVisibility)) ||
		sitting && options.ReviewVisibility != nil ||
		!options.ExcludeCandidateUserID.IsValid() ||
		options.Limit < 1 || options.Limit > store.SubmissionListMaximum+1 ||
		len(options.SubmissionIDs) > store.SubmissionListMaximum ||
		options.Sort != store.SubmissionListBySubmitted && options.Sort != store.SubmissionListByName {
		return store.NewErrInvalidInput("submission", "list_options", nil)
	}
	for _, id := range options.SubmissionIDs {
		if !id.IsValid() {
			return store.NewErrInvalidInput("submission", "submission_ids", nil)
		}
	}
	for _, state := range options.Filter.ReviewStates {
		if !state.IsValid() {
			return store.NewErrInvalidInput("submission", "review_state", nil)
		}
	}
	for _, state := range options.Filter.ReleaseStates {
		if state != model.SubmissionReviewWithheld && state != model.SubmissionReviewReleased {
			return store.NewErrInvalidInput("submission", "release_state", nil)
		}
	}
	if after := options.After; after != nil {
		if !after.SubmissionID.IsValid() ||
			options.Sort == store.SubmissionListBySubmitted && after.SubmittedAt.IsZero() ||
			options.Sort == store.SubmissionListByName &&
				(after.Name == "" || len(after.Name) > submissionListNameMaximumBytes || !utf8.ValidString(after.Name)) {
			return store.NewErrInvalidInput("submission", "cursor", nil)
		}
	}
	return nil
}

func (row submissionListRow) item() (store.SubmissionListItem, error) {
	invalid := func(field string, err error) (store.SubmissionListItem, error) {
		return store.SubmissionListItem{}, invalidPersistedState("submission_list", field, err)
	}
	submissionID, err := model.ParseSubmissionID(row.SubmissionID)
	if err != nil {
		return invalid("submission_id", err)
	}
	examID, err := model.ParseExamID(row.ExamID)
	if err != nil {
		return invalid("exam_id", err)
	}
	sittingID, err := model.ParseExamSittingID(row.SittingID)
	if err != nil {
		return invalid("exam_sitting_id", err)
	}
	attemptID, err := model.ParseExamAttemptID(row.AttemptID)
	if err != nil {
		return invalid("exam_attempt_id", err)
	}
	candidateID, err := model.ParseUserID(row.CandidateUserID)
	if err != nil || row.Username == "" || row.DisplayName == "" {
		return invalid("candidate", err)
	}
	receipt, err := model.ParseSubmissionReceiptCode(row.ReceiptCode)
	if err != nil {
		return invalid("receipt_code", err)
	}
	integrity := model.SubmissionIntegrityState(row.IntegrityState)
	if integrity != model.SubmissionIntegritySettled && integrity != model.SubmissionIntegrityGapped &&
		integrity != model.SubmissionIntegrityRetired || row.FlagCount < 0 || row.UnresolvedIntegrityCount < 0 {
		return invalid("integrity", errors.New("invalid integrity standing"))
	}

	item := store.SubmissionListItem{
		SubmissionID: submissionID, ExamID: examID, ExamTitle: row.ExamTitle, SittingID: sittingID, AttemptID: attemptID,
		Candidate:   store.SittingCandidateIdentity{UserID: candidateID, Username: row.Username, DisplayName: row.DisplayName},
		SubmittedAt: model.TimeUTC(row.SubmittedAt), ReceiptCode: receipt, FlagCount: row.FlagCount,
		UnresolvedIntegrityCount: row.UnresolvedIntegrityCount, IntegrityState: integrity, ReviewVisible: row.ReviewVisible,
	}
	if !row.ReviewVisible || !row.ReviewID.Valid {
		return item, nil
	}
	reviewID, err := model.ParseSubmissionReviewID(row.ReviewID.String)
	state := model.SubmissionReviewState(row.ReviewState.String)
	release := model.SubmissionReviewReleaseState(row.ReleaseState.String)
	if err != nil ||
		state != model.SubmissionReviewDraft && state != model.SubmissionReviewFinalized ||
		release != model.SubmissionReviewWithheld && release != model.SubmissionReviewReleased ||
		!row.ReviewRevision.Valid || row.ReviewRevision.Int64 < 1 ||
		(state == model.SubmissionReviewFinalized) != (row.FinalizedByUserID.Valid && row.FinalizedAt.Valid) ||
		(release == model.SubmissionReviewReleased) != row.ReleasedAt.Valid {
		return invalid("review", err)
	}
	item.Review = &store.SubmissionListReview{ID: reviewID, State: state, ReleaseState: release, Revision: row.ReviewRevision.Int64}
	if row.FinalizedByUserID.Valid {
		if item.Review.FinalizedByUserID, err = model.ParseUserID(row.FinalizedByUserID.String); err != nil {
			return invalid("finalized_by_user_id", err)
		}
		item.Review.FinalizedAt = model.OptionalTimeFrom(model.TimeUTC(row.FinalizedAt.Time))
	}
	if row.ReleasedAt.Valid {
		item.Review.ReleasedAt = model.OptionalTimeFrom(model.TimeUTC(row.ReleasedAt.Time))
	}
	return item, nil
}
