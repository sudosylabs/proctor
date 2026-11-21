// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type sessionStartArgs struct {
	Command string `json:"command"`
	Timeout *int64 `json:"timeout_seconds"`
	Wait    *int64 `json:"wait_ms"`
}

type sessionInputArgs struct {
	Session string   `json:"session"`
	Text    string   `json:"text"`
	Keys    []string `json:"keys"`
	Wait    *int64   `json:"wait_ms"`
}

func sessionArguments(raw json.RawMessage, dest any) bool {
	if !utf8.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dest) == nil && decoder.Decode(new(any)) == io.EOF
}

func sessionWait(value *int64, otherwise time.Duration) (time.Duration, bool) {
	if value == nil {
		return otherwise, true
	}
	if *value < environment.MinAssistantWait.Milliseconds() || *value > environment.MaxAssistantWait.Milliseconds() {
		return 0, false
	}
	return time.Duration(*value) * time.Millisecond, true
}

func parseSessionInput(raw json.RawMessage) (sessionInputArgs, bool) {
	var input sessionInputArgs
	if !sessionArguments(raw, &input) || !model.ValidAssistantProcessTag(input.Session) ||
		len(input.Text) > environment.MaxAssistantInputBytes || len(input.Keys) > environment.MaxAssistantKeys {
		return input, false
	}
	for _, r := range input.Text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return input, false
		}
	}
	for _, key := range input.Keys {
		if _, ok := environment.AssistantKeyBytes(key); !ok {
			return input, false
		}
	}
	_, ok := sessionWait(input.Wait, time.Second)
	return input, ok
}

func (a sessionInputArgs) summary(command string) string {
	quotedCommand := strconv.QuoteToGraphic(command)
	return a.Session + " (" + quotedCommand[1:len(quotedCommand)-1] + "): " + strconv.Quote(a.Text) + " + [" + strings.Join(a.Keys, ", ") + "]"
}

func (a sessionInputArgs) data() []byte {
	data := []byte(a.Text)
	for _, key := range a.Keys {
		sequence, _ := environment.AssistantKeyBytes(key)
		data = append(data, sequence...)
	}
	return data
}

func (r *turnRun) startSession(arguments json.RawMessage, call model.AssistantCallID) toolResult {
	var input sessionStartArgs
	if !sessionArguments(arguments, &input) || strings.TrimSpace(input.Command) == "" ||
		len(input.Command) > commandMaximumBytes || strings.ContainsRune(input.Command, 0) {
		return toolError("The arguments do not match the tool.")
	}
	wait, valid := sessionWait(input.Wait, 5*time.Second)
	if !valid || (input.Timeout != nil && (*input.Timeout < 1 || *input.Timeout > 600)) {
		return toolError("The arguments do not match the tool.")
	}
	timeout := int64(environment.DefaultAssistantSessionTimeout / time.Second)
	if input.Timeout != nil {
		timeout = *input.Timeout
	}
	if _, err := r.service.reconcileSessions(r.ctx, r.inv, r.environmentAccess(), r.target.SittingID, r.key.Editor, ""); err != nil {
		return toolError("The assistant sessions cannot be checked now.")
	}
	slot, err := r.service.transcripts.ReserveSession(r.ctx,
		&store.AssistantSessionReservation{Access: r.key, CallID: call, TimeoutSeconds: timeout})
	if apperror.Is(storeFailure(err), "exam.attempt.assistant_session_limit") {
		return toolError("Two sessions are already running. Stop one first.")
	}
	if err != nil {
		return toolError("The session cannot start now.")
	}
	session := model.AssistantSession{Tag: slot.Tag, CallID: call, ConversationID: r.conversation, TurnID: r.key.TurnID,
		CandidateUserID: r.target.CandidateID, State: "running", StartedAt: slot.StartedAt, ReservedMilliseconds: slot.TimeoutSeconds * 1000}
	if sessionExpired(session, r.service.now()) {
		if _, err = r.service.endSession(r.ctx, r.target.SittingID, r.key.Editor, session, "timed_out", nil); err != nil {
			return taggedSessionError(slot.Tag, "The session cannot be settled now.")
		}
		return taggedSessionError(slot.Tag, "The session timed out before it started.")
	}
	if err = r.service.terminals.StartAssistantSession(r.ctx, r.inv, r.environmentAccess(),
		environment.AssistantSessionStart{Tag: model.AttemptEnvironmentProcessTag(slot.Tag), Command: input.Command,
			Timeout: time.Duration(slot.TimeoutSeconds) * time.Second}); err != nil {
		// A lost transport response may hide a start. Keep its reservation
		// until the next check can inspect the process.
		return taggedSessionError(slot.Tag, "The session could not start.")
	}
	event, eventErr := realtime.NewAssistantSessionStartedEvent(r.target.SittingID, r.attemptID(), r.target.CandidateID, slot.Tag, slot.StartedAt)
	r.service.publish(r.ctx, "assistant_session_started", event, eventErr)
	return r.readSession(session, sessionInputArgs{}, wait, false)
}

