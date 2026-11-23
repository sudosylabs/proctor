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

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// sittingCandidatePopulationSQL is used beneath a sitting aliased s. The
// caller supplies the SQL expression for its read time and the excluded user.
func sittingCandidatePopulationSQL(at string) string {
	return `SELECT members.user_id
	FROM (
		SELECT a.candidate_user_id AS user_id
		FROM exam_attempts a
		WHERE a.exam_sitting_id = s.id
		UNION
		SELECT member.user_id
		FROM (VALUES
			(CASE WHEN s.state IN ('scheduled', 'canceled') THEN s.scheduled_start_at END),
			(CASE WHEN s.state NOT IN ('scheduled', 'canceled') THEN s.opened_at END),
			(CASE WHEN s.state IN ('open', 'paused') THEN ` + at + ` END)
		) instant(at)
		JOIN class_members member ON member.class_id = s.class_id
			AND member.start_at <= instant.at
			AND (member.end_at IS NULL OR member.end_at > instant.at)
			AND (member.archived_at IS NULL OR member.archived_at > instant.at)
	) members
	WHERE members.user_id <> ?`
}

func (s SQLExamSittingStore) CountCandidates(
	ctx context.Context,
	sittingIDs []model.ExamSittingID,
	viewerUserID model.UserID,
) (map[model.ExamSittingID]int, error) {
	if len(sittingIDs) > 200 || !viewerUserID.IsValid() {
		return nil, store.NewErrInvalidInput("exam_sitting", "candidate_count", nil)
	}
	counts := make(map[model.ExamSittingID]int, len(sittingIDs))
	if len(sittingIDs) == 0 {
		return counts, nil
	}
	ids := make([]string, len(sittingIDs))
	for index, id := range sittingIDs {
		if !id.IsValid() {
			return nil, store.NewErrInvalidInput("exam_sitting", "candidate_count", nil)
		}
		ids[index] = id.String()
	}

	var rows []struct {
		SittingID string `db:"exam_sitting_id"`
		Count     int    `db:"candidate_count"`
	}
	if err := s.GetMaster().Select(ctx, &rows, `SELECT s.id AS exam_sitting_id, (
			SELECT COUNT(*)
			FROM (`+sittingCandidatePopulationSQL("statement_timestamp()")+` LIMIT ?) population
		) AS candidate_count
		FROM exam_sittings s
		WHERE s.id = ANY(?)`, viewerUserID.String(), store.MaximumListCount, pq.Array(ids)); err != nil {
		return nil, fmt.Errorf("count Exam Sitting candidates: %w", err)
	}

	for _, row := range rows {
		id, err := model.ParseExamSittingID(row.SittingID)
		if err != nil || row.Count < 0 || row.Count > store.MaximumListCount {
			return nil, invalidPersistedState("exam_sitting", "candidate_count", errors.New("invalid candidate count"))
		}
		counts[id] = row.Count
	}

	return counts, nil
}

func (s SQLExamSittingStore) ListCandidateBoardTargets(
	ctx context.Context,
	userID model.UserID,
	afterSittingID model.ExamSittingID,
	limit int,
) ([]store.ExamSittingInvalidationTarget, error) {
	if !userID.IsValid() || (!afterSittingID.IsZero() && !afterSittingID.IsValid()) || limit < 1 || limit > 201 {
		return nil, store.NewErrInvalidInput("exam_sitting", "candidate_board_targets", nil)
	}

	var rows []struct {
		ExamID    string `db:"exam_id"`
		SittingID string `db:"id"`
	}
	if err := s.GetMaster().Select(ctx, &rows, `SELECT s.exam_id, s.id
		FROM exam_sittings s
		WHERE s.state IN ('scheduled', 'open', 'paused', 'closing')
			AND s.id > ?
			AND (
				EXISTS (
					SELECT 1
					FROM exam_attempts a
					WHERE a.exam_sitting_id = s.id
						AND a.candidate_user_id = ?
				)
				OR EXISTS (
					SELECT 1
					FROM class_members member
					WHERE member.class_id = s.class_id
						AND member.user_id = ?
						AND member.start_at <= statement_timestamp()
						AND (member.end_at IS NULL OR member.end_at > statement_timestamp())
						AND member.archived_at IS NULL
				)
			)
		ORDER BY s.id
		LIMIT ?`, afterSittingID.String(), userID.String(), userID.String(), limit); err != nil {
		return nil, fmt.Errorf("list candidate Exam Sitting board targets: %w", err)
	}

	targets := make([]store.ExamSittingInvalidationTarget, 0, len(rows))
	for _, row := range rows {
		examID, examErr := model.ParseExamID(row.ExamID)
		sittingID, sittingErr := model.ParseExamSittingID(row.SittingID)
		if examErr != nil || sittingErr != nil {
			return nil, invalidPersistedState("exam_sitting", "board_target", errors.Join(examErr, sittingErr))
		}
		targets = append(targets, store.ExamSittingInvalidationTarget{ExamID: examID, SittingID: sittingID})
	}
	return targets, nil
}

