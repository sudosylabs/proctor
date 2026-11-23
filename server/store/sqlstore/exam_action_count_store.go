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
	"time"

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CountExamActions counts, for one page of exams, the candidates who need a
// manager and the submissions waiting for review, each only where the
// visibility reaches the exam. It reads each count in one grouped query.
func (s SQLExamAuthoringStore) CountExamActions(
	ctx context.Context,
	options store.ExamActionCountOptions,
) (map[model.ExamID]store.ExamActionCounts, error) {
	if len(options.ExamIDs) > 201 ||
		!options.ViewerUserID.IsValid() ||
		!validExamManagerVisibility(options.SittingVisibility) ||
		!validExamManagerVisibility(options.ReviewVisibility) ||
		!store.ValidCountMaximum(options.Maximum) {
		return nil, store.NewErrInvalidInput("exam", "action_counts", nil)
	}
	counts := make(map[model.ExamID]store.ExamActionCounts, len(options.ExamIDs))
	if len(options.ExamIDs) == 0 {
		return counts, nil
	}
	ids := make([]string, len(options.ExamIDs))
	for index, id := range options.ExamIDs {
		if !id.IsValid() {
			return nil, store.NewErrInvalidInput("exam", "action_counts", nil)
		}
		ids[index] = id.String()
	}
	reader := s.catalogReader
	if reader == nil {
		reader = s.GetMaster()
	}

	sittings := newExamManagerVisibility("sitting_view", options.SittingVisibility, "e")
	reviews := newExamManagerVisibility("review", options.ReviewVisibility, "e")
	var rows []struct {
		ExamID         string        `db:"id"`
		SittingVisible bool          `db:"sitting_visible"`
		ReviewVisible  bool          `db:"review_visible"`
		PendingReview  sql.NullInt64 `db:"pending_review_count"`
	}
	args := append(append([]any{}, sittings.cteArgs...), reviews.cteArgs...)
	args = append(args, sittings.predicateArgs...)
	args = append(args, reviews.predicateArgs...)
	args = append(args, reviews.predicateArgs...)
	args = append(args, options.Maximum, pq.Array(ids))
	if err := reader.Select(ctx, &rows, `WITH RECURSIVE `+sittings.ctes+`, `+reviews.ctes+`
		SELECT e.id, `+sittings.predicate+` AS sitting_visible, `+reviews.predicate+` AS review_visible,
			CASE WHEN `+reviews.predicate+` THEN (
				SELECT COUNT(*)
				FROM (
					SELECT 1
					FROM exam_submissions submission
					JOIN exam_attempts a ON a.id = submission.exam_attempt_id
					WHERE a.exam_id = e.id
						AND submission.sealed = true
						AND submission.integrity_retired_at IS NULL
						AND NOT EXISTS (
							SELECT 1
							FROM submission_reviews review
							WHERE review.submission_id = submission.id
								AND review.state = 'finalized'
						)
					LIMIT ?
				) pending
			) END AS pending_review_count
		FROM exams e
		WHERE e.id = ANY(?)`, args...); err != nil {
		return nil, fmt.Errorf("count Exam review backlog: %w", err)
	}

	visibleSittings := map[model.ExamID]bool{}
	for _, row := range rows {
		examID, err := model.ParseExamID(row.ExamID)
		if err != nil || row.ReviewVisible != row.PendingReview.Valid || row.PendingReview.Int64 < 0 ||
			row.PendingReview.Int64 > int64(options.Maximum) {
			return nil, invalidPersistedState("exam", "action_counts", errors.Join(err, errors.New("invalid review count")))
		}
		value := store.ExamActionCounts{}
		if row.ReviewVisible {
			pending := int(row.PendingReview.Int64)
			value.PendingReview = &pending
		}
		if row.SittingVisible {
			zero := 0
			value.NeedsAttention = &zero
			visibleSittings[examID] = true
		}
		counts[examID] = value
	}
	if len(visibleSittings) == 0 {
		return counts, nil
	}

	var now []time.Time
	if err := reader.Select(ctx, &now, `SELECT statement_timestamp()`); err != nil || len(now) != 1 {
		return nil, fmt.Errorf("read Exam attention time: %w", errors.Join(err, errors.New("no time")))
	}
	serverTime := model.TimeUTC(now[0])

	visibleIDs := make([]string, 0, len(visibleSittings))
	for id := range visibleSittings {
		visibleIDs = append(visibleIDs, id.String())
	}
	// The board counts candidates of open and paused sittings only; the
	// viewer is never their own candidate.
	cte, cteArgs := candidateStatusCTE(serverTime, options.ViewerUserID,
		`SELECT s.id, s.class_id, s.state, s.scheduled_start_at, s.opened_at
			FROM exam_sittings s
			WHERE s.exam_id = ANY(?)
				AND s.state IN ('open', 'paused')`, pq.Array(visibleIDs))
	var attention []struct {
		ExamID string `db:"exam_id"`
		Count  int    `db:"needs_attention"`
	}
	if err := reader.Select(ctx, &attention, cte+`
		SELECT sitting.exam_id, LEAST(COUNT(*) FILTER (WHERE `+candidateNeedsAttentionSQL+`), ?) AS needs_attention
		FROM candidate_status status
		JOIN exam_sittings sitting ON sitting.id = status.exam_sitting_id
		GROUP BY sitting.exam_id`, append(cteArgs, options.Maximum)...); err != nil {
		return nil, fmt.Errorf("count Exam attention: %w", err)
	}
	for _, row := range attention {
		examID, err := model.ParseExamID(row.ExamID)
		value, found := counts[examID]
		if err != nil || !found || value.NeedsAttention == nil || row.Count < 0 {
			return nil, invalidPersistedState("exam", "attention_count", errors.Join(err, errors.New("invalid attention count")))
		}
		count := row.Count
		value.NeedsAttention = &count
		counts[examID] = value
	}

	return counts, nil
}
