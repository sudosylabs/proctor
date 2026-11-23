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
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// candidateStatusPresenceSQL derives the board's presence state from a
// candidate_status_row, the same way sittingCandidateStatusRow.item does, so
// filters and counts agree with the rows they describe. item checks the two
// agree for every row it returns.
const candidateStatusPresenceSQL = `CASE
		WHEN status.attempt_id IS NULL THEN 'not_started'
		WHEN status.attempt_state = 'submitted' THEN 'submitted'
		WHEN status.attempt_state = 'suspended' THEN 'suspended'
		WHEN status.attempt_state = 'ready' THEN 'ready'
		WHEN status.lease_expires_at <= status.server_time THEN 'lease_expired'
		WHEN status.open_connection_count = 0 THEN 'reconnecting'
		ELSE 'connected'
	END`

// candidateStatusFailingCheckSQL is true while the live participation has an
// enforced native rule the server evaluates as not met. An unknown rule is not
// failing: it is a gap in coverage, which integrity attention already counts.
const candidateStatusFailingCheckSQL = `COALESCE(status.native_live AND EXISTS (
		SELECT 1
		FROM jsonb_each(convert_from(status.native_interpretation, 'UTF8')::jsonb -> 'rules') standing
		WHERE standing.value ->> 'mode' = 'enforce'
			AND standing.value -> 'tracker' ->> 'status' = 'not_met'
	), false)`

// candidateNeedsAttentionSQL is the one definition of a candidate who needs a
// manager: suspended, failing a check, or with unresolved integrity attention.
// It reads a candidate_status row and is counted only for open sittings.
const candidateNeedsAttentionSQL = `(status.suspension_id IS NOT NULL
		OR status.failing_check
		OR status.integrity_attention_count > 0)`

