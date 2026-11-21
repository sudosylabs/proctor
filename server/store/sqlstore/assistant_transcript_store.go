// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLAssistantTranscriptStore serializes run ownership and transcript appends per Attempt.
type SQLAssistantTranscriptStore struct{ *SQLStore }

func newSQLAssistantTranscriptStore(s *SQLStore) store.AssistantTranscriptStore {
	return &SQLAssistantTranscriptStore{SQLStore: s}
}

type assistantInventoryRow struct {
	model.AssistantInventory
	SubmissionID        sql.NullString `db:"submission_id"`
	Turns               int64          `db:"turns"`
	InputTokens         int64          `db:"input_tokens"`
	OutputTokens        int64          `db:"output_tokens"`
	ToolCalls           int64          `db:"tool_calls"`
	Commands            int64          `db:"commands"`
	CommandMilliseconds int64          `db:"command_milliseconds"`
}

const assistantInventoryColumns = `records,bytes,gaps,digest,submission_id,turns,input_tokens,output_tokens,tool_calls,commands,command_milliseconds,sessions,session_inputs`

func lockAssistantInventory(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID) (assistantInventoryRow, error) {
	var row assistantInventoryRow
	if _, err := tx.Exec(ctx, `INSERT INTO exam_attempt_assistant_inventories(id,digest) VALUES(?,?) ON CONFLICT(id) DO NOTHING`,
		id.String(), model.NewAssistantInventory().Digest); err != nil {
		return row, err
	}
	err := tx.Get(ctx, &row, `SELECT `+assistantInventoryColumns+` FROM exam_attempt_assistant_inventories WHERE id=? FOR UPDATE`, id.String())
	if err != nil {
		return row, fmt.Errorf("lock assistant inventory: %w", err)
	}
	if row.SubmissionID.Valid {
		return row, store.NewErrNotFound("exam_attempt_access", id.String())
	}
	return row, row.AssistantInventory.Validate()
}

type assistantRunRow struct {
	ConversationID              string    `db:"conversation_id"`
	TurnID                      string    `db:"turn_id"`
	OwnerHash                   string    `db:"owner_hash"`
	Active                      bool      `db:"active"`
	LeaseExpiresAt              time.Time `db:"lease_expires_at"`
	ReservedInput               int64     `db:"reserved_input"`
	ReservedOutput              int64     `db:"reserved_output"`
	UsageInput                  int64     `db:"usage_input"`
	UsageCacheRead              int64     `db:"usage_cache_read"`
	UsageCacheWrite             int64     `db:"usage_cache_write"`
	UsageOutput                 int64     `db:"usage_output"`
	UsageEstimated              bool      `db:"usage_estimated"`
	ReservedCommandMilliseconds int64     `db:"reserved_command_milliseconds"`
	CommandSequence             int64     `db:"command_sequence"`
}

const assistantRunColumns = `conversation_id,turn_id,owner_hash,active,lease_expires_at,reserved_input,reserved_output,usage_input,
usage_cache_read,usage_cache_write,usage_output,usage_estimated,command_sequence,reserved_command_milliseconds`

