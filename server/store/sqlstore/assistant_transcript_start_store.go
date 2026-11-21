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

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type assistantStartOutcome struct {
	RemainingInputTokens  int64                 `json:"remaining_input_tokens"`
	RemainingOutputTokens int64                 `json:"remaining_output_tokens"`
	Policy                model.AssistantPolicy `json:"policy"`
	Provider              string                `json:"provider"`
	Model                 string                `json:"model"`
	DisplayName           string                `json:"display_name"`
	Pseudonymize          bool                  `json:"pseudonymize"`
	Execution             bool                  `json:"execution"`
	ConversationID        string                `json:"conversation_id"`
	TurnID                string                `json:"turn_id"`
}

func (assistantStartOutcome) outcomeDocument() {}
func (o assistantStartOutcome) result(replayed bool) *store.AssistantRunStart {
	return &store.AssistantRunStart{Recipe: store.AssistantRecipe{Policy: o.Policy, Provider: o.Provider, Model: o.Model,
		DisplayName: o.DisplayName, Pseudonymize: o.Pseudonymize, Execution: o.Execution},
		ConversationID: model.AssistantConversationID(o.ConversationID), TurnID: model.AssistantTurnID(o.TurnID), Replayed: replayed,
		RemainingInputTokens: o.RemainingInputTokens, RemainingOutputTokens: o.RemainingOutputTokens}
}
func (s *SQLAssistantTranscriptStore) Start(ctx context.Context, input *store.AssistantMessageStart,
	command *store.CommandIdempotency) (*store.AssistantRunStart, error) {
	if input == nil || command == nil || command.UserID != input.Access.CandidateUserID || !input.ConversationID.IsValid() ||
		!input.TurnID.IsValid() || !model.IsValidCredentialToken(input.OwnerToken) || !input.AuditEventID.IsValid() || input.AuditAt.IsZero() {
		return nil, store.NewErrInvalidInput("assistant_message", "start", nil)
	}
	turn := model.AssistantTurn{ConversationID: input.ConversationID, TurnID: input.TurnID, Role: "candidate", Text: input.Text, Final: true}
	if !turn.Valid() {
		return nil, store.NewErrInvalidInput("assistant_message", "text", nil)
	}
	result, err := runIdempotentMutation(ctx, s.SQLStore, "start assistant turn", idempotentMutation[assistantStartOutcome]{
		command: command, auditEventID: input.AuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (assistantStartOutcome, error) {
			return startAssistantTurn(ctx, tx, input)
		},
		encode: func(outcome assistantStartOutcome) ([]byte, error) { return json.Marshal(outcome) },
		decode: func(data []byte) (assistantStartOutcome, error) {
			var outcome assistantStartOutcome
			if err := decodeOutcome(data, &outcome); err != nil {
				return outcome, err
			}
			if !model.AssistantConversationID(outcome.ConversationID).IsValid() || !model.AssistantTurnID(outcome.TurnID).IsValid() ||
				outcome.Policy.Validate() != nil {
				return outcome, model.ErrAssistantTranscriptInvalid
			}
			return outcome, nil
		},
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, outcome assistantStartOutcome, original string) error {
			if _, _, err := lockAttemptWorkspaceMutationTarget(ctx, tx, input.Access); err != nil {
				return err
			}
			data, err := model.EncodeAuditData(map[string]any{"attempt_id": input.Access.AttemptID.String(), "turn_id": outcome.TurnID,
				"idempotency_replayed": true, "original_audit_event_id": original})
			if err != nil {
				return err
			}
			_, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", data, input.AuditAt)
			return err
		},
	})
	if err != nil {
		return nil, err
	}
	return result.Value.result(result.Replayed), nil
}
func startAssistantTurn(ctx context.Context, tx *sqlxTxWrapper, input *store.AssistantMessageStart) (assistantStartOutcome, error) {
	var zero assistantStartOutcome
	_, now, err := lockAttemptWorkspaceMutationTarget(ctx, tx, input.Access)
	if err != nil {
		return zero, err
	}
	recipe, err := readAssistantRecipe(ctx, tx, input.Access.AttemptID)
	if err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO exam_attempt_assistant_runs(id,conversation_id,turn_id,owner_hash,active,lease_expires_at)
 VALUES(?,?,?,?,false,?) ON CONFLICT(id) DO NOTHING`, input.Access.AttemptID.String(), input.ConversationID.String(),
		input.TurnID.String(), model.HashToken(input.OwnerToken), now); err != nil {
		return zero, err
	}
	var run assistantRunRow
	if err = tx.Get(ctx, &run, `SELECT `+assistantRunColumns+` FROM exam_attempt_assistant_runs WHERE id=? FOR UPDATE`,
		input.Access.AttemptID.String()); err != nil {
		return zero, err
	}
	if _, now, err = lockAttemptWorkspaceMutationTarget(ctx, tx, input.Access); err != nil {
		return zero, err
	}

	if run.Active {
		if now.Before(run.LeaseExpiresAt) {
			return zero, store.NewErrConflict("assistant_run", "assistant_busy", nil)
		}
		if err = endAssistantRun(ctx, tx, input.Access.AttemptID, run, "lease_expired"); err != nil {
			return zero, err
		}
	}
	inventory, err := lockAssistantInventory(ctx, tx, input.Access.AttemptID)
	if err != nil {
		return zero, err
	}
	if inventory.Turns >= int64(recipe.Policy.Budgets.Turns) || inventory.InputTokens >= recipe.Policy.Budgets.InputTokens ||
		inventory.OutputTokens >= recipe.Policy.Budgets.OutputTokens || inventory.Records >= model.AssistantTranscriptMaximumRecords-34 ||
		inventory.Bytes+int64(len(input.Text)) > model.AssistantTranscriptMaximumBytes-(96<<10) {
		return zero, store.NewErrConflict("assistant_run", "assistant_budget_exhausted", nil)
	}
	turn := model.AssistantTurn{ConversationID: model.AssistantConversationID(run.ConversationID), TurnID: input.TurnID,
		Role: "candidate", Text: input.Text, Final: true}
	if _, err = appendAssistantRecord(ctx, tx, input.Access.AttemptID, model.AssistantRecord{Turn: &turn}, false); err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_inventories SET turns=turns+1 WHERE id=?`, input.Access.AttemptID.String()); err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE exam_attempt_assistant_runs SET
	turn_id=?,owner_hash=?,active=true,lease_expires_at=?,reserved_input=0,reserved_output=0,reserved_command_milliseconds=0,usage_input=0,usage_cache_read=0,
	usage_cache_write=0,usage_output=0,usage_estimated=false WHERE id=?`, input.TurnID.String(), model.HashToken(input.OwnerToken),
		now.Add(20*time.Second), input.Access.AttemptID.String()); err != nil {
		return zero, err
	}
	data, err := model.EncodeAuditData(map[string]any{"attempt_id": input.Access.AttemptID.String(), "turn_id": input.TurnID.String()})
	if err != nil {
		return zero, err
	}
	if _, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", data, input.AuditAt); err != nil {
		return zero, fmt.Errorf("complete assistant start audit: %w", err)
	}
	return assistantStartOutcome{RemainingInputTokens: recipe.Policy.Budgets.InputTokens - inventory.InputTokens,
		RemainingOutputTokens: recipe.Policy.Budgets.OutputTokens - inventory.OutputTokens, Policy: recipe.Policy, Provider: recipe.Provider,
		Model: recipe.Model, DisplayName: recipe.DisplayName,
		Pseudonymize: recipe.Pseudonymize, Execution: recipe.Execution, ConversationID: run.ConversationID, TurnID: input.TurnID.String()}, nil
}
