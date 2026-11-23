// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// liveEvidenceRetiredSQL is true once retention retired the attempt's
// integrity data; the live reads then return nothing, as the Submission's
// reads do.
const liveEvidenceRetiredSQL = `EXISTS (SELECT 1 FROM exam_submissions retired
		WHERE retired.exam_attempt_id = ? AND retired.integrity_retired_at IS NOT NULL)`

// nativeConditionAtSQL is when a native condition record's rule changed,
// read from its canonical document, alias e.
const nativeConditionAtSQL = `(convert_from(e.canonical, 'UTF8')::jsonb -> 'transition' ->> 'at')::timestamptz`

// ResolveLiveEvidence names the attempt's exam, sitting and candidate at
// database time, and whether it is sealed or retired.
func (s *SQLExamIntegrityReviewStore) ResolveLiveEvidence(
	ctx context.Context,
	attemptID model.ExamAttemptID,
) (*store.LiveEvidenceScope, error) {
	if !attemptID.IsValid() {
		return nil, store.NewErrInvalidInput("exam_attempt", "id", nil)
	}
	var row struct {
		ExamID    string    `db:"exam_id"`
		SittingID string    `db:"exam_sitting_id"`
		Candidate string    `db:"candidate_user_id"`
		Submitted bool      `db:"submitted"`
		Retired   bool      `db:"retired"`
		AsOf      time.Time `db:"as_of"`
	}
	if err := s.GetMaster().Get(ctx, &row, `SELECT a.exam_id, a.exam_sitting_id, a.candidate_user_id,
			EXISTS (SELECT 1 FROM exam_submissions sub WHERE sub.exam_attempt_id = a.id AND sub.sealed) AS submitted,
			EXISTS (SELECT 1 FROM exam_submissions sub WHERE sub.exam_attempt_id = a.id
				AND sub.integrity_retired_at IS NOT NULL) AS retired,
			statement_timestamp() AS as_of
		FROM exam_attempts a
		WHERE a.id = ?`, attemptID.String()); err != nil {
		return nil, translateError("exam_attempt", attemptID.String(), err)
	}
	examID, err := model.ParseExamID(row.ExamID)
	if err != nil {
		return nil, invalidPersistedState("exam_attempt", "exam_id", err)
	}
	sittingID, err := model.ParseExamSittingID(row.SittingID)
	if err != nil {
		return nil, invalidPersistedState("exam_attempt", "exam_sitting_id", err)
	}
	candidateID, err := model.ParseUserID(row.Candidate)
	if err != nil {
		return nil, invalidPersistedState("exam_attempt", "candidate_user_id", err)
	}
	return &store.LiveEvidenceScope{AttemptID: attemptID, ExamID: examID, SittingID: sittingID, CandidateUserID: candidateID,
		Submitted: row.Submitted, Retired: row.Retired, AsOf: model.TimeUTC(row.AsOf)}, nil
}

func liveWindowSQL(column string, window store.LiveEvidenceWindow) (string, []any) {
	clause := ``
	var args []any
	if !window.From.IsZero() {
		clause += ` AND ` + column + ` >= ?`
		args = append(args, model.TimeUTC(window.From))
	}
	if !window.To.IsZero() {
		clause += ` AND ` + column + ` < ?`
		args = append(args, model.TimeUTC(window.To))
	}
	return clause, args
}

func validLiveWindow(window store.LiveEvidenceWindow) bool {
	return window.From.IsZero() || window.To.IsZero() || window.From.Before(window.To)
}