func (r assistantRunRow) usage() model.AssistantUsage {
	return model.AssistantUsage{Input: r.UsageInput + r.ReservedInput, CacheRead: r.UsageCacheRead, CacheWrite: r.UsageCacheWrite,
		Output: r.UsageOutput + r.ReservedOutput, Estimated: r.UsageEstimated || r.ReservedInput > 0 || r.ReservedOutput > 0}
}
func readAssistantRecipe(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID) (store.AssistantRecipe, error) {
	var row struct {
		Policy         []byte `db:"policy"`
		Provider       string `db:"provider"`
		Model          string `db:"model"`
		DisplayName    string `db:"display_name"`
		Pseudonymize   bool   `db:"pseudonymize"`
		RevisionPolicy []byte `db:"revision_policy"`
	}
	err := tx.Get(ctx, &row, `SELECT a.policy,a.provider,a.model,a.display_name,a.pseudonymize,r.policy_canonical AS revision_policy
 FROM exam_attempt_assistants a JOIN exam_attempts attempt ON attempt.id=a.id JOIN exam_revisions r ON
 r.id=attempt.admission_revision_id WHERE a.id=?`, id.String())
	if err != nil {
		return store.AssistantRecipe{}, translateError("assistant_recipe", id.String(), err)
	}
	policy, err := model.DecodeAssistantPolicy(row.Policy)
	if err != nil {
		return store.AssistantRecipe{}, invalidPersistedState("assistant_recipe", "policy", err)
	}
	revision, err := model.DecodeExamPolicySet(row.RevisionPolicy)
	if err != nil {
		return store.AssistantRecipe{}, invalidPersistedState("assistant_recipe", "revision_policy", err)
	}
	if !policy.Equal(revision.Assistant) {
		return store.AssistantRecipe{}, invalidPersistedState("assistant_recipe", "admission_policy", model.ErrAssistantTranscriptInvalid)
	}

	return store.AssistantRecipe{Policy: policy, Provider: row.Provider, Model: row.Model, DisplayName: row.DisplayName,
		Pseudonymize: row.Pseudonymize, Execution: revision.Execution.Enabled}, nil
}
func validAssistantRunAccess(a store.AssistantRunAccess) bool {
	return a.TurnID.IsValid() && model.IsValidCredentialToken(a.OwnerToken) && validAttemptWorkspaceWriteAccess(a.Editor)
}

// A final append can record access loss, but still needs the current owner and
// lease. Submission holds the Attempt exclusively and closes this path first.
func lockAssistantRun(ctx context.Context, tx *sqlxTxWrapper, a store.AssistantRunAccess, live bool) (assistantRunRow, time.Time, error) {
	var row assistantRunRow
	if !validAssistantRunAccess(a) {
		return row, time.Time{}, store.NewErrInvalidInput("assistant_run", "access", nil)
	}
	var now time.Time
	if live {
		_, at, err := lockAttemptWorkspaceMutationTarget(ctx, tx, a.Editor)
		if err != nil {
			return row, now, err
		}
		now = at
	} else {
		var id string
		err := tx.Get(ctx, &id, `SELECT id FROM exam_attempts a WHERE id=? AND NOT EXISTS(SELECT 1 FROM exam_submissions sub WHERE
		sub.exam_attempt_id=a.id) FOR SHARE`, a.Editor.AttemptID.String())
		if err != nil {
			return row, now, translateError("assistant_run", a.Editor.AttemptID.String(), err)
		}
		if err = tx.Get(ctx, &now, `SELECT statement_timestamp()`); err != nil {
			return row, now, err
		}
	}
	err := tx.Get(ctx, &row, `SELECT `+assistantRunColumns+` FROM exam_attempt_assistant_runs WHERE id=? FOR UPDATE`, a.Editor.AttemptID.String())
	if err != nil {
		return row, now, translateError("assistant_run", a.Editor.AttemptID.String(), err)
	}
	// A contended run lock can outlive an eligibility deadline. The first check
	// holds the same locks, so repeating it here takes no locks in a new order.
	if live {
		_, at, checkErr := lockAttemptWorkspaceMutationTarget(ctx, tx, a.Editor)
		if checkErr != nil {
			return row, now, checkErr
		}
		now = at
	} else if err = tx.Get(ctx, &now, `SELECT statement_timestamp()`); err != nil {
		return row, now, err
	}

	hash := model.HashToken(a.OwnerToken)
	if !row.Active || row.TurnID != a.TurnID.String() || !now.Before(row.LeaseExpiresAt) || subtle.ConstantTimeCompare([]byte(hash),
		[]byte(row.OwnerHash)) != 1 {
		return row, now, store.NewErrNotFound("assistant_run", a.TurnID.String())
	}
	return row, model.TimeUTC(now), nil
}

