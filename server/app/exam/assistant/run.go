// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The causes that stop a turn from outside its loop.
var (
	// errFenceLost: a seal, the end of the participation or a newer owner
	// took the run fence. The store has, or will, record the end.
	errFenceLost = errors.New("assistant run fence lost")
	// errCanceled: the candidate stopped the turn, which the store recorded.
	errCanceled = errors.New("assistant turn canceled")
)

// replyMaximumBytes bounds the reply the assistant keeps for one turn; the
// turn record allows more, and the rest is the transcript's headroom.
const replyMaximumBytes = 64 << 10

// liveDeltaBytes is how much streamed text is gathered into one delta.
const liveDeltaBytes = 512

// turnRun is one turn in progress on this node.
type turnRun struct {
	service      *Service
	inv          invocation.Invocation
	access       attempt.CandidateAccess
	candidate    attempt.AssistantCandidate
	recipe       store.AssistantRecipe
	conversation model.AssistantConversationID
	key          store.AssistantRunAccess
	target       realtime.AssistantTarget

	ctx     context.Context
	cancel  context.CancelCauseFunc
	decided chan struct{}
	wake    chan struct{}

	deltas          int64
	calls           int
	readBytes       int
	publicationErr  error
	remainingInput  int64
	remainingOutput int64
}

func newTurnRun(s *Service, inv invocation.Invocation, access attempt.CandidateAccess, candidate attempt.AssistantCandidate,
	started *store.AssistantRunStart, token string) *turnRun {
	ctx, cancel := context.WithCancelCause(s.base)
	return &turnRun{service: s, inv: inv, access: access, candidate: candidate, recipe: started.Recipe,
		conversation:   started.ConversationID,
		remainingInput: started.RemainingInputTokens, remainingOutput: started.RemainingOutputTokens,
		key:    store.AssistantRunAccess{Editor: candidate.Editor, TurnID: started.TurnID, OwnerToken: token},
		target: targetOf(candidate, started.TurnID), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), decided: make(chan struct{}, 1)}
}

func targetOf(candidate attempt.AssistantCandidate, turn model.AssistantTurnID) realtime.AssistantTarget {
	return realtime.AssistantTarget{ExamID: candidate.ExamID, SittingID: candidate.SittingID, AttemptID: candidate.Editor.AttemptID,
		CandidateID: candidate.Editor.CandidateUserID, SessionID: candidate.Editor.SessionID, TurnID: turn}
}

func (r *turnRun) attemptID() model.ExamAttemptID { return r.key.Editor.AttemptID }

func (r *turnRun) stop(cause error) { r.cancel(cause) }

func (r *turnRun) notifyDecision() {
	select {
	case r.decided <- struct{}{}:
	default:
	}
}

// abandon ends a turn that never ran, so the attempt is not left busy until
// its lease runs out.
func (r *turnRun) abandon() {
	r.cancel(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = r.service.transcripts.AppendTurn(ctx, &store.AssistantTurnAppend{Access: r.key, Gap: true, Turn: model.AssistantTurn{
		ConversationID: r.conversation, TurnID: r.key.TurnID, Role: "assistant", Model: r.recipe.Model, Final: true,
		StopReason: "canceled"}})
}

// turnEnd is how a turn ends. A failure is a provider's, recorded as a gap.
type turnEnd struct {
	reason  string
	text    string
	failure *ModelError
}

// converse is the turn's background loop. Whatever happens, it records the
// end of the turn unless something else already did.
func (s *Service) converse(run *turnRun) {
	defer s.forget(run)
	defer run.cancel(nil)
	go run.keepAlive()
	event, err := realtime.NewAssistantTurnStartedEvent(run.target, run.conversation, s.now())
	s.publish(run.ctx, "assistant_turn_started", event, err)
	end := run.reply()
	run.finish(end)
}

// keepAlive renews the run fence. Losing it, or failing to renew it before
// the lease could run out, stops the turn.
func (r *turnRun) keepAlive() {
	ticker := time.NewTicker(r.service.limits.RenewEvery)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
		err := r.service.transcripts.Renew(r.ctx, r.key)
		switch {
		case err == nil:
			failures = 0
			r.notifyDecision()
		case r.ctx.Err() != nil:
			return
		case store.IsNotFound(err) || isRefusal(err):
			// A seal or the end of the participation refuses the candidate's proof.
			r.stop(errFenceLost)
			return
		default:
			failures++
			if failures >= 2 {
				r.stop(errFenceLost)
				return
			}
		}
	}
}

