// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"context"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// publicFailures presents assistant failures as the attempt's do: a refused
// candidate proof conceals the attempt.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.attempt.unavailable",
	Concealed:   []apperror.Code{"exam.attempt.not_found", "exam.attempt.continuity_invalid"},
}

var storeRules = slices.Concat(
	[]apperror.StoreRule{
		{Kind: apperror.StoreNotFound, Entity: "assistant_recipe", Code: "exam.attempt.assistant_disabled"},
		{Kind: apperror.StoreNotFound, Entity: "assistant_run", Code: "exam.attempt.assistant_turn_not_found"},
		{Kind: apperror.StoreNotFound, Entity: "assistant_call", Code: "exam.attempt.assistant_approval_not_found"},
	},
	apperror.Conflicts("assistant_run", map[string]apperror.Code{
		"assistant_busy":             "exam.attempt.assistant_busy",
		"assistant_budget_exhausted": "exam.attempt.assistant_budget_exhausted",
	}),
	apperror.Conflicts("assistant_call", map[string]apperror.Code{
		"assistant_approval_decided": "exam.attempt.assistant_approval_conflict",
	}),
)

func storeFailure(err error) error { return attempt.AssistantStoreError(err, storeRules...) }

func unavailable() error { return apperror.New("exam.attempt.assistant_unavailable") }

// SendMessageCommand is one candidate message. The same idempotency key and
// text return the turn the first send started.
type SendMessageCommand struct {
	Access         attempt.CandidateAccess
	Text           string
	IdempotencyKey string
}

// SendMessageResult names the turn the message started. The reply arrives
// as realtime events and is recorded in the transcript.
type SendMessageResult struct {
	ConversationID model.AssistantConversationID
	TurnID         model.AssistantTurnID
	Replayed       bool
}

// SendMessage records the candidate's message and starts the assistant's
// reply in the background. It refuses while another turn runs, when the
// Exam does not offer the assistant and when the attempt's budget is spent.
func (s *Service) SendMessage(ctx context.Context, inv invocation.Invocation, command SendMessageCommand) (_ SendMessageResult,
	resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if s == nil {
		return SendMessageResult{}, unavailable()
	}
	if command.Text == "" || len(command.Text) > model.AssistantCandidateMessageMaximumBytes || !utf8.ValidString(command.Text) ||
		model.ContainsControlCharacter(stripLayout(command.Text)) {
		return SendMessageResult{}, apperror.Invalid("exam.attempt.invalid", "text")
	}
	candidate, err := s.attempts.VerifyAssistantCandidate(ctx, inv, command.Access)
	if err != nil {
		return SendMessageResult{}, err
	}
	commandIdempotency, err := idempotency.RequireFor(candidate.Editor.CandidateUserID, store.AssistantMessageOperation,
		command.IdempotencyKey, struct {
			AttemptID string `json:"exam_attempt_id"`
			Text      string `json:"text"`
		}{candidate.Editor.AttemptID.String(), command.Text})
	if err != nil {
		return SendMessageResult{}, err
	}
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: candidate.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    candidate.ClassID.String(),
		Operation:  store.AssistantMessageOperation,
		Value:      map[string]any{"exam_attempt_id": candidate.Editor.AttemptID.String()},
	})
	if err != nil {
		return SendMessageResult{}, err
	}
	token := s.newOwnerToken()
	started, err := s.transcripts.Start(ctx, &store.AssistantMessageStart{Access: candidate.Editor,
		ConversationID: s.newConversationID(), TurnID: s.newTurnID(), OwnerToken: token, Text: command.Text,
		AuditEventID: auditID, AuditAt: model.TimeUTC(s.now())}, commandIdempotency)
	if err != nil {
		return SendMessageResult{}, s.failAudit(ctx, auditID, err)
	}
	result := SendMessageResult{ConversationID: started.ConversationID, TurnID: started.TurnID, Replayed: started.Replayed}
	if started.Replayed {
		return result, nil
	}
	run := newTurnRun(s, inv, command.Access, candidate, started, token)
	if !s.register(run) {
		// The node is stopping: give the turn back so the candidate can ask again.
		run.abandon()
		return result, nil
	}
	go s.converse(run)
	return result, nil
}

func (s *Service) failAudit(ctx context.Context, auditID model.AuditEventID, err error) error {
	mapped := storeFailure(err)
	code := "exam.attempt.unavailable"
	if fault, ok := apperror.As(mapped); ok {
		code = fault.Code()
	}
	if auditErr := s.auditor.Fail(ctx, auditID, code); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}

// TranscriptQuery pages the candidate's own transcript in record order,
// after AfterSequence. It needs only the candidate's session, so the
// transcript stays readable once the attempt is submitted.
type TranscriptQuery struct {
	AttemptID     model.ExamAttemptID
	AfterSequence int64
	Limit         int
}

