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
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Cancel records the stop in the same transaction that ends the run, so a
// cancel and a racing reply cannot both finish the turn.
func (s *SQLAssistantTranscriptStore) Cancel(ctx context.Context, input store.AssistantRunCancel) error {
	if !input.TurnID.IsValid() || !validAttemptWorkspaceWriteAccess(input.Editor) || !input.AuditEventID.IsValid() || input.AuditAt.IsZero() {
		return store.NewErrInvalidInput("assistant_run", "cancel", nil)
	}
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "cancel assistant turn", func(ctx context.Context, tx *sqlxTxWrapper) (bool, error) {
		if _, _, err := lockAttemptWorkspaceMutationTarget(ctx, tx, input.Editor); err != nil {
			return false, err
		}
		var run assistantRunRow
		err := tx.Get(ctx, &run, `SELECT `+assistantRunColumns+` FROM exam_attempt_assistant_runs WHERE id=? FOR UPDATE`,
			input.Editor.AttemptID.String())
		if err != nil {
			return false, translateError("assistant_run", input.TurnID.String(), err)
		}
		if _, _, err = lockAttemptWorkspaceMutationTarget(ctx, tx, input.Editor); err != nil {
			return false, err
		}
		if !run.Active || run.TurnID != input.TurnID.String() {
			return false, store.NewErrNotFound("assistant_run", input.TurnID.String())
		}
		if err = endAssistantRun(ctx, tx, input.Editor.AttemptID, run, "canceled"); err != nil {
			return false, err
		}
		data, err := model.EncodeAuditData(map[string]any{"attempt_id": input.Editor.AttemptID.String(), "turn_id": input.TurnID.String()})
		if err != nil {
			return false, err
		}
		_, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", data, input.AuditAt)
		return err == nil, err
	})
	return err
}

// ReserveCommand charges the command budget before a process exists, so a
// crash after the start still counts the command.
func (s *SQLAssistantTranscriptStore) ReserveCommand(ctx context.Context,
	input *store.AssistantCommandReservation) (*store.AssistantCommandSlot, error) {
	if input == nil {
		return nil, store.NewErrInvalidInput("assistant_command", "reservation", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "reserve assistant command", func(ctx context.Context,
		tx *sqlxTxWrapper) (*store.AssistantCommandSlot, error) {
		run, _, err := lockAssistantRun(ctx, tx, input.Access, true)
		if err != nil {
			return nil, err
		}
		recipe, err := readAssistantRecipe(ctx, tx, input.Access.Editor.AttemptID)
		if err != nil {
			return nil, err
		}
		if !recipe.Execution {
			return nil, store.NewErrConflict("assistant_command", "assistant_tool_unavailable", nil)
		}
		if !input.CallID.IsValid() || input.TimeoutSeconds < 1 || input.TimeoutSeconds > 600 {
			return nil, store.NewErrInvalidInput("assistant_command", "request", nil)
		}
		if recipe.Policy.Level != model.AssistantLevelPair && recipe.Policy.Level != model.AssistantLevelAgent {
			return nil, store.NewErrConflict("assistant_command", "assistant_tool_unavailable", nil)
		}
		if run.ReservedCommandMilliseconds != 0 {
			return nil, store.NewErrConflict("assistant_command", "assistant_busy", nil)
		}
		request, err := readAssistantExecutionRequest(ctx, tx, input.Access.Editor.AttemptID, run, input.CallID, "run_command", recipe.Policy.Approvals)
		if err != nil {
			return nil, err
		}

		inventory, err := lockAssistantInventory(ctx, tx, input.Access.Editor.AttemptID)
		if err != nil {
			return nil, err
		}
		budgets := recipe.Policy.Budgets
		if inventory.Commands >= int64(budgets.Commands) || budgets.CommandSeconds*1000-inventory.CommandMilliseconds < 1000 {
			return nil, store.NewErrConflict("assistant_command", "assistant_budget_exhausted", nil)
		}
		timeout := min(input.TimeoutSeconds, budgets.CommandTimeoutSeconds, (budgets.CommandSeconds*1000-inventory.CommandMilliseconds)/1000)
		next := run.CommandSequence + 1
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET commands=commands+1,
		command_milliseconds=command_milliseconds+? WHERE id=?`, timeout*1000,
			input.Access.Editor.AttemptID.String()); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET command_sequence=?,reserved_command_milliseconds=? WHERE id=?`, next, timeout*1000,
			input.Access.Editor.AttemptID.String()); err != nil {
			return nil, err
		}
		reservation := *request
		reservation.Phase = "reserved"
		reservation.Arguments = json.RawMessage(`{}`)
		reservation.ProcessTag = "assistant:" + strconv.FormatInt(next, 10)
		reservation.Approval = ""
		if _, err = appendAssistantRecord(ctx, tx, input.Access.Editor.AttemptID, model.AssistantRecord{Call: &reservation}, false); err != nil {
			return nil, err
		}

		return &store.AssistantCommandSlot{Tag: "assistant:" + strconv.FormatInt(next, 10), TimeoutSeconds: timeout}, nil
	})
}