func appendAssistantRecord(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID, record model.AssistantRecord,
	terminal bool) (*model.AssistantRecord, error) {
	inventory, err := lockAssistantInventory(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	record.Sequence = inventory.Records + 1
	if err = tx.Get(ctx, &record.CreatedAt, `SELECT statement_timestamp()`); err != nil {
		return nil, err
	}
	record.CreatedAt = model.TimeUTC(record.CreatedAt)
	maximumBytes := min(int64(model.AssistantRecordMaximumBytes), model.AssistantTranscriptMaximumBytes-inventory.Bytes)
	if !terminal {
		maximumBytes = min(maximumBytes, model.AssistantTranscriptMaximumBytes-inventory.Bytes-(64<<10))
	}
	canonical, err := record.Canonical()
	if err != nil || int64(len(canonical)) > maximumBytes {
		if call := record.Call; call != nil && call.Tool == "run_command" && call.Phase == "completed" && call.ProcessTag != "" {
			record = truncateAssistantCommandRecord(record, int(maximumBytes))
			canonical, err = record.Canonical()
		}
	}
	if err != nil {
		return nil, store.NewErrInvalidInput("assistant_record", "document", err)
	}
	// Leave enough room to close a pending tool and the turn at any seal.
	if !terminal && (inventory.Records >= model.AssistantTranscriptMaximumRecords-32 ||
		int64(len(canonical)) > maximumBytes) {
		return nil, store.NewErrConflict("assistant_transcript", "assistant_budget_exhausted", nil)
	}
	next, err := inventory.AssistantInventory.Append(record)
	if err != nil {
		return nil, store.NewErrConflict("assistant_transcript", "assistant_budget_exhausted", nil)
	}
	if record.Turn != nil {
		_, err = tx.Exec(ctx, `INSERT INTO exam_attempt_assistant_turns(exam_attempt_id,sequence,turn_id,canonical,created_at)
		VALUES(?,?,?,?,?)`, id.String(), record.Sequence, record.Turn.TurnID.String(), canonical, record.CreatedAt)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO exam_attempt_assistant_calls(exam_attempt_id,sequence,turn_id,call_id,tool,phase,canonical,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, id.String(), record.Sequence, record.Call.TurnID.String(), record.Call.CallID.String(), record.Call.Tool, record.Call.Phase,
			canonical, record.CreatedAt)
	}
	if err != nil {
		return nil, fmt.Errorf("append assistant record: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET records=?,bytes=?,gaps=?,digest=? WHERE id=?`, next.Records,
		next.Bytes, next.Gaps, next.Digest, id.String())
	if err != nil {
		return nil, fmt.Errorf("extend assistant inventory: %w", err)
	}
	return &record, nil
}

// Keep the largest prefix that fits after JSON escaping and record metadata.
// A result that fills the remaining inventory must still leave room for the
// terminal record and retain the command's tag, exit status and gap marker.
func truncateAssistantCommandRecord(record model.AssistantRecord, limit int) model.AssistantRecord {
	call := *record.Call
	record.Call = &call
	record.Gap, call.Truncated = true, true
	result := call.Result
	call.Result = ""
	low, high := 0, len(result)
	for low < high {
		middle := low + (high-low+1)/2
		end := middle
		for end < len(result) && !utf8.RuneStart(result[end]) {
			end--
		}
		call.Result = result[:end]
		if raw, err := record.Canonical(); err == nil && len(raw) <= limit {
			low = middle
		} else {
			high = middle - 1
		}
	}
	for low < len(result) && !utf8.RuneStart(result[low]) {
		low--
	}
	call.Result = result[:low]
	return record
}

func (s *SQLAssistantTranscriptStore) Renew(ctx context.Context, a store.AssistantRunAccess) error {
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "renew assistant run", func(ctx context.Context, tx *sqlxTxWrapper) (bool, error) {
		_, now, err := lockAssistantRun(ctx, tx, a, true)
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET lease_expires_at=? WHERE id=?`, now.Add(20*time.Second), a.Editor.AttemptID.String())
		return err == nil, err
	})
	return err
}
func (s *SQLAssistantTranscriptStore) ReserveModel(ctx context.Context, input *store.AssistantModelReservation) error {
	if input == nil || input.InputTokens < 1 || input.OutputTokens < 1 {
		return store.NewErrInvalidInput("assistant_model", "reservation", nil)
	}
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "reserve assistant model budget", func(ctx context.Context, tx *sqlxTxWrapper) (bool, error) {
		run, _, err := lockAssistantRun(ctx, tx, input.Access, true)
		if err != nil {
			return false, err
		}
		if run.ReservedInput != 0 || run.ReservedOutput != 0 {
			return false, store.NewErrConflict("assistant_model", "assistant_busy", nil)
		}
		recipe, err := readAssistantRecipe(ctx, tx, input.Access.Editor.AttemptID)
		if err != nil {
			return false, err
		}
		inventory, err := lockAssistantInventory(ctx, tx, input.Access.Editor.AttemptID)
		if err != nil {
			return false, err
		}
		if input.InputTokens > recipe.Policy.Budgets.InputTokens-inventory.InputTokens ||
			input.OutputTokens > recipe.Policy.Budgets.OutputTokens-inventory.OutputTokens {
			return false, store.NewErrConflict("assistant_model", "assistant_budget_exhausted", nil)
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET input_tokens=input_tokens+?,output_tokens=output_tokens+?
		WHERE id=?`, input.InputTokens, input.OutputTokens, input.Access.Editor.AttemptID.String()); err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET reserved_input=?,reserved_output=? WHERE id=?`, input.InputTokens,
			input.OutputTokens, input.Access.Editor.AttemptID.String())
		return err == nil, err
	})
	return err
}
func (s *SQLAssistantTranscriptStore) CompleteModel(ctx context.Context, input *store.AssistantModelCompletion) (model.AssistantUsage, error) {
	if input == nil || !input.Usage.Valid() {
		return model.AssistantUsage{}, store.NewErrInvalidInput("assistant_model", "usage", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "settle assistant model budget", func(ctx context.Context,
		tx *sqlxTxWrapper) (model.AssistantUsage, error) {
		run, _, err := lockAssistantRun(ctx, tx, input.Access, true)
		if err != nil {
			return model.AssistantUsage{}, err
		}
		usage := input.Usage
		if run.ReservedInput < 1 || run.ReservedOutput < 1 || usage.InputTotal() > run.ReservedInput || usage.Output > run.ReservedOutput {
			return model.AssistantUsage{}, store.NewErrInvalidInput("assistant_model", "usage_reservation", nil)
		}
		if _, err = lockAssistantInventory(ctx, tx, input.Access.Editor.AttemptID); err != nil {
			return model.AssistantUsage{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET input_tokens=input_tokens-?,output_tokens=output_tokens-?
		WHERE id=?`, run.ReservedInput-usage.InputTotal(), run.ReservedOutput-usage.Output, input.Access.Editor.AttemptID.String()); err != nil {
			return model.AssistantUsage{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET
		reserved_input=0,reserved_output=0,usage_input=usage_input+?,usage_cache_read=usage_cache_read+?,
		usage_cache_write=usage_cache_write+?,usage_output=usage_output+?,usage_estimated=usage_estimated OR ? WHERE id=?`, usage.Input,
			usage.CacheRead, usage.CacheWrite, usage.Output, usage.Estimated, input.Access.Editor.AttemptID.String())
		if err != nil {
			return model.AssistantUsage{}, err
		}
		run.UsageInput += usage.Input
		run.UsageCacheRead += usage.CacheRead
		run.UsageCacheWrite += usage.CacheWrite
		run.UsageOutput += usage.Output
		run.UsageEstimated = run.UsageEstimated || usage.Estimated
		run.ReservedInput = 0
		run.ReservedOutput = 0
		return run.usage(), nil
	})
}

func (s *SQLAssistantTranscriptStore) AppendTurn(ctx context.Context, input *store.AssistantTurnAppend) (*model.AssistantRecord, error) {
	if input == nil || input.Turn.Role != "assistant" || !input.Turn.Valid() {
		return nil, store.NewErrInvalidInput("assistant_turn", "append", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "append assistant turn", func(ctx context.Context, tx *sqlxTxWrapper) (*model.AssistantRecord, error) {
		run, _, err := lockAssistantRun(ctx, tx, input.Access, !input.Turn.Final)
		if err != nil {
			return nil, err
		}
		if run.ConversationID != input.Turn.ConversationID.String() || run.TurnID != input.Turn.TurnID.String() {
			return nil, store.NewErrInvalidInput("assistant_turn", "run", nil)
		}
		turn := input.Turn
		if turn.Final {
			turn.Usage = run.usage()
		}
		record, err := appendAssistantRecord(ctx, tx, input.Access.Editor.AttemptID, model.AssistantRecord{Turn: &turn, Gap: input.Gap}, turn.Final)
		if err != nil {
			return nil, err
		}
		if turn.Final {
			_, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET active=false WHERE id=?`, input.Access.Editor.AttemptID.String())
		}
		return record, err
	})
}
func (s *SQLAssistantTranscriptStore) AppendCall(ctx context.Context, input *store.AssistantCallAppend) (*model.AssistantRecord, error) {
	return s.appendCall(ctx, input, false)
}
func (s *SQLAssistantTranscriptStore) CompleteCall(ctx context.Context, input *store.AssistantCallAppend) (*model.AssistantRecord, error) {
	return s.appendCall(ctx, input, true)
}
func (s *SQLAssistantTranscriptStore) appendCall(ctx context.Context, input *store.AssistantCallAppend, complete bool) (*model.AssistantRecord, error) {
	if input == nil || !input.Call.Valid() || input.Call.Phase == "session_ended" || complete != (input.Call.Phase == "completed") {
		return nil, store.NewErrInvalidInput("assistant_call", "append", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "append assistant tool call", func(ctx context.Context,
		tx *sqlxTxWrapper) (*model.AssistantRecord, error) {
		run, _, err := lockAssistantRun(ctx, tx, input.Access, true)
		if err != nil {
			return nil, err
		}
		if run.ConversationID != input.Call.ConversationID.String() || run.TurnID != input.Call.TurnID.String() {
			return nil, store.NewErrInvalidInput("assistant_call", "run", nil)
		}
		if input.Call.Phase != "requested" {
			var found bool
			if err = tx.Get(ctx, &found, `SELECT EXISTS(SELECT 1 FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND call_id=? AND
			phase='requested')`, input.Access.Editor.AttemptID.String(), input.Call.CallID.String()); err != nil {
				return nil, err
			}
			if !found {
				return nil, store.NewErrNotFound("assistant_call", input.Call.CallID.String())
			}
		} else if input.Call.Tool != "output_check" {
			recipe, recipeErr := readAssistantRecipe(ctx, tx, input.Access.Editor.AttemptID)
			if recipeErr != nil {
				return nil, recipeErr
			}
			inventory, inventoryErr := lockAssistantInventory(ctx, tx, input.Access.Editor.AttemptID)
			if inventoryErr != nil {
				return nil, inventoryErr
			}
			if inventory.ToolCalls >= int64(recipe.Policy.Budgets.ToolCalls) {
				return nil, store.NewErrConflict("assistant_call", "assistant_budget_exhausted", nil)
			}
			if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET tool_calls=tool_calls+1 WHERE id=?`,
				input.Access.Editor.AttemptID.String()); err != nil {
				return nil, err
			}
		}
		if input.Call.Phase == "completed" && input.Call.Tool == "run_command" && input.Call.ProcessTag != "" && run.ReservedCommandMilliseconds > 0 {
			if input.Call.ProcessTag != "assistant:"+strconv.FormatInt(run.CommandSequence, 10) {
				return nil, store.NewErrInvalidInput("assistant_command", "process_tag", nil)
			}
			used := run.ReservedCommandMilliseconds
			if input.Call.ExitCode != nil || input.Call.DurationMilliseconds > 0 {
				used = min(used, input.Call.DurationMilliseconds)
			}
			if _, err = lockAssistantInventory(ctx, tx, input.Access.Editor.AttemptID); err != nil {
				return nil, err
			}
			if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET command_milliseconds=command_milliseconds-? WHERE id=?`,
				run.ReservedCommandMilliseconds-used, input.Access.Editor.AttemptID.String()); err != nil {
				return nil, err
			}
			if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET reserved_command_milliseconds=0 WHERE id=?`,
				input.Access.Editor.AttemptID.String()); err != nil {
				return nil, err
			}
		}

		return appendAssistantRecord(ctx, tx, input.Access.Editor.AttemptID, model.AssistantRecord{Call: &input.Call, Gap: input.Gap}, false)
	})
}

// Finish orphaned work in the submission transaction. No provider or host is
// contacted here; loss of the run fence stops the owner independently.
func sealAssistantTranscript(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID) error {
	var run assistantRunRow
	err := tx.Get(ctx, &run, `SELECT `+assistantRunColumns+` FROM exam_attempt_assistant_runs WHERE id=? AND active=true FOR UPDATE`, id.String())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock sealed assistant run: %w", err)
	}
	if run.Active {
		if err = endAssistantRun(ctx, tx, id, run, "sealed"); err != nil {
			return err
		}
	}
	return sealAssistantSessions(ctx, tx, id)
}
func endAssistantRun(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID, run assistantRunRow, reason string) error {
	recipe, err := readAssistantRecipe(ctx, tx, id)
	if err != nil {
		return err
	}
	// A pending call remains visible even when a seal cuts off its result.
	var pending [][]byte
	err = tx.Select(ctx, &pending, `SELECT request.canonical FROM exam_attempt_assistant_calls request
 WHERE request.exam_attempt_id=? AND request.turn_id=? AND request.phase='requested' AND NOT EXISTS(
 SELECT 1 FROM exam_attempt_assistant_calls done WHERE done.exam_attempt_id=request.exam_attempt_id AND done.call_id=request.call_id
 AND done.phase='completed') ORDER BY request.sequence`, id.String(), run.TurnID)
	if err != nil {
		return err
	}
	for _, raw := range pending {
		original, decodeErr := model.DecodeAssistantRecord(raw)
		if decodeErr != nil || original.Call == nil {
			return invalidPersistedState("assistant_call", "canonical", decodeErr)
		}
		call := *original.Call
		call.Phase = "completed"
		call.Result = ""
		call.Outcome = "canceled"
		call.Approval = ""
		call.Arguments = json.RawMessage(`{}`)
		var reservedRaw []byte
		reserveErr := tx.Get(ctx, &reservedRaw, `SELECT canonical FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND call_id=? AND phase='reserved'`, id.String(), call.CallID.String())
		if reserveErr == nil {
			reservation, decodeErr := model.DecodeAssistantRecord(reservedRaw)
			if decodeErr != nil {
				return decodeErr
			}
			if reservation.Call == nil {
				return model.ErrAssistantTranscriptInvalid
			}
			call.ProcessTag = reservation.Call.ProcessTag
		} else if !errors.Is(reserveErr, sql.ErrNoRows) {
			return reserveErr
		}

		if _, err = appendAssistantRecord(ctx, tx, id, model.AssistantRecord{Call: &call, Gap: true}, true); err != nil {
			return err
		}
	}
	turn := model.AssistantTurn{ConversationID: model.AssistantConversationID(run.ConversationID),
		TurnID: model.AssistantTurnID(run.TurnID), Role: "assistant", Model: recipe.Model, Usage: run.usage(), Final: true, StopReason: reason}
	if _, err = appendAssistantRecord(ctx, tx, id, model.AssistantRecord{Turn: &turn, Gap: true}, true); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET active=false WHERE id=?`, id.String())
	return err
}