// reply runs model calls, and tool calls between them, until the model
// stops, a budget or bound is reached, or the turn is stopped.
func (r *turnRun) reply() turnEnd {
	messages, err := r.history()
	if err != nil {
		return r.stopped(err)
	}
	system := systemPrompt(r.recipe)
	tools := r.tools()
	for {
		response, end := r.call(system, messages, tools)
		if end != nil {
			return *end
		}
		if response.stop == "tool_use" && len(response.calls) > 0 && len(tools) > 0 {
			if r.calls >= r.service.limits.MaxCallsPerTurn {
				return turnEnd{reason: "budget_exhausted"}
			}
			messages = append(messages, ModelMessage{Role: "assistant", Text: response.text, Calls: response.calls, Replay: response.replay})
			for _, call := range response.calls {
				if r.calls >= r.service.limits.MaxCallsPerTurn {
					return turnEnd{reason: "budget_exhausted"}
				}
				result, isError, end := r.runTool(call)
				if end != nil {
					return *end
				}
				messages = append(messages, ModelMessage{Role: "tool", CallID: call.ID, Text: result, IsError: isError})
			}
			continue
		}
		text := response.text
		if checked(r.recipe.Policy.Level) {
			var end *turnEnd
			text, end = r.checkReply(system, messages, text)
			if end != nil {
				return *end
			}
			r.publishText(text)
			if r.publicationErr != nil {
				return r.stopped(r.publicationErr)
			}
		}
		return turnEnd{reason: response.stop, text: text}
	}
}

// stopped is the end of a turn whose store call failed: an exhausted budget
// ends it normally, anything else means the fence is gone.
func (r *turnRun) stopped(err error) turnEnd {
	var conflict *store.ErrConflict
	if errors.As(err, &conflict) && conflict.Constraint == "assistant_budget_exhausted" {
		return turnEnd{reason: "budget_exhausted"}
	}
	if r.ctx.Err() == nil {
		r.stop(errFenceLost)
	}
	return turnEnd{reason: "access_lost"}
}

type modelResponse struct {
	replay []json.RawMessage
	text   string
	calls  []ModelToolCall
	stop   string
}

// call makes one model call. The budget is reserved first, so a turn never
// spends what the attempt does not have; the reservation is then settled to
// the provider's usage, or kept whole when that usage was lost.
func (r *turnRun) call(system string, messages []ModelMessage, tools []ModelTool) (modelResponse, *turnEnd) {
	s := r.service
	reserveIn := estimateInputTokens(system, messages, tools)
	reserveOut := min(s.limits.MaxOutputTokens, r.remainingOutput)
	if reserveOut < 1 || reserveIn > r.remainingInput {
		end := turnEnd{reason: "budget_exhausted"}
		return modelResponse{}, &end
	}
	if err := s.transcripts.ReserveModel(r.ctx, &store.AssistantModelReservation{Access: r.key, InputTokens: reserveIn,
		OutputTokens: reserveOut}); err != nil {
		end := r.stopped(err)
		return modelResponse{}, &end
	}
	response, usage, failure := r.stream(ModelRequest{Provider: r.recipe.Provider, Model: r.recipe.Model, System: system,
		Messages: messages, Tools: tools, MaxOutputTokens: reserveOut})
	if usage == nil {
		usage = &ModelUsage{Input: reserveIn, Output: reserveOut, Estimated: true}
	}
	settled := clampUsage(*usage, reserveIn, reserveOut)
	r.remainingInput -= settled.InputTotal()
	r.remainingOutput -= settled.Output
	// The settlement outlives a stopped turn: a canceled call still spent.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err := s.transcripts.CompleteModel(ctx, &store.AssistantModelCompletion{Access: r.key, Usage: settled})
	cancel()
	if r.publicationErr != nil {
		end := r.stopped(r.publicationErr)
		return modelResponse{}, &end
	}
	if r.ctx.Err() != nil {
		end := turnEnd{reason: "canceled"}
		return modelResponse{}, &end
	}
	if err != nil {
		end := r.stopped(err)
		return modelResponse{}, &end
	}
	if failure != nil {
		if checked(r.recipe.Policy.Level) {
			response.text = ""
		}
		return modelResponse{}, &turnEnd{reason: string(failure.Class), text: response.text, failure: failure}
	}
	switch response.stop {
	case "end_turn", "tool_use", "max_tokens", "stop_sequence", "refusal":
	default:
		response.stop = "end_turn"
	}
	return response, nil
}