// readAssistantExecutionRequest ties an execution reservation to the original
// call and, when required, a timely candidate approval.
func readAssistantExecutionRequest(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID, run assistantRunRow,
	callID model.AssistantCallID, tool string, mode model.AssistantApprovalMode) (*model.AssistantCall, error) {
	var raw []byte
	if err := tx.Get(ctx, &raw, `SELECT canonical FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND turn_id=? AND call_id=?
	AND phase='requested'`, id.String(), run.TurnID, callID.String()); err != nil {
		return nil, translateError("assistant_call", callID.String(), err)
	}
	request, err := model.DecodeAssistantRecord(raw)
	if err != nil {
		return nil, err
	}
	if request.Call == nil || request.Call.Tool != tool {
		return nil, store.NewErrInvalidInput("assistant_command", "call", nil)
	}
	if mode == model.AssistantApprovalAlwaysAsk {
		var decisionRaw []byte
		if err = tx.Get(ctx, &decisionRaw, `SELECT canonical FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND call_id=? AND
		phase='decision'`, id.String(), callID.String()); err != nil {
			return nil, translateError("assistant_call", callID.String(), err)
		}
		decision, decodeErr := model.DecodeAssistantRecord(decisionRaw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if decision.Call == nil || decision.Call.Approval != "accept" || !decision.CreatedAt.Before(request.CreatedAt.Add(5*time.Minute)) {
			return nil, store.NewErrConflict("assistant_call", "assistant_approval_decided", nil)
		}
	}
	return request.Call, nil
}

// Decide records the candidate's answer once. The unique call phase makes a
// racing expiry by the owner and a late answer exclude each other.
func (s *SQLAssistantTranscriptStore) Decide(ctx context.Context, input store.AssistantCallDecision) (*model.AssistantRecord, error) {
	if !input.CallID.IsValid() || !validAttemptWorkspaceWriteAccess(input.Editor) || !input.AuditEventID.IsValid() || input.AuditAt.IsZero() {
		return nil, store.NewErrInvalidInput("assistant_call", "decision", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "decide assistant call", func(ctx context.Context,
		tx *sqlxTxWrapper) (*model.AssistantRecord, error) {
		if _, _, err := lockAttemptWorkspaceMutationTarget(ctx, tx, input.Editor); err != nil {
			return nil, err
		}
		var run assistantRunRow
		err := tx.Get(ctx, &run, `SELECT `+assistantRunColumns+` FROM exam_attempt_assistant_runs WHERE id=? FOR UPDATE`,
			input.Editor.AttemptID.String())
		if err != nil {
			return nil, translateError("assistant_call", input.CallID.String(), err)
		}
		if _, _, err = lockAttemptWorkspaceMutationTarget(ctx, tx, input.Editor); err != nil {
			return nil, err
		}
		if !run.Active {
			return nil, store.NewErrNotFound("assistant_call", input.CallID.String())
		}
		var raw []byte
		err = tx.Get(ctx, &raw, `SELECT canonical FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND turn_id=? AND call_id=?
		AND phase='requested'`, input.Editor.AttemptID.String(), run.TurnID, input.CallID.String())
		if err != nil {
			return nil, translateError("assistant_call", input.CallID.String(), err)
		}
		requested, err := model.DecodeAssistantRecord(raw)
		if err != nil || requested.Call == nil {
			return nil, invalidPersistedState("assistant_call", "canonical", err)
		}
		var now time.Time
		if err = tx.Get(ctx, &now, `SELECT statement_timestamp()`); err != nil {
			return nil, err
		}
		if !now.Before(requested.CreatedAt.Add(5 * time.Minute)) {
			return nil, store.NewErrConflict("assistant_call", "assistant_approval_decided", nil)
		}

		if requested.Call.Approval != "pending" {
			return nil, store.NewErrNotFound("assistant_call", input.CallID.String())
		}
		var decided bool
		if err = tx.Get(ctx, &decided, `SELECT EXISTS(SELECT 1 FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND call_id=?
		AND phase IN ('decision','completed'))`, input.Editor.AttemptID.String(), input.CallID.String()); err != nil {
			return nil, err
		}
		if decided {
			return nil, store.NewErrConflict("assistant_call", "assistant_approval_decided", nil)
		}
		call := *requested.Call
		call.Phase, call.Arguments, call.Result, call.Outcome, call.Approval = "decision", json.RawMessage(`{}`), "", "", "decline"
		if input.Accept {
			call.Approval = "accept"
		}
		record, err := appendAssistantRecord(ctx, tx, input.Editor.AttemptID, model.AssistantRecord{Call: &call}, false)
		if err != nil {
			return nil, err
		}
		data, err := model.EncodeAuditData(map[string]any{"attempt_id": input.Editor.AttemptID.String(), "call_id": input.CallID.String(),
			"decision": call.Approval})
		if err != nil {
			return nil, err
		}
		_, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", data, input.AuditAt)
		return record, err
	})
}

// Decision is "" until the call has an answer: accept, decline or expired.
func (s *SQLAssistantTranscriptStore) Decision(ctx context.Context, access store.AssistantRunAccess, call model.AssistantCallID) (string, error) {
	if !call.IsValid() {
		return "", store.NewErrInvalidInput("assistant_call", "call_id", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "read assistant call decision", func(ctx context.Context,
		tx *sqlxTxWrapper) (string, error) {
		if _, _, err := lockAssistantRun(ctx, tx, access, true); err != nil {
			return "", err
		}
		var raw []byte
		err := tx.Get(ctx, &raw, `SELECT canonical FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND call_id=? AND phase='decision'`,
			access.Editor.AttemptID.String(), call.String())
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		record, err := model.DecodeAssistantRecord(raw)
		if err != nil || record.Call == nil {
			return "", invalidPersistedState("assistant_call", "canonical", err)
		}
		return record.Call.Approval, nil
	})
}