func (r *turnRun) sendInput(arguments json.RawMessage) toolResult {
	input, ok := parseSessionInput(arguments)
	if !ok {
		return toolError("The arguments do not match the tool.")
	}
	session, err := r.activeSession(input.Session)
	if err != nil || session.State != "running" {
		return taggedSessionError(input.Session, "That session is no longer running.")
	}
	data := input.data()
	if len(data) != 0 {
		if err = r.service.terminals.WriteAssistantInput(r.ctx, r.inv, r.environmentAccess(),
			model.AttemptEnvironmentProcessTag(session.Tag), data); err != nil {
			return taggedSessionError(session.Tag, "The session could not take input.")
		}
	}
	wait, _ := sessionWait(input.Wait, time.Second)
	return r.readSession(session, input, wait, len(data) != 0)
}

func (r *turnRun) activeSession(tag string) (model.AssistantSession, error) {
	rows, err := r.service.reconcileSessions(r.ctx, r.inv, r.environmentAccess(), r.target.SittingID, r.key.Editor, tag)
	if err != nil {
		return model.AssistantSession{}, err
	}
	if len(rows) == 0 || rows[0].Starting || rows[0].AccessEnded {
		return model.AssistantSession{}, store.NewErrNotFound("assistant_session", tag)
	}
	return rows[0], nil
}

func (r *turnRun) readSession(session model.AssistantSession, input sessionInputArgs, wait time.Duration, wrote bool) toolResult {
	tag := model.AttemptEnvironmentProcessTag(session.Tag)
	deadline := session.StartedAt.Add(time.Duration(session.ReservedMilliseconds) * time.Millisecond)
	output, err := r.service.terminals.ReadAssistantSession(r.ctx, r.inv, r.environmentAccess(), tag, session.ReadOffset,
		min(wait, deadline.Sub(r.service.now())))
	// A host-acknowledged write counts even when the output read failed.
	if err == nil || wrote {
		next := output.Next
		if err != nil {
			next = session.ReadOffset
		}
		if checkpointErr := r.service.transcripts.AdvanceSessionRead(r.ctx, r.key, session.Tag, next, wrote); checkpointErr != nil {
			return taggedSessionError(session.Tag, "The session output could not be recorded.")
		}
	}
	if err != nil {
		return taggedSessionError(session.Tag, "The session output could not be read.")
	}
	outcome := ""
	if output.Exit != nil {
		outcome = "exited"
	} else if sessionExpired(session, r.service.now()) {
		status, checkErr := r.service.terminals.CheckAssistantSession(r.ctx, r.inv, r.environmentAccess(), session)
		if checkErr != nil {
			return taggedSessionError(session.Tag, "The session deadline could not be enforced.")
		}
		outcome, output.Exit = status.Outcome, status.Exit
	}
	if outcome != "" {
		if _, err = r.service.endSession(r.ctx, r.target.SittingID, r.key.Editor, session, outcome, output.Exit); err != nil {
			return taggedSessionError(session.Tag, "The session could not be settled.")
		}
	}
	return sessionResult(session, input, output, outcome)
}

func (r *turnRun) stopSession(arguments json.RawMessage) toolResult {
	var input struct {
		Session string `json:"session"`
	}
	if !sessionArguments(arguments, &input) || !model.ValidAssistantProcessTag(input.Session) {
		return toolError("The arguments do not match the tool.")
	}
	session, err := r.activeSession(input.Session)
	if err != nil {
		return taggedSessionError(input.Session, "That session is no longer running.")
	}
	if session.State == "ended" {
		return sessionResult(session, sessionInputArgs{}, environment.AssistantSessionOutput{}, session.Outcome)
	}
	output, err := r.service.terminals.StopAssistantSession(r.ctx, r.inv, r.environmentAccess(),
		model.AttemptEnvironmentProcessTag(session.Tag), session.ReadOffset)
	if err != nil {
		return taggedSessionError(session.Tag, "The session could not be stopped.")
	}
	if _, err = r.service.endSession(r.ctx, r.target.SittingID, r.key.Editor, session, "stopped", output.Exit); err != nil {
		return taggedSessionError(session.Tag, "The session could not be settled.")
	}
	return sessionResult(session, sessionInputArgs{}, output, "stopped")
}

func sessionExpired(session model.AssistantSession, now time.Time) bool {
	return !now.Before(session.StartedAt.Add(time.Duration(session.ReservedMilliseconds) * time.Millisecond))
}