func (s SQLExamSittingStore) ListHistory(
	ctx context.Context,
	sittingID model.ExamSittingID,
	limit int,
) ([]store.ExamSittingHistoryEntry, error) {
	if !sittingID.IsValid() || limit < 1 || limit > store.ExamSittingHistoryMaximum {
		return nil, store.NewErrInvalidInput("exam_sitting", "history", nil)
	}

	var rows []struct {
		ActionCode  string    `db:"action_code"`
		CreatedAt   time.Time `db:"created_at"`
		ActorUserID string    `db:"actor_user_id"`
		Reason      string    `db:"private_reason"`
	}
	if err := s.GetMaster().Select(ctx, &rows, `SELECT action_code, created_at, actor_user_id, private_reason
		FROM exam_sitting_private_actions
		WHERE exam_sitting_id = ?
		ORDER BY sitting_revision DESC
		LIMIT ?`, sittingID.String(), limit); err != nil {
		return nil, fmt.Errorf("list Exam Sitting history: %w", err)
	}

	entries := make([]store.ExamSittingHistoryEntry, 0, len(rows))
	for _, row := range rows {
		actorID, err := model.ParseUserID(row.ActorUserID)
		kind := store.ExamSittingHistoryKind(strings.TrimPrefix(row.ActionCode, "manager_"))
		switch kind {
		case store.ExamSittingHistoryCanceled, store.ExamSittingHistoryPaused, store.ExamSittingHistoryResumed,
			store.ExamSittingHistoryExtended, store.ExamSittingHistoryClosed:
		default:
			err = errors.Join(err, errors.New("unknown action code"))
		}
		if err != nil || row.Reason == "" {
			return nil, invalidPersistedState("exam_sitting_private_action", "history", err)
		}
		entries = append(entries, store.ExamSittingHistoryEntry{
			Kind: kind, At: model.TimeUTC(row.CreatedAt), ActorUserID: actorID, Reason: row.Reason,
		})
	}

	return entries, nil
}

// ListCorrections counts, for each correction, the Attempts admitted before it
// that had not submitted when it took effect; those must acknowledge it.
func (s SQLExamSittingStore) ListCorrections(
	ctx context.Context,
	sittingID model.ExamSittingID,
) ([]store.ExamSittingCorrectionSummary, error) {
	if !sittingID.IsValid() {
		return nil, store.NewErrInvalidInput("exam_sitting", "corrections", nil)
	}

	var rows []struct {
		RevisionID    string    `db:"correction_revision_id"`
		Number        int64     `db:"number"`
		EffectiveAt   time.Time `db:"effective_at"`
		Required      bool      `db:"acknowledgement_required"`
		Acknowledged  int       `db:"acknowledged_count"`
		RequiredCount int       `db:"required_count"`
	}
	if err := s.GetMaster().Select(ctx, &rows, `SELECT live.correction_revision_id, correction.number, live.effective_at,
			COALESCE(correction.candidate_correction_acknowledgement_required, false) AS acknowledgement_required,
			(
				SELECT COUNT(*)
				FROM exam_attempt_correction_acknowledgements acknowledgement
				WHERE acknowledgement.exam_sitting_id = live.exam_sitting_id
					AND acknowledgement.correction_revision_id = live.correction_revision_id
			) AS acknowledged_count,
			CASE WHEN COALESCE(correction.candidate_correction_acknowledgement_required, false) THEN (
				SELECT COUNT(*)
				FROM exam_attempts a
				JOIN exam_revisions admission ON admission.id = a.admission_revision_id
					AND admission.exam_id = a.exam_id
				LEFT JOIN exam_submissions submission ON submission.exam_attempt_id = a.id
					AND submission.sealed = true
				WHERE a.exam_sitting_id = live.exam_sitting_id
					AND admission.number < correction.number
					AND (submission.id IS NULL OR submission.submitted_at > live.effective_at)
			) ELSE 0 END AS required_count
		FROM exam_sitting_live_corrections live
		JOIN exam_revisions correction ON correction.id = live.correction_revision_id
			AND correction.exam_id = live.exam_id
		WHERE live.exam_sitting_id = ?
		ORDER BY correction.number
		LIMIT ?`, sittingID.String(), model.ExamSittingMaximumLiveCorrections+1); err != nil {
		return nil, fmt.Errorf("list Exam Sitting corrections: %w", err)
	}
	if len(rows) > model.ExamSittingMaximumLiveCorrections {
		return nil, invalidPersistedState("exam_sitting", "corrections", errors.New("correction limit exceeded"))
	}

	corrections := make([]store.ExamSittingCorrectionSummary, 0, len(rows))
	for _, row := range rows {
		revisionID, err := model.ParseExamRevisionID(row.RevisionID)
		if err != nil || row.Number < 1 || row.Acknowledged < 0 || row.RequiredCount < 0 {
			return nil, invalidPersistedState("exam_sitting", "corrections", errors.Join(err, errors.New("invalid correction summary")))
		}
		corrections = append(corrections, store.ExamSittingCorrectionSummary{
			RevisionID: revisionID, Number: row.Number, EffectiveAt: model.TimeUTC(row.EffectiveAt),
			AcknowledgementRequired: row.Required, AcknowledgedCount: row.Acknowledged, RequiredCount: row.RequiredCount,
		})
	}

	return corrections, nil
}

