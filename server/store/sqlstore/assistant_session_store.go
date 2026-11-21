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
	"math"
	"strconv"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type assistantSessionRow struct {
	AccessEnded          bool         `db:"access_ended"`
	Starting             bool         `db:"starting"`
	CandidateUserID      model.UserID `db:"candidate_user_id"`
	Tag                  string       `db:"tag"`
	CallID               string       `db:"call_id"`
	ConversationID       string       `db:"conversation_id"`
	ReservedMilliseconds int64        `db:"reserved_milliseconds"`
	ReadOffset           uint64       `db:"read_offset"`
	State                string       `db:"state"`
	StartedAt            time.Time    `db:"started_at"`
	EndedAt              *time.Time   `db:"ended_at"`
}

const assistantSessionColumns = `tag,call_id,conversation_id,reserved_milliseconds,read_offset,state,started_at,ended_at`

func (s *SQLAssistantTranscriptStore) ReserveSession(ctx context.Context,
	input *store.AssistantSessionReservation) (*store.AssistantSessionSlot, error) {
	if input == nil || !input.CallID.IsValid() || input.TimeoutSeconds < 1 || input.TimeoutSeconds > 600 {
		return nil, store.NewErrInvalidInput("assistant_session", "reservation", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "reserve assistant session", func(ctx context.Context,
		tx *sqlxTxWrapper) (*store.AssistantSessionSlot, error) {
		run, now, err := lockAssistantRun(ctx, tx, input.Access, true)
		if err != nil {
			return nil, err
		}
		id := input.Access.Editor.AttemptID
		recipe, err := readAssistantRecipe(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !recipe.Execution || (recipe.Policy.Level != model.AssistantLevelPair && recipe.Policy.Level != model.AssistantLevelAgent) {
			return nil, store.NewErrConflict("assistant_session", "assistant_tool_unavailable", nil)
		}
		request, err := readAssistantExecutionRequest(ctx, tx, id, run, input.CallID, "start_session", recipe.Policy.Approvals)
		if err != nil {
			return nil, err
		}
		inventory, err := lockAssistantInventory(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		var count int
		if err = tx.Get(ctx, &count, `SELECT count(*) FROM exam_attempt_assistant_sessions WHERE exam_attempt_id=? AND state='running'`,
			id.String()); err != nil {
			return nil, err
		}
		if count >= model.MaxAssistantSessions {
			return nil, store.NewErrConflict("assistant_session", "assistant_session_limit", nil)
		}
		budgets := recipe.Policy.Budgets
		remaining := (budgets.CommandSeconds*1000 - inventory.CommandMilliseconds) / 1000
		if inventory.Commands >= int64(budgets.Commands) || remaining < 1 {
			return nil, store.NewErrConflict("assistant_session", "assistant_budget_exhausted", nil)
		}
		timeout := min(input.TimeoutSeconds, budgets.CommandTimeoutSeconds, remaining)
		sequence := run.CommandSequence + 1
		tag := "assistant:" + strconv.FormatInt(sequence, 10)
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET commands=commands+1,sessions=sessions+1,
			command_milliseconds=command_milliseconds+? WHERE id=?`, timeout*1000, id.String()); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET command_sequence=? WHERE id=?`, sequence, id.String()); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO exam_attempt_assistant_sessions
			(exam_attempt_id,tag,call_id,conversation_id,connection_id,reserved_milliseconds,state,started_at)
			VALUES (?,?,?,?,?,?,'running',?)`,
			id.String(), tag, input.CallID.String(), run.ConversationID, input.Access.Editor.ConnectionID.String(), timeout*1000, now); err != nil {
			return nil, translateError("assistant_session", tag, err)
		}
		reservation := *request
		reservation.Phase, reservation.Approval, reservation.ProcessTag = "reserved", "", tag
		reservation.Arguments = json.RawMessage(`{}`)
		if _, err = appendAssistantRecord(ctx, tx, id, model.AssistantRecord{Call: &reservation}, false); err != nil {
			return nil, err
		}
		return &store.AssistantSessionSlot{Tag: tag, StartedAt: now, TimeoutSeconds: timeout}, nil
	})
}

func lockAssistantSession(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID, tag string) (assistantSessionRow, error) {
	var row assistantSessionRow
	err := tx.Get(ctx, &row, `SELECT `+assistantSessionColumns+` FROM exam_attempt_assistant_sessions
		WHERE exam_attempt_id=? AND tag=? FOR UPDATE`, id.String(), tag)
	return row, translateError("assistant_session", tag, err)
}

func (s *SQLAssistantTranscriptStore) AdvanceSessionRead(ctx context.Context, access store.AssistantSessionAccess, tag string,
	offset uint64, wrote bool) error {
	if !model.ValidAssistantProcessTag(tag) || offset > math.MaxInt64 {
		return store.NewErrInvalidInput("assistant_session", "offset", nil)
	}
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "advance assistant session read", func(ctx context.Context, tx *sqlxTxWrapper) (bool, error) {
		if _, _, err := lockAssistantRun(ctx, tx, access, true); err != nil {
			return false, err
		}
		id := access.Editor.AttemptID
		if _, err := lockAssistantInventory(ctx, tx, id); err != nil {
			return false, err
		}
		row, err := lockAssistantSession(ctx, tx, id, tag)
		if err != nil {
			return false, err
		}
		if row.State != "running" && !wrote {
			return false, store.NewErrNotFound("assistant_session", tag)
		}
		if offset < row.ReadOffset {
			return false, store.NewErrConflict("assistant_session", "assistant_session_read_offset", nil)
		}
		if row.State == "running" {
			if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_sessions SET read_offset=? WHERE exam_attempt_id=? AND tag=?`,
				offset, id.String(), tag); err != nil {
				return false, err
			}
		}
		if wrote {
			_, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET session_inputs=session_inputs+1 WHERE id=?`, id.String())
		}
		return err == nil, err
	})
	return err
}

func (s *SQLAssistantTranscriptStore) EndSession(ctx context.Context, input *store.AssistantSessionEnd) (*store.AssistantSessionEndResult, error) {
	if input == nil || !model.ValidAssistantProcessTag(input.Tag) ||
		!model.ValidAssistantSessionOutcome(input.Outcome) || input.Outcome == "sealed" || input.DurationMilliseconds < 0 {
		return nil, store.NewErrInvalidInput("assistant_session", "end", nil)
	}
	var authority store.ExamAttemptWorkspaceWriteAccess = input.Editor
	if input.EnvironmentOwner != nil {
		if input.Editor != (store.ExamAttemptWorkspaceEditorAccess{}) {
			return nil, store.NewErrInvalidInput("assistant_session", "end_authority", nil)
		}
		authority = *input.EnvironmentOwner
	}
	if !validAttemptWorkspaceWriteAccess(authority) {
		return nil, store.NewErrInvalidInput("assistant_session", "end_authority", nil)
	}
	if !input.AuditEventID.IsZero() && (!input.AuditEventID.IsValid() || input.AuditAt.IsZero() || input.EnvironmentOwner != nil) {
		return nil, store.NewErrInvalidInput("assistant_session", "end_authority", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "end assistant session", func(ctx context.Context,
		tx *sqlxTxWrapper) (*store.AssistantSessionEndResult, error) {
		_, now, err := lockAttemptWorkspaceMutationTarget(ctx, tx, authority)
		if err != nil {
			return nil, err
		}
		id := authority.WorkspaceAttemptID()
		if _, err = lockAssistantInventory(ctx, tx, id); err != nil {
			return nil, err
		}
		row, err := lockAssistantSession(ctx, tx, id, input.Tag)
		if err != nil {
			return nil, err
		}
		record, err := endAssistantSession(ctx, tx, id, row, input, now)
		if err != nil {
			return nil, err
		}
		result := &store.AssistantSessionEndResult{Record: record, Changed: row.State == "running"}
		if input.AuditEventID.IsZero() {
			return result, nil
		}
		data, err := model.EncodeAuditData(map[string]any{"attempt_id": id.String(), "process_tag": row.Tag, "outcome": record.Call.Outcome})
		if err != nil {
			return nil, err
		}
		_, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", data, input.AuditAt)
		return result, err
	})
}

func assistantSessionCall(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID, callID, phase string) (*model.AssistantRecord, error) {
	var raw []byte
	err := tx.Get(ctx, &raw, `SELECT canonical FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND call_id=? AND phase=?`,
		id.String(), callID, phase)
	if err != nil {
		return nil, translateError("assistant_call", callID, err)
	}
	record, err := model.DecodeAssistantRecord(raw)
	if err != nil || record.Call == nil || record.Call.Tool != "start_session" {
		return nil, invalidPersistedState("assistant_session", "canonical", err)
	}
	return &record, nil
}

func endAssistantSession(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID, row assistantSessionRow,
	input *store.AssistantSessionEnd, now time.Time) (*model.AssistantRecord, error) {
	if row.State == "ended" {
		return assistantSessionCall(ctx, tx, id, row.CallID, "session_ended")
	}
	request, err := assistantSessionCall(ctx, tx, id, row.CallID, "requested")
	if err != nil {
		return nil, err
	}
	call := *request.Call
	call.Phase, call.Approval, call.ProcessTag = "session_ended", "", row.Tag
	call.Arguments = json.RawMessage(`{}`)
	call.Outcome, call.ExitCode, call.Signal = input.Outcome, input.ExitCode, input.Signal
	call.DurationMilliseconds = min(input.DurationMilliseconds, row.ReservedMilliseconds)
	if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_sessions SET state='ended',ended_at=GREATEST(started_at,?)
		WHERE exam_attempt_id=? AND tag=?`, now, id.String(), row.Tag); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET command_milliseconds=command_milliseconds-? WHERE id=?`,
		row.ReservedMilliseconds-call.DurationMilliseconds, id.String()); err != nil {
		return nil, err
	}
	return appendAssistantRecord(ctx, tx, id, model.AssistantRecord{Call: &call}, true)
}

func sealAssistantSessions(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID) error {
	// Sealing holds the Attempt exclusively, so no editor can add a row or
	// race these updates. The terminal records must enter the sealed digest.
	var rows []assistantSessionRow
	if err := tx.Select(ctx, &rows, `SELECT `+assistantSessionColumns+` FROM exam_attempt_assistant_sessions
		WHERE exam_attempt_id=? AND state='running' ORDER BY started_at,tag FOR UPDATE`, id.String()); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	var now time.Time
	if err := tx.Get(ctx, &now, `SELECT statement_timestamp()`); err != nil {
		return err
	}
	for _, row := range rows {
		input := store.AssistantSessionEnd{Outcome: "sealed", DurationMilliseconds: max(0, now.Sub(row.StartedAt).Milliseconds())}
		if _, err := endAssistantSession(ctx, tx, id, row, &input, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLAssistantTranscriptStore) ListSessions(ctx context.Context, query store.AssistantSessionQuery) ([]model.AssistantSession, error) {
	return runSQLTransaction(ctx, s.GetMaster().Begin, "list assistant sessions", func(ctx context.Context, tx *sqlxTxWrapper) ([]model.AssistantSession, error) {
		var id model.ExamAttemptID
		var err error
		if query.EnvironmentOwner != nil {
			if query.Owner != (store.AssistantTranscriptOwnerAccess{}) || !query.ReviewSubmissionID.IsZero() {
				return nil, store.NewErrInvalidInput("assistant_session", "list_authority", nil)
			}
			_, _, err = lockAttemptWorkspaceMutationTarget(ctx, tx, *query.EnvironmentOwner)
			id = query.EnvironmentOwner.AttemptID
		} else {
			read := store.AssistantTranscriptQuery{ReviewSubmissionID: query.ReviewSubmissionID}
			if query.Owner != (store.AssistantTranscriptOwnerAccess{}) {
				read.AttemptID, read.Owner = query.Owner.AttemptID, &query.Owner
			}
			id, err = assistantReadAttempt(ctx, tx, read)
		}
		if err != nil {
			return nil, err
		}
		var rows []assistantSessionRow
		// A live turn may be between its reservation and the host start. Only
		// reconcile a missing process once that turn can no longer start it.
		if err = tx.Select(ctx, &rows, `SELECT `+assistantSessionColumns+`,
			(SELECT candidate_user_id FROM exam_attempts WHERE id=session.exam_attempt_id) AS candidate_user_id,
			NOT EXISTS(SELECT 1 FROM exam_attempt_connections connection
				JOIN exam_attempt_participations participation ON participation.id=connection.participation_id
				WHERE connection.id=session.connection_id AND connection.state='open' AND participation.state='active') AS access_ended,
			EXISTS(SELECT 1 FROM exam_attempt_assistant_runs run
				JOIN exam_attempt_assistant_calls request ON request.exam_attempt_id=run.id AND request.turn_id=run.turn_id
				WHERE run.id=session.exam_attempt_id AND run.active AND run.lease_expires_at>statement_timestamp()
				AND request.call_id=session.call_id AND request.phase='requested' AND NOT EXISTS(
					SELECT 1 FROM exam_attempt_assistant_calls completed WHERE completed.exam_attempt_id=session.exam_attempt_id
					AND completed.call_id=session.call_id AND completed.phase='completed')) AS starting
			FROM exam_attempt_assistant_sessions session WHERE exam_attempt_id=? AND (?='' OR tag=?)
			AND (NOT ? OR state='running')
			ORDER BY (state='running') DESC, CASE WHEN ? THEN ended_at ELSE started_at END DESC,tag DESC LIMIT 52`,
			id.String(), query.Tag, query.Tag, query.EnvironmentOwner != nil, !query.ReviewSubmissionID.IsZero()); err != nil {
			return nil, fmt.Errorf("list assistant sessions: %w", err)
		}
		sessions := make([]model.AssistantSession, 0, len(rows))
		for _, row := range rows {
			request, readErr := assistantSessionCall(ctx, tx, id, row.CallID, "requested")
			if readErr != nil {
				return nil, readErr
			}
			var args struct {
				Command string `json:"command"`
			}
			if err = json.Unmarshal(request.Call.Arguments, &args); err != nil {
				return nil, invalidPersistedState("assistant_session", "command", err)
			}
			session := model.AssistantSession{Tag: row.Tag, Starting: row.Starting, AccessEnded: row.AccessEnded, CandidateUserID: row.CandidateUserID,
				TurnID: request.Call.TurnID, CallID: request.Call.CallID, ConversationID: request.Call.ConversationID,
				Command: args.Command, ReservedMilliseconds: row.ReservedMilliseconds, ReadOffset: row.ReadOffset, State: row.State,
				StartedAt: model.TimeUTC(row.StartedAt)}
			if row.EndedAt != nil {
				end, endErr := assistantSessionCall(ctx, tx, id, row.CallID, "session_ended")
				if endErr != nil {
					return nil, endErr
				}
				session.EndedAt, session.Outcome = model.TimeUTC(*row.EndedAt), end.Call.Outcome
			}
			sessions = append(sessions, session)
		}
		return sessions, nil
	})
}