// stream reads one model stream to its end. Text goes live only at levels
// whose replies are not checked first.
func (r *turnRun) stream(request ModelRequest) (modelResponse, *ModelUsage, *ModelError) {
	var response modelResponse
	stream, err := r.service.gateway.Stream(r.ctx, request)
	if err != nil {
		return response, nil, modelFailure(err)
	}
	defer func() { _ = stream.Close() }()
	var usage *ModelUsage
	var failure *ModelError
	live := !checked(r.recipe.Policy.Level)
	var pending []byte
	firstText := true
	text := make([]byte, 0, 1024)
	type slot struct {
		name string
		args []byte
	}
	slots := map[string]*slot{}
	var order []string
	for {
		event, nextErr := stream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			failure = modelFailure(nextErr)
			break
		}
		switch event.Kind {
		case ModelTextDelta:
			room := replyMaximumBytes - len(text)
			chunk := truncateUTF8(event.Text, max(room, 0))
			text = append(text, chunk...)
			if live && chunk != "" {
				if firstText && r.deltas > 0 {
					pending = append(pending, '\n', '\n')
				}
				firstText = false
				pending = append(pending, chunk...)
				if len(pending) >= liveDeltaBytes {
					r.publishDelta(string(pending))
					pending = pending[:0]
				}
			}
		case ModelToolCallStart:
			if _, seen := slots[event.ID]; !seen && event.ID != "" && len(order) < r.service.limits.MaxCallsPerTurn {
				slots[event.ID] = &slot{name: event.Name}
				order = append(order, event.ID)
			}
		case ModelToolCallArgsDelta:
			if current := slots[event.ID]; current != nil && len(current.args)+len(event.Text) <= 64<<10 {
				current.args = append(current.args, event.Text...)
			}
		case ModelUsageEvent:
			if event.Usage.Valid() {
				value := event.Usage
				usage = &value
			}
		case ModelReplayEvent:
			response.replay = event.Replay
		case ModelStop:
			response.stop = event.Reason
		case ModelErrorEvent:
			if event.Failure != nil {
				failure = event.Failure
			} else {
				failure = &ModelError{Class: ModelUnavailable}
			}
		}
	}
	if live && len(pending) > 0 {
		r.publishDelta(string(pending))
	}
	response.text = string(text)
	for _, id := range order {
		current := slots[id]
		response.calls = append(response.calls, ModelToolCall{ID: id, Name: current.name, Arguments: current.args})
	}
	return response, usage, failure
}

func modelFailure(err error) *ModelError {
	var failure *ModelError
	if errors.As(err, &failure) && failure != nil {
		return failure
	}
	return &ModelError{Class: ModelUnavailable}
}