func (s *SQLExamAttemptStore) ListSittingCandidateDetails(
	ctx context.Context,
	options store.SittingCandidateDetailOptions,
) ([]store.SittingCandidateDetail, error) {
	if !options.ExamID.IsValid() || !options.SittingID.IsValid() || len(options.CandidateUserIDs) > 200 {
		return nil, store.NewErrInvalidInput("sitting_candidate_detail", "options", nil)
	}
	if len(options.CandidateUserIDs) == 0 {
		return []store.SittingCandidateDetail{}, nil
	}
	ids := make([]string, len(options.CandidateUserIDs))
	for index, id := range options.CandidateUserIDs {
		if !id.IsValid() {
			return nil, store.NewErrInvalidInput("sitting_candidate_detail", "candidate_user_id", nil)
		}
		ids[index] = id.String()
	}

	var rows []sittingCandidateDetailRow
	if err := s.GetMaster().Select(ctx, &rows, `SELECT candidate.id AS candidate_user_id,
			registration.desktop_release, registration.desktop_build_id, registration.platform,
			registration.architecture, registration.realtime_protocol,
			COALESCE(device.display_name, '') AS device_name,
			COALESCE(changed.count, 0) AS changed_file_count,
			COALESCE((
				SELECT SUM(evaluation.accepted_sequence)
				FROM exam_attempt_focus_loss_evaluations evaluation
				WHERE evaluation.exam_attempt_id = a.id
			), 0) AS focus_loss_count,
			acknowledgement.acknowledged_at
		FROM users candidate
		JOIN exam_sittings s ON s.id = ?
			AND s.exam_id = ?
		LEFT JOIN exam_attempts a ON a.exam_sitting_id = s.id
			AND a.candidate_user_id = candidate.id
		LEFT JOIN LATERAL (
			SELECT r.desktop_release, r.desktop_build_id, r.platform, r.architecture, r.realtime_protocol
			FROM desktop_registrations r
			WHERE r.user_id = candidate.id
				AND r.revoked_at IS NULL
			ORDER BY r.last_used_at DESC, r.id DESC
			LIMIT 1
		) registration ON TRUE
		LEFT JOIN LATERAL (
			SELECT r.display_name
			FROM exam_attempt_participations p
			JOIN sessions session ON session.id = p.session_id
			JOIN desktop_registrations r ON r.id = session.desktop_registration_id
			WHERE p.exam_attempt_id = a.id
			ORDER BY p.generation DESC
			LIMIT 1
		) device ON TRUE
		LEFT JOIN LATERAL (
			SELECT (
				SELECT COUNT(*)
				FROM exam_attempt_workspace_entries entry
				WHERE entry.workspace_id = w.id
					AND entry.kind = 'file'
					AND (entry.source_starter_entry_id IS NULL OR entry.last_author_kind IS NOT NULL)
			) + (
				SELECT COUNT(*)
				FROM exam_revision_starter_workspace_entries starter
				WHERE starter.exam_revision_id = w.admission_revision_id
					AND starter.kind = 'file'
					AND NOT EXISTS (
						SELECT 1
						FROM exam_attempt_workspace_entries kept
						WHERE kept.workspace_id = w.id
							AND kept.source_starter_entry_id = starter.entry_id
					)
			) AS count
			FROM exam_attempt_workspaces w
			WHERE w.exam_attempt_id = a.id
		) changed ON TRUE
		LEFT JOIN exam_attempt_correction_acknowledgements acknowledgement ON acknowledgement.exam_attempt_id = a.id
			AND acknowledgement.correction_revision_id = s.exam_revision_id
		WHERE candidate.id = ANY(?)
		ORDER BY candidate.id`, options.SittingID.String(), options.ExamID.String(), pq.Array(ids)); err != nil {
		return nil, fmt.Errorf("list Sitting candidate details: %w", err)
	}

	details := make([]store.SittingCandidateDetail, 0, len(rows))
	for _, row := range rows {
		detail, err := row.detail()
		if err != nil {
			return nil, err
		}
		details = append(details, detail)
	}

	return details, nil
}