func (s *Service) reconcileSessions(ctx context.Context, inv invocation.Invocation, access environment.CandidateAccess,
	sittingID model.ExamSittingID, editor store.ExamAttemptWorkspaceEditorAccess, tag string) ([]model.AssistantSession, error) {
	rows, err := s.transcripts.ListSessions(ctx, store.AssistantSessionQuery{Tag: tag,
		Owner: sessionOwner(editor)})
	if err != nil {
		return nil, err
	}
	var joined error
	for i := range rows {
		session := &rows[i]
		if session.State != "running" {
			continue
		}
		status, checkErr := s.terminals.CheckAssistantSession(ctx, inv, access, *session)
		if checkErr != nil {
			if errors.Is(checkErr, environment.ErrEnvironmentNotRunning) || errors.Is(checkErr, environment.ErrNoEnvironment) {
				continue
			}
			joined = errors.Join(joined, checkErr)
			continue
		}
		if status.Outcome != "" {
			record, endErr := s.endSession(ctx, sittingID, editor, *session, status.Outcome, status.Exit)
			if endErr != nil {
				joined = errors.Join(joined, endErr)
				continue
			}
			session.State, session.Outcome, session.EndedAt, session.Starting = "ended", record.Call.Outcome, record.CreatedAt, false
		}
	}
	return rows, joined
}

func (s *Service) endSession(ctx context.Context, sittingID model.ExamSittingID, editor store.ExamAttemptWorkspaceEditorAccess,
	session model.AssistantSession,
	outcome string, exit *environment.ProcessExit) (*model.AssistantRecord, error) {
	end := store.AssistantSessionEnd{Editor: editor, Tag: session.Tag, Outcome: outcome,
		DurationMilliseconds: max(0, s.now().Sub(session.StartedAt).Milliseconds())}
	if exit != nil {
		end.Signal = exit.Signal
		if exit.Signal == "" {
			end.ExitCode = &exit.Code
		}
	}
	result, err := s.transcripts.EndSession(ctx, &end)
	if err != nil {
		return nil, err
	}
	if result.Changed {
		s.publishSessionEnded(ctx, sittingID, editor.AttemptID, editor.CandidateUserID, result.Record)
	}
	return result.Record, nil
}

func (s *Service) publishSessionEnded(ctx context.Context, sittingID model.ExamSittingID, attemptID model.ExamAttemptID,
	candidate model.UserID, record *model.AssistantRecord) {
	event, err := realtime.NewAssistantSessionEndedEvent(sittingID, attemptID, candidate, record.Call.ProcessTag, record.Call.Outcome, record.CreatedAt)
	s.publish(ctx, "assistant_session_ended", event, err)
}

func taggedSessionError(tag, message string) toolResult {
	result := toolError(message)
	result.processTag = tag
	return result
}

func sessionResult(session model.AssistantSession, input sessionInputArgs, output environment.AssistantSessionOutput, outcome string) toolResult {
	text := string(output.Data)
	if output.AltScreen {
		text = environment.CleanAssistantScreen(strings.Join(output.Screen, "\n"))
	}
	bytesSeen := uint64(len(text))
	if output.Next >= session.ReadOffset && !output.AltScreen {
		bytesSeen = output.Next - session.ReadOffset
	}
	cutForModel := utf8.RuneCountInString(text) > 10000 || output.Dropped
	preview := headTail(text, 10000)
	if cutForModel {
		preview += "\n[output cut, " + strconv.FormatUint(bytesSeen, 10) + " bytes total]"
	}
	var heading string
	switch {
	case outcome == "":
		heading = "session: " + session.Tag + "\n"
	case output.Exit != nil:
		heading = outcome + ": " + exitLine(output.Exit)
	default:
		heading = "The session " + outcome + ".\n"
	}
	if output.AltScreen {
		heading += "Full-screen program. Current screen follows.\n"
	}
	document := struct {
		Text   string   `json:"text,omitempty"`
		Keys   []string `json:"keys,omitempty"`
		Output string   `json:"output"`
		Bytes  uint64   `json:"output_bytes"`
	}{Text: input.Text, Keys: input.Keys, Output: text, Bytes: bytesSeen}
	encoded, _ := json.Marshal(document)
	truncated := cutForModel
	for len(encoded) > model.AssistantCallResultMaximumBytes {
		document.Output = truncateUTF8(document.Output, len(document.Output)/2)
		truncated = true
		encoded, _ = json.Marshal(document)
	}
	result := toolResult{forModel: heading + preview, record: string(encoded), outcome: "ok", processTag: session.Tag, truncated: truncated}
	if output.Exit != nil {
		result.signal = output.Exit.Signal
		if output.Exit.Signal == "" {
			result.exitCode = &output.Exit.Code
		}
	}
	if outcome == "timed_out" {
		result.isError, result.outcome = true, "error"
	}
	return result
}
