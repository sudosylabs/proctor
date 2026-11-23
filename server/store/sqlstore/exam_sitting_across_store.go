// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type examSittingAcrossRow struct {
	SittingID        string    `db:"id"`
	ExamID           string    `db:"exam_id"`
	ExamTitle        string    `db:"exam_title"`
	AcademicUnitID   string    `db:"academic_unit_id"`
	ClassID          string    `db:"class_id"`
	ClassDisplayName string    `db:"class_display_name"`
	State            string    `db:"state"`
	Revision         int64     `db:"revision"`
	ScheduledStartAt time.Time `db:"scheduled_start_at"`
	ScheduledEndAt   time.Time `db:"scheduled_end_at"`
}

// ListAcrossExams reads the page and, in one more grouped query, the
// candidate counts of its open and paused sittings, both in one transaction
// so the counts describe the page they came with.
func (s *SQLExamSittingStore) ListAcrossExams(
	ctx context.Context,
	options store.ExamSittingAcrossListOptions,
) (*store.ExamSittingAcrossPage, error) {
	if !validExamManagerVisibility(options.Visibility) ||
		!options.ReaderUserID.IsValid() ||
		options.Limit < 1 || options.Limit > 201 ||
		options.AfterScheduledStartAt.IsZero() != options.AfterSittingID.IsZero() ||
		!options.AfterSittingID.IsZero() && !options.AfterSittingID.IsValid() ||
		!options.AcademicUnitID.IsZero() && !options.AcademicUnitID.IsValid() ||
		len(options.States) > 7 {
		return nil, store.NewErrInvalidInput("exam_sitting", "list_options", nil)
	}
	for _, state := range options.States {
		if !state.IsValid() {
			return nil, store.NewErrInvalidInput("exam_sitting", "state", nil)
		}
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "list Sittings across Exams", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamSittingAcrossPage, error) {
		var serverTime time.Time
		if err := tx.Get(ctx, &serverTime, `SELECT statement_timestamp()`); err != nil {
			return nil, fmt.Errorf("read database time: %w", err)
		}
		serverTime = model.TimeUTC(serverTime)

		visibility := newExamManagerVisibility("sitting_view", options.Visibility, "e")
		query := `WITH RECURSIVE ` + visibility.ctes + `
			SELECT s.id, s.exam_id, d.title AS exam_title, e.academic_unit_id, s.class_id,
				c.display_name AS class_display_name, s.state, s.revision, s.scheduled_start_at, s.scheduled_end_at
			FROM exam_sittings s
			JOIN exams e ON e.id = s.exam_id
			JOIN exam_drafts d ON d.exam_id = e.id
			JOIN classes c ON c.id = s.class_id
			WHERE ` + visibility.predicate
		args := append(append([]any{}, visibility.cteArgs...), visibility.predicateArgs...)
		if len(options.States) > 0 {
			states := make([]string, len(options.States))
			for i, state := range options.States {
				states[i] = string(state)
			}
			query += ` AND s.state = ANY(?)`
			args = append(args, pq.Array(states))
		}
		if !options.StartsBefore.IsZero() {
			query += ` AND s.scheduled_start_at < ?`
			args = append(args, model.TimeUTC(options.StartsBefore))
		}
		if !options.EndsAfter.IsZero() {
			query += ` AND s.scheduled_end_at > ?`
			args = append(args, model.TimeUTC(options.EndsAfter))
		}
		if !options.AcademicUnitID.IsZero() {
			query += ` AND e.academic_unit_id = ?`
			args = append(args, options.AcademicUnitID.String())
		}
		if !options.AfterSittingID.IsZero() {
			query += ` AND (s.scheduled_start_at, s.id) > (?, ?)`
			args = append(args, model.TimeUTC(options.AfterScheduledStartAt), options.AfterSittingID.String())
		}
		query += ` ORDER BY s.scheduled_start_at, s.id LIMIT ?`
		args = append(args, options.Limit)

		var rows []examSittingAcrossRow
		if err := tx.Select(ctx, &rows, query, args...); err != nil {
			return nil, fmt.Errorf("list Sittings across Exams: %w", err)
		}

		page := &store.ExamSittingAcrossPage{ServerTime: serverTime, Items: make([]store.ExamSittingAcrossItem, 0, len(rows))}
		open := make([]string, 0, len(rows))
		for _, row := range rows {
			item, err := row.item()
			if err != nil {
				return nil, err
			}
			if item.State == model.ExamSittingOpen || item.State == model.ExamSittingPaused {
				open = append(open, item.SittingID.String())
			}
			page.Items = append(page.Items, item)
		}
		if len(open) == 0 {
			return page, nil
		}

		counts, err := countCandidateStatuses(ctx, tx, serverTime, options.ReaderUserID,
			`SELECT id, class_id, state, scheduled_start_at, opened_at FROM exam_sittings WHERE id = ANY(?)`, pq.Array(open))
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			item := &page.Items[i]
			if item.State != model.ExamSittingOpen && item.State != model.ExamSittingPaused {
				continue
			}
			item.Counts = counts[item.SittingID]
			if item.Counts == nil {
				item.Counts = &store.SittingCandidateStatusCounts{ByPresenceState: map[store.SittingCandidatePresenceState]int64{}}
			}
		}
		return page, nil
	})
}

func (row examSittingAcrossRow) item() (store.ExamSittingAcrossItem, error) {
	sittingID, err := model.ParseExamSittingID(row.SittingID)
	if err != nil {
		return store.ExamSittingAcrossItem{}, invalidPersistedState("exam_sitting", "id", err)
	}
	examID, err := model.ParseExamID(row.ExamID)
	if err != nil {
		return store.ExamSittingAcrossItem{}, invalidPersistedState("exam_sitting", "exam_id", err)
	}
	unitID, err := model.ParseAcademicUnitID(row.AcademicUnitID)
	if err != nil {
		return store.ExamSittingAcrossItem{}, invalidPersistedState("exam_sitting", "academic_unit_id", err)
	}
	classID, err := model.ParseClassID(row.ClassID)
	if err != nil {
		return store.ExamSittingAcrossItem{}, invalidPersistedState("exam_sitting", "class_id", err)
	}
	state := model.ExamSittingState(row.State)
	if !state.IsValid() || row.Revision < 1 || !row.ScheduledEndAt.After(row.ScheduledStartAt) {
		return store.ExamSittingAcrossItem{}, invalidPersistedState("exam_sitting", "state", nil)
	}
	return store.ExamSittingAcrossItem{
		SittingID: sittingID, ExamID: examID, ExamTitle: row.ExamTitle, AcademicUnitID: unitID,
		ClassID: classID, ClassDisplayName: row.ClassDisplayName, State: state, Revision: row.Revision,
		ScheduledStartAt: model.TimeUTC(row.ScheduledStartAt), ScheduledEndAt: model.TimeUTC(row.ScheduledEndAt),
	}, nil
}