type sittingCandidateDetailRow struct {
	CandidateUserID  string         `db:"candidate_user_id"`
	DesktopRelease   sql.NullString `db:"desktop_release"`
	DesktopBuildID   sql.NullString `db:"desktop_build_id"`
	Platform         sql.NullString `db:"platform"`
	Architecture     sql.NullString `db:"architecture"`
	RealtimeProtocol sql.NullInt64  `db:"realtime_protocol"`
	DeviceName       string         `db:"device_name"`
	ChangedFileCount int64          `db:"changed_file_count"`
	FocusLossCount   int64          `db:"focus_loss_count"`
	AcknowledgedAt   sql.NullTime   `db:"acknowledged_at"`
}

func (row sittingCandidateDetailRow) detail() (store.SittingCandidateDetail, error) {
	candidateID, err := model.ParseUserID(row.CandidateUserID)
	if err != nil || row.ChangedFileCount < 0 || row.FocusLossCount < 0 {
		return store.SittingCandidateDetail{}, invalidPersistedState("sitting_candidate_detail", "candidate",
			errors.Join(err, errors.New("invalid candidate detail")))
	}

	detail := store.SittingCandidateDetail{
		CandidateUserID:                 candidateID,
		DeviceName:                      row.DeviceName,
		ChangedFileCount:                row.ChangedFileCount,
		FocusLossCount:                  row.FocusLossCount,
		CurrentCorrectionAcknowledgedAt: OptionalTimeFromNullTime(row.AcknowledgedAt),
	}
	if row.DesktopRelease.Valid {
		detail.DesktopRegistration = &model.DesktopBuildTuple{
			DesktopRelease:   row.DesktopRelease.String,
			DesktopBuildID:   row.DesktopBuildID.String,
			Platform:         model.DesktopPlatform(row.Platform.String),
			Architecture:     model.DesktopArchitecture(row.Architecture.String),
			RealtimeProtocol: int(row.RealtimeProtocol.Int64),
		}
	}

	return detail, nil
}

func (s *SQLExamAttemptStore) ListManagerEnds(
	ctx context.Context,
	sittingID model.ExamSittingID,
	attemptIDs []model.ExamAttemptID,
) ([]store.ExamAttemptManagerEnd, error) {
	if !sittingID.IsValid() || len(attemptIDs) > 201 {
		return nil, store.NewErrInvalidInput("exam_attempt", "manager_ends", nil)
	}
	if len(attemptIDs) == 0 {
		return []store.ExamAttemptManagerEnd{}, nil
	}
	ids := make([]string, len(attemptIDs))
	for index, id := range attemptIDs {
		if !id.IsValid() {
			return nil, store.NewErrInvalidInput("exam_attempt", "manager_ends", nil)
		}
		ids[index] = id.String()
	}

	var rows []struct {
		AttemptID   string    `db:"exam_attempt_id"`
		ActorUserID string    `db:"actor_user_id"`
		Reason      string    `db:"private_reason"`
		EndedAt     time.Time `db:"ended_at"`
	}
	if err := s.GetMaster().Select(ctx, &rows, `SELECT action.exam_attempt_id, action.actor_user_id,
			action.private_reason, action.ended_at
		FROM exam_attempt_manager_end_actions action
		JOIN exam_attempts a ON a.id = action.exam_attempt_id
		WHERE a.exam_sitting_id = ?
			AND action.exam_attempt_id = ANY(?)
		ORDER BY action.exam_attempt_id`, sittingID.String(), pq.Array(ids)); err != nil {
		return nil, fmt.Errorf("list Exam Attempt manager ends: %w", err)
	}

	ends := make([]store.ExamAttemptManagerEnd, 0, len(rows))
	for _, row := range rows {
		attemptID, attemptErr := model.ParseExamAttemptID(row.AttemptID)
		actorID, actorErr := model.ParseUserID(row.ActorUserID)
		if attemptErr != nil || actorErr != nil || row.Reason == "" {
			return nil, invalidPersistedState("exam_attempt_manager_end_action", "row", errors.Join(attemptErr, actorErr))
		}
		ends = append(ends, store.ExamAttemptManagerEnd{
			AttemptID: attemptID, ActorUserID: actorID, Reason: row.Reason, EndedAt: model.TimeUTC(row.EndedAt),
		})
	}

	return ends, nil
}