// ListLiveFlags returns one page of an attempt's flags, live or sealed.
func (s *SQLExamIntegrityReviewStore) ListLiveFlags(
	ctx context.Context,
	options store.LiveFlagListOptions,
) (*store.ExamIntegrityFlagPage, error) {
	if !options.AttemptID.IsValid() || options.Limit < 1 || options.Limit > store.LiveEvidenceReadMaximum ||
		!options.AfterFlagID.IsZero() && !options.AfterFlagID.IsValid() || !validLiveWindow(options.Window) {
		return nil, store.NewErrInvalidInput("integrity_flag", "live_list_options", nil)
	}
	window, windowArgs := liveWindowSQL("f.created_at", options.Window)
	query := `SELECT ` + integrityFlagSummaryColumns + `
		FROM integrity_flags f
		` + integrityFlagSummaryJoins + `
		WHERE f.exam_attempt_id = ? AND f.id > ? AND NOT ` + liveEvidenceRetiredSQL + window + `
		GROUP BY ` + integrityFlagSummaryGroup + `
		ORDER BY f.id
		LIMIT ?`
	args := append([]any{options.AttemptID.String(), options.AfterFlagID.String(), options.AttemptID.String()}, windowArgs...)
	args = append(args, options.Limit+1)
	var rows []integrityFlagSummaryRow
	if err := s.GetMaster().Select(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list live Integrity Flags: %w", err)
	}
	page := &store.ExamIntegrityFlagPage{HasMore: len(rows) > options.Limit, Items: []store.ExamIntegrityFlagSummary{}}
	for _, row := range rows[:min(len(rows), options.Limit)] {
		summary, err := row.summary()
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, summary)
	}
	return page, nil
}

const liveEvidenceColumns = `ev.id, ev.exam_attempt_id, ev.participation_id, ev.integrity_flag_id,
			ev.generation, ev.policy_kind, ev.focus_loss_signal_id, ev.sequence,
			ev.duration_milliseconds, ev.source, ev.missing_before, ev.observed_at, ev.recorded_at,
			ev.browser_detail_canonical, ev.detail`

// ListLiveEvidence returns one page of one flag's evidence.
func (s *SQLExamIntegrityReviewStore) ListLiveEvidence(
	ctx context.Context,
	options store.LiveEvidenceListOptions,
) (*store.ExamIntegrityEvidencePage, error) {
	if !options.AttemptID.IsValid() || !options.FlagID.IsValid() || options.Limit < 1 ||
		options.Limit > store.LiveEvidenceReadMaximum || !validLiveWindow(options.Window) ||
		!options.AfterEvidenceID.IsZero() && !options.AfterEvidenceID.IsValid() {
		return nil, store.NewErrInvalidInput("integrity_evidence", "live_list_options", nil)
	}
	window, windowArgs := liveWindowSQL("ev.observed_at", options.Window)
	query := `SELECT ` + liveEvidenceColumns + `
		FROM integrity_evidence ev
		WHERE ev.exam_attempt_id = ? AND ev.integrity_flag_id = ? AND ev.id > ? AND NOT ` + liveEvidenceRetiredSQL + window + `
		ORDER BY ev.id
		LIMIT ?`
	args := append([]any{options.AttemptID.String(), options.FlagID.String(), options.AfterEvidenceID.String(),
		options.AttemptID.String()}, windowArgs...)
	args = append(args, options.Limit+1)
	var rows []examIntegrityEvidenceRow
	if err := s.GetMaster().Select(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list live Integrity Evidence: %w", err)
	}
	page := &store.ExamIntegrityEvidencePage{HasMore: len(rows) > options.Limit, Items: []model.IntegrityEvidence{}}
	for _, row := range rows[:min(len(rows), options.Limit)] {
		evidence, err := row.value()
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, *evidence)
	}
	return page, nil
}