// candidateStatusCTE returns the WITH clause that defines candidate_status:
// one row per candidate of every sitting statusSittings selects, excluding
// excludeUserID, read at serverTime. statusSittings must select id, class_id,
// state, scheduled_start_at and opened_at from exam_sittings.
func candidateStatusCTE(
	serverTime time.Time,
	excludeUserID model.UserID,
	statusSittings string,
	statusSittingArgs ...any,
) (string, []any) {
	query := `WITH status_sittings AS (` + statusSittings + `),
		population AS (
			SELECT s.id AS exam_sitting_id, a.candidate_user_id
			FROM status_sittings s
			JOIN exam_attempts a ON a.exam_sitting_id = s.id
			UNION
			SELECT s.id, member.user_id
			FROM status_sittings s
			-- A scheduled sitting's candidates are the class at its start. An
			-- opened sitting's are the class when it opened and, while it is
			-- open or paused, the class now as well.
			JOIN LATERAL (
				VALUES
					(CASE WHEN s.state IN ('scheduled', 'canceled') THEN s.scheduled_start_at END),
					(CASE WHEN s.state NOT IN ('scheduled', 'canceled') THEN s.opened_at END),
					(CASE WHEN s.state IN ('open', 'paused') THEN ?::timestamptz END)
			) instant(at) ON instant.at IS NOT NULL
			JOIN class_members member ON member.class_id = s.class_id
				AND member.start_at <= instant.at
				AND (member.end_at IS NULL OR member.end_at > instant.at)
				AND (member.archived_at IS NULL OR member.archived_at > instant.at)
		),
		finalized_review AS (
			SELECT review.id, review.exam_attempt_id
			FROM submission_reviews review
			WHERE review.state = 'finalized'
		),
		candidate_status_row AS (
		SELECT population.exam_sitting_id, ?::timestamptz AS server_time,
			candidate.id AS candidate_user_id, candidate.username, candidate.display_name,
			EXISTS (
				SELECT 1
				FROM class_members current_member
				WHERE current_member.class_id = s.class_id
					AND current_member.user_id = candidate.id
					AND current_member.start_at <= ?
					AND (current_member.end_at IS NULL OR current_member.end_at > ?)
					AND (current_member.archived_at IS NULL OR current_member.archived_at > ?)
			) AS current_membership,
			a.id AS attempt_id, a.state AS attempt_state, a.revision AS attempt_revision,
			a.created_at AS attempt_created_at, a.updated_at AS attempt_updated_at,
			submission.id AS submission_id, submission.submitted_at,
			submission.provenance AS submission_provenance, participation.id AS participation_id,
			participation.state AS participation_state,
			participation.updated_at AS participation_updated_at, participation.lease_expires_at,
			(
				SELECT COUNT(*)
				FROM exam_attempt_participations active_participation
				WHERE active_participation.exam_attempt_id = a.id
					AND active_participation.state = 'active'
			) AS active_participation_count,
			(
				SELECT COUNT(*)
				FROM exam_attempt_connections open_connection
				WHERE open_connection.exam_attempt_id = a.id
					AND open_connection.state = 'open'
			) AS open_connection_count,
			suspension.id AS suspension_id, suspension.candidate_reason AS suspension_reason,
			(
				SELECT COUNT(*)
				FROM exam_attempt_suspensions active_suspension
				WHERE active_suspension.exam_attempt_id = a.id
					AND active_suspension.state = 'active'
			) AS active_suspension_count,
			COALESCE(
				(
					SELECT COUNT(*)
					FROM integrity_flags flag
					WHERE flag.exam_attempt_id = a.id
						AND NOT EXISTS (
							SELECT 1
							FROM integrity_review_decisions decision
							JOIN submission_reviews decision_review ON decision_review.id = decision.submission_review_id
							WHERE decision.integrity_flag_id = flag.id
								AND decision_review.exam_attempt_id = a.id
						)
				),
					0
			) + CASE
				WHEN (
						a.state <> 'submitted'
						AND (
							EXISTS (
								SELECT 1
								FROM exam_attempt_focus_loss_evaluations evaluation
								WHERE evaluation.exam_attempt_id = a.id
									AND evaluation.unresolved_missing_count > 0
							)
							OR EXISTS (
								SELECT 1
								FROM browser_activity_sources source
								WHERE source.exam_attempt_id = a.id
									AND (
										source.state = 'gapped'
										OR (
											source.state = 'current'
											AND source.highest_contiguous < source.highest_seen
										)
									)
							)
						)
					)
					OR (
						a.state = 'submitted'
						AND submission.integrity_state = 'gapped'
						AND NOT EXISTS (
							SELECT 1
							FROM finalized_review review
							WHERE review.exam_attempt_id = a.id
						)
					)
					THEN 1
				ELSE 0
			END + COALESCE(
				(
					SELECT COUNT(*)
					FROM integrity_discrepancies discrepancy
					WHERE discrepancy.exam_attempt_id = a.id
						AND NOT EXISTS (
							SELECT 1
							FROM submission_review_inventory_discrepancies inventory
							JOIN finalized_review review ON review.id = inventory.submission_review_id
							WHERE inventory.integrity_discrepancy_id = discrepancy.id
						)
				),
					0
			) AS integrity_attention_count,
			CASE
				WHEN submission.integrity_retired_at IS NULL
					THEN COALESCE(delivery_budget.native_condition_records, 0)
				ELSE 0
			END AS native_condition_records,
			COALESCE(participation.state = 'active'
				AND participation.lease_expires_at > statement_timestamp()
				AND delivery_budget.security_retired_at IS NULL
				AND native_owner.participation_id IS NOT NULL, false) AS native_live,
			CASE
				WHEN participation.state = 'active'
					AND participation.lease_expires_at > statement_timestamp()
					AND delivery_budget.security_retired_at IS NULL
					THEN native_owner.native_interpretation_canonical
				ELSE NULL
			END AS native_interpretation,
			COALESCE(clips.requested, 0) AS clips_requested,
			COALESCE(clips.recording, 0) AS clips_recording,
			COALESCE(clips.uploading, 0) AS clips_uploading,
			COALESCE(clips.ready, 0) AS clips_ready,
			COALESCE(clips.failed, 0) AS clips_failed,
			environment.state AS environment_state, environment.desired AS environment_desired,
			environment.incarnation AS environment_incarnation, environment.waiting_since AS environment_waiting_since
		FROM population
		JOIN status_sittings s ON s.id = population.exam_sitting_id
		JOIN users candidate ON candidate.id = population.candidate_user_id
		LEFT JOIN exam_attempts a ON a.exam_sitting_id = population.exam_sitting_id
			AND a.candidate_user_id = candidate.id
		LEFT JOIN exam_submissions submission ON submission.exam_attempt_id = a.id
			AND submission.sealed = true
		LEFT JOIN LATERAL (
			SELECT p.id, p.state, p.updated_at, p.lease_expires_at
			FROM exam_attempt_participations p
			WHERE p.exam_attempt_id = a.id
			ORDER BY p.generation DESC
			LIMIT 1
		) participation ON TRUE
		LEFT JOIN exam_attempt_security_owners native_owner ON native_owner.participation_id = participation.id
		LEFT JOIN exam_attempt_delivery_budgets delivery_budget ON delivery_budget.exam_attempt_id = a.id
		LEFT JOIN LATERAL (
			SELECT count(*) FILTER (WHERE c.state = 'requested') AS requested,
				count(*) FILTER (WHERE c.state = 'recording') AS recording,
				count(*) FILTER (WHERE c.state = 'uploading') AS uploading,
				count(*) FILTER (WHERE c.state = 'ready') AS ready,
				count(*) FILTER (WHERE c.state = 'failed') AS failed
			FROM exam_attempt_screen_clips c
			WHERE c.exam_attempt_id = a.id
		) clips ON TRUE
		LEFT JOIN exam_attempt_suspensions suspension ON suspension.exam_attempt_id = a.id
			AND suspension.state = 'active'
		LEFT JOIN exam_attempt_environments environment ON environment.exam_attempt_id = a.id
		WHERE candidate.id <> ?
		),
		candidate_status_presence AS (
			SELECT status.*, ` + candidateStatusPresenceSQL + ` AS presence_state
			FROM candidate_status_row status
		),
		candidate_status AS (
			SELECT status.*, ` + candidateStatusFailingCheckSQL + ` AS failing_check
			FROM candidate_status_presence status
		)`
	args := append([]any{}, statusSittingArgs...)
	args = append(args, serverTime, serverTime, serverTime, serverTime, serverTime, excludeUserID.String())
	return query, args
}