// clampUsage keeps a settlement inside its reservation. A provider that
// reports more than was reserved is charged the reservation, as an estimate.
func clampUsage(usage ModelUsage, input, output int64) model.AssistantUsage {
	settled := model.AssistantUsage{Input: usage.Input, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite,
		Output: usage.Output, Estimated: usage.Estimated}
	if !usage.Valid() {
		return model.AssistantUsage{Input: input, Output: output, Estimated: true}
	}
	if settled.InputTotal() > input {
		settled = model.AssistantUsage{Input: input, Output: settled.Output, Estimated: true}
	}
	if settled.Output > output {
		settled.Output, settled.Estimated = output, true
	}
	return settled
}

// estimateInputTokens overestimates on purpose: tokens rarely outnumber
// bytes, and a reservation too small would be refused at settlement.
func estimateInputTokens(system string, messages []ModelMessage, tools []ModelTool) int64 {
	encoded, _ := json.Marshal(ModelRequest{System: system, Messages: messages, Tools: tools})
	// Cover provider envelopes and JSON escaping, including cached input.
	return int64(len(encoded))*4 + 4096
}

func (r *turnRun) publishDelta(text string) {
	for text != "" {
		chunk := truncateUTF8(text, realtime.AssistantDeltaMaximumBytes)
		if chunk == "" {
			return
		}
		if r.publicationErr != nil {
			return
		}
		_, appendErr := r.service.transcripts.AppendTurn(r.ctx, &store.AssistantTurnAppend{Access: r.key,
			Turn: model.AssistantTurn{ConversationID: r.conversation, TurnID: r.key.TurnID, Role: "assistant", Model: r.recipe.Model, Text: chunk}})
		if appendErr != nil {
			r.publicationErr = appendErr
			r.stop(errFenceLost)
			return
		}

		event, err := realtime.NewAssistantTextDeltaEvent(r.target, r.deltas, chunk)
		r.service.publish(r.ctx, "assistant_text_delta", event, err)
		r.deltas++
		text = text[len(chunk):]
	}
}

// publishText delivers a checked reply in the chunks a live one would have.
func (r *turnRun) publishText(text string) {
	for text != "" {
		chunk := truncateUTF8(text, liveDeltaBytes*4)
		if chunk == "" {
			return
		}
		r.publishDelta(chunk)
		text = text[len(chunk):]
	}
}

// finish records the turn's end. When the fence is gone the store, or the
// node that took it, records the end instead.
func (r *turnRun) finish(end turnEnd) {
	s := r.service
	cause := context.Cause(r.ctx)
	if errors.Is(cause, errCanceled) {
		return
	}
	if errors.Is(cause, errFenceLost) {
		end = turnEnd{reason: "access_lost"}
	} else if r.ctx.Err() != nil && end.reason != "access_lost" {
		// The node is stopping.
		end = turnEnd{reason: "canceled", text: end.text}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := s.transcripts.AppendTurn(ctx, &store.AssistantTurnAppend{Access: r.key, Gap: end.failure != nil ||
		end.reason == "access_lost", Turn: model.AssistantTurn{ConversationID: r.conversation, TurnID: r.key.TurnID,
		Role: "assistant", Model: r.recipe.Model, Final: true, StopReason: end.reason}})
	if err != nil {
		// A seal or a cancel recorded the end first.
		return
	}
	if end.failure != nil {
		event, eventErr := realtime.NewAssistantUnavailableEvent(r.target, string(end.failure.Class), end.failure.RetryAfter, s.now())
		s.publish(ctx, "assistant_unavailable", event, eventErr)
	}
	s.publishEnded(ctx, r.target, end.reason, record.Turn.Usage)
}

func (s *Service) publishEnded(ctx context.Context, target realtime.AssistantTarget, reason string, usage model.AssistantUsage) {
	event, err := realtime.NewAssistantTurnEndedEvent(target, reason, usage, s.now())
	s.publish(ctx, "assistant_turn_ended", event, err)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func isRefusal(err error) bool {
	var conflict *store.ErrConflict
	var invalid *store.ErrInvalidInput
	return errors.As(err, &conflict) || errors.As(err, &invalid)
}