// ListLiveNativeConditions returns one page of an attempt's native condition
// records.
func (s *SQLExamIntegrityReviewStore) ListLiveNativeConditions(
	ctx context.Context,
	options store.LiveNativeConditionListOptions,
) (*store.NativeConditionPage, error) {
	if !options.AttemptID.IsValid() || options.Limit < 1 || options.Limit > store.LiveEvidenceReadMaximum ||
		options.AfterID != "" && !options.AfterID.IsValid() || !validLiveWindow(options.Window) {
		return nil, store.NewErrInvalidInput("native_condition", "live_list_options", nil)
	}
	window, windowArgs := liveWindowSQL(nativeConditionAtSQL, options.Window)
	query := `SELECT e.canonical
		FROM native_condition_evidence e
		WHERE e.exam_attempt_id = ? AND e.id > ? AND NOT ` + liveEvidenceRetiredSQL + window + `
		ORDER BY e.id
		LIMIT ?`
	args := append([]any{options.AttemptID.String(), options.AfterID.String(), options.AttemptID.String()}, windowArgs...)
	args = append(args, options.Limit+1)
	var rows [][]byte
	if err := s.GetMaster().Select(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list live native conditions: %w", err)
	}
	page := &store.NativeConditionPage{Items: []model.NativeConditionEvidence{}, HasMore: len(rows) > options.Limit}
	for _, raw := range rows[:min(len(rows), options.Limit)] {
		value, err := nativeConditionValue(raw)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func nativeConditionValue(raw []byte) (model.NativeConditionEvidence, error) {
	var value model.NativeConditionEvidence
	if json.Unmarshal(raw, &value) != nil || value.Validate() != nil {
		return model.NativeConditionEvidence{}, invalidPersistedState("native_condition", "value", model.ErrNativeDeliveryInvalid)
	}
	return value, nil
}

type liveTimelineRow struct {
	Kind string    `db:"kind"`
	At   time.Time `db:"at"`
	ID   string    `db:"id"`
}

// ListLiveTimeline merges an attempt's flags, evidence and native condition
// records in time order, then reads the page's records in one query per kind,
// all in one transaction so the page is one consistent read.
func (s *SQLExamIntegrityReviewStore) ListLiveTimeline(
	ctx context.Context,
	options store.LiveTimelineListOptions,
) (*store.LiveTimelinePage, error) {
	if !options.AttemptID.IsValid() || options.Limit < 1 || options.Limit > store.LiveEvidenceReadMaximum ||
		!validLiveWindow(options.Window) ||
		options.After != nil && (options.After.At.IsZero() || !model.IsValidOpaqueID(options.After.ID) ||
			options.After.Kind != store.LiveTimelineFlag && options.After.Kind != store.LiveTimelineEvidence &&
				options.After.Kind != store.LiveTimelineNativeCondition) {
		return nil, store.NewErrInvalidInput("integrity_timeline", "list_options", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "list live Attempt timeline", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.LiveTimelinePage, error) {
		attempt := options.AttemptID.String()
		window, windowArgs := liveWindowSQL("entry.at", options.Window)
		query := `SELECT entry.kind, entry.at, entry.id FROM (
				SELECT 'flag' AS kind, f.created_at AS at, f.id FROM integrity_flags f WHERE f.exam_attempt_id = ?
				UNION ALL
				SELECT 'evidence', ev.observed_at, ev.id FROM integrity_evidence ev WHERE ev.exam_attempt_id = ?
				UNION ALL
				SELECT 'native_condition', ` + nativeConditionAtSQL + `, e.id FROM native_condition_evidence e
				WHERE e.exam_attempt_id = ?
			) entry
			WHERE NOT ` + liveEvidenceRetiredSQL + window
		args := append([]any{attempt, attempt, attempt, attempt}, windowArgs...)
		if after := options.After; after != nil {
			query += ` AND (entry.at, entry.kind, entry.id) > (?, ?, ?)`
			args = append(args, model.TimeUTC(after.At), string(after.Kind), after.ID)
		}
		query += ` ORDER BY entry.at, entry.kind, entry.id LIMIT ?`
		args = append(args, options.Limit+1)
		var entries []liveTimelineRow
		if err := tx.Select(ctx, &entries, query, args...); err != nil {
			return nil, fmt.Errorf("list live Attempt timeline: %w", err)
		}
		page := &store.LiveTimelinePage{HasMore: len(entries) > options.Limit, Items: []store.LiveTimelineItem{}}
		entries = entries[:min(len(entries), options.Limit)]

		ids := map[store.LiveTimelineKind][]string{}
		for _, entry := range entries {
			ids[store.LiveTimelineKind(entry.Kind)] = append(ids[store.LiveTimelineKind(entry.Kind)], entry.ID)
		}
		flags, evidence, conditions, err := liveTimelineRecords(ctx, tx, attempt, ids)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			item := store.LiveTimelineItem{Kind: store.LiveTimelineKind(entry.Kind), At: model.TimeUTC(entry.At)}
			switch item.Kind {
			case store.LiveTimelineFlag:
				item.Flag = flags[entry.ID]
			case store.LiveTimelineEvidence:
				item.Evidence = evidence[entry.ID]
			case store.LiveTimelineNativeCondition:
				item.NativeCondition = conditions[entry.ID]
			}
			if item.Flag == nil && item.Evidence == nil && item.NativeCondition == nil {
				return nil, invalidPersistedState("integrity_timeline", "entry", nil)
			}
			page.Items = append(page.Items, item)
		}
		return page, nil
	})
}

func liveTimelineRecords(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attempt string,
	ids map[store.LiveTimelineKind][]string,
) (map[string]*store.ExamIntegrityFlagSummary, map[string]*model.IntegrityEvidence, map[string]*model.NativeConditionEvidence, error) {
	flags := map[string]*store.ExamIntegrityFlagSummary{}
	evidence := map[string]*model.IntegrityEvidence{}
	conditions := map[string]*model.NativeConditionEvidence{}
	if list := ids[store.LiveTimelineFlag]; len(list) > 0 {
		var rows []integrityFlagSummaryRow
		if err := tx.Select(ctx, &rows, `SELECT `+integrityFlagSummaryColumns+`
			FROM integrity_flags f
			`+integrityFlagSummaryJoins+`
			WHERE f.exam_attempt_id = ? AND f.id = ANY(?)
			GROUP BY `+integrityFlagSummaryGroup, attempt, pq.Array(list)); err != nil {
			return nil, nil, nil, fmt.Errorf("read timeline Flags: %w", err)
		}
		for _, row := range rows {
			summary, err := row.summary()
			if err != nil {
				return nil, nil, nil, err
			}
			flags[row.ID] = &summary
		}
	}
	if list := ids[store.LiveTimelineEvidence]; len(list) > 0 {
		var rows []examIntegrityEvidenceRow
		if err := tx.Select(ctx, &rows, `SELECT `+liveEvidenceColumns+`
			FROM integrity_evidence ev WHERE ev.exam_attempt_id = ? AND ev.id = ANY(?)`, attempt, pq.Array(list)); err != nil {
			return nil, nil, nil, fmt.Errorf("read timeline Evidence: %w", err)
		}
		for _, row := range rows {
			value, err := row.value()
			if err != nil {
				return nil, nil, nil, err
			}
			evidence[row.ID] = value
		}
	}
	if list := ids[store.LiveTimelineNativeCondition]; len(list) > 0 {
		var rows []struct {
			ID        string `db:"id"`
			Canonical []byte `db:"canonical"`
		}
		if err := tx.Select(ctx, &rows, `SELECT e.id, e.canonical FROM native_condition_evidence e
			WHERE e.exam_attempt_id = ? AND e.id = ANY(?)`, attempt, pq.Array(list)); err != nil {
			return nil, nil, nil, fmt.Errorf("read timeline native conditions: %w", err)
		}
		for _, row := range rows {
			value, err := nativeConditionValue(row.Canonical)
			if err != nil {
				return nil, nil, nil, err
			}
			conditions[row.ID] = &value
		}
	}
	return flags, evidence, conditions, nil
}