// candidateStatusFilterSQL narrows candidate_status rows, aliased status, to
// the options' filters. An empty filter keeps every row.
func candidateStatusFilterSQL(filter store.SittingCandidateStatusFilter) (string, []any) {
	var clauses []string
	var args []any
	if len(filter.PresenceStates) > 0 {
		states := make([]string, len(filter.PresenceStates))
		for i, state := range filter.PresenceStates {
			states[i] = string(state)
		}
		clauses = append(clauses, `status.presence_state = ANY(?)`)
		args = append(args, pq.Array(states))
	}
	if filter.FailingChecks {
		clauses = append(clauses, `status.failing_check`)
	}
	if filter.Attention {
		clauses = append(clauses, `status.integrity_attention_count > 0`)
	}
	if term := strings.TrimSpace(filter.Query); term != "" {
		pattern := directorySearchPattern(term)
		clauses = append(clauses, `(status.display_name ILIKE ? ESCAPE '!' OR status.username ILIKE ? ESCAPE '!')`)
		args = append(args, pattern, pattern)
	}
	if len(clauses) == 0 {
		return "TRUE", nil
	}
	return strings.Join(clauses, " AND "), args
}

type candidateStatusCountRow struct {
	ExamSittingID  string `db:"exam_sitting_id"`
	PresenceState  string `db:"presence_state"`
	Candidates     int64  `db:"candidates"`
	FailingChecks  int64  `db:"failing_checks"`
	Attention      int64  `db:"attention"`
	NeedsAttention int64  `db:"needs_attention"`
}

// countCandidateStatuses counts every candidate of the selected sittings by
// presence state, in one grouped query, keyed by sitting. Filters do not
// apply: the counts describe the whole board.
func countCandidateStatuses(
	ctx context.Context,
	reader examCatalogReader,
	serverTime time.Time,
	excludeUserID model.UserID,
	statusSittings string,
	statusSittingArgs ...any,
) (map[model.ExamSittingID]*store.SittingCandidateStatusCounts, error) {
	cte, args := candidateStatusCTE(serverTime, excludeUserID, statusSittings, statusSittingArgs...)
	var rows []candidateStatusCountRow
	if err := reader.Select(ctx, &rows, cte+`
		SELECT status.exam_sitting_id, status.presence_state, COUNT(*) AS candidates,
			COUNT(*) FILTER (WHERE status.failing_check) AS failing_checks,
			COUNT(*) FILTER (WHERE status.integrity_attention_count > 0) AS attention,
			COUNT(*) FILTER (WHERE `+candidateNeedsAttentionSQL+`) AS needs_attention
		FROM candidate_status status
		GROUP BY status.exam_sitting_id, status.presence_state`, args...); err != nil {
		return nil, fmt.Errorf("count Sitting candidate statuses: %w", err)
	}

	counts := map[model.ExamSittingID]*store.SittingCandidateStatusCounts{}
	for _, row := range rows {
		sittingID, err := model.ParseExamSittingID(row.ExamSittingID)
		state := store.SittingCandidatePresenceState(row.PresenceState)
		if err != nil || !state.IsValid() || row.Candidates < 1 ||
			row.FailingChecks > row.Candidates || row.Attention > row.Candidates || row.NeedsAttention > row.Candidates {
			return nil, invalidPersistedState("sitting_candidate_status", "counts", err)
		}
		value := counts[sittingID]
		if value == nil {
			value = &store.SittingCandidateStatusCounts{ByPresenceState: map[store.SittingCandidatePresenceState]int64{}}
			counts[sittingID] = value
		}
		value.Total += row.Candidates
		value.ByPresenceState[state] += row.Candidates
		value.WithFailedChecks += row.FailingChecks
		value.WithAttention += row.Attention
		value.NeedsAttention += row.NeedsAttention
	}
	return counts, nil
}