// ListTranscript returns one page of the candidate's own transcript and the
// inventory it is bound by.
func (s *Service) ListTranscript(ctx context.Context, inv invocation.Invocation, query TranscriptQuery) (_ *store.AssistantTranscriptPage,
	resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if s == nil {
		return nil, unavailable()
	}
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, apperror.New("authentication.token_invalid")
	}
	if !query.AttemptID.IsValid() || query.AfterSequence < 0 || query.Limit < 1 || query.Limit > 200 {
		return nil, apperror.Invalid("exam.attempt.invalid", "transcript_page")
	}
	page, err := s.transcripts.ListForAttempt(ctx, store.AssistantTranscriptQuery{AttemptID: query.AttemptID,
		AfterSequence: query.AfterSequence, Limit: query.Limit, Owner: &store.AssistantTranscriptOwnerAccess{
			AttemptID: query.AttemptID, CandidateUserID: principal.UserID, SessionID: principal.SessionID}})
	if err != nil {
		return nil, storeFailure(err)
	}
	return page, nil
}

// CancelTurnCommand stops the candidate's running turn.
type CancelTurnCommand struct {
	Access attempt.CandidateAccess
	TurnID model.AssistantTurnID
}

// CancelTurn records the turn as canceled, whichever node runs it, and stops
// the model call and any assistant command this node runs for it.
func (s *Service) CancelTurn(ctx context.Context, inv invocation.Invocation, command CancelTurnCommand) (resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if s == nil {
		return unavailable()
	}
	if !command.TurnID.IsValid() {
		return apperror.Invalid("exam.attempt.invalid", "turn_id")
	}
	candidate, err := s.attempts.VerifyAssistantCandidate(ctx, inv, command.Access)
	if err != nil {
		return err
	}
	auditID, err := s.beginControlAudit(ctx, inv, candidate, "assistant_cancel")
	if err != nil {
		return err
	}
	if err = s.transcripts.Cancel(ctx, store.AssistantRunCancel{Editor: candidate.Editor, TurnID: command.TurnID, AuditEventID: auditID,
		AuditAt: model.TimeUTC(s.now())}); err != nil {
		return s.failAudit(ctx, auditID, err)
	}
	if run := s.local(candidate.Editor.AttemptID, command.TurnID); run != nil {
		run.stop(errCanceled)
	}
	s.notifyCommitted(ctx, candidate.Editor.AttemptID)
	s.publishEnded(ctx, targetOf(candidate, command.TurnID), "canceled", model.AssistantUsage{})
	return nil
}

// DecideApprovalCommand answers an approval request for a command or an edit.
type DecideApprovalCommand struct {
	Access attempt.CandidateAccess
	CallID model.AssistantCallID
	Accept bool
}

// DecideApproval records the candidate's answer. The turn waiting for it
// reads it back from the store, so it may run on any node.
func (s *Service) DecideApproval(ctx context.Context, inv invocation.Invocation, command DecideApprovalCommand) (resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if s == nil {
		return unavailable()
	}
	if !command.CallID.IsValid() {
		return apperror.Invalid("exam.attempt.invalid", "call_id")
	}
	candidate, err := s.attempts.VerifyAssistantCandidate(ctx, inv, command.Access)
	if err != nil {
		return err
	}
	auditID, err := s.beginControlAudit(ctx, inv, candidate, "assistant_approval")
	if err != nil {
		return err
	}
	if _, err = s.transcripts.Decide(ctx, store.AssistantCallDecision{Editor: candidate.Editor, CallID: command.CallID,
		Accept: command.Accept, AuditEventID: auditID, AuditAt: model.TimeUTC(s.now())}); err != nil {
		return s.failAudit(ctx, auditID, err)
	}
	s.notifyCommitted(ctx, candidate.Editor.AttemptID)
	return nil
}

// stripLayout leaves the control characters a message may not hold; line
// breaks and tabs are text.
func stripLayout(text string) string {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		if r != '\n' && r != '\r' && r != '\t' {
			out = append(out, r)
		}
	}
	return string(out)
}

func (s *Service) beginControlAudit(ctx context.Context, inv invocation.Invocation, candidate attempt.AssistantCandidate,
	operation string) (model.AuditEventID, error) {
	return s.auditor.Begin(ctx, access.Mutation{Invocation: inv, Action: model.ActionExamSittingParticipate,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: candidate.SittingID.String()}, ScopeType: model.RoleScopeClass,
		ScopeID: candidate.ClassID.String(), Operation: operation, Value: map[string]any{"exam_attempt_id": candidate.Editor.AttemptID.String()}})
}
