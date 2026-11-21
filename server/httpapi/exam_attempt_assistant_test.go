// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// scriptedModel answers each model call with the next script, and records
// what it was asked.
type scriptedModel struct {
	mu       sync.Mutex
	scripts  [][]assistant.ModelEvent
	requests []assistant.ModelRequest
}

func (m *scriptedModel) Stream(_ context.Context, request assistant.ModelRequest) (assistant.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	if len(m.scripts) == 0 {
		return nil, &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	events := m.scripts[0]
	m.scripts = m.scripts[1:]
	return &scriptedStream{events: events}, nil
}

func (m *scriptedModel) asked() []assistant.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]assistant.ModelRequest(nil), m.requests...)
}

type scriptedStream struct{ events []assistant.ModelEvent }

func (s *scriptedStream) Next() (assistant.ModelEvent, error) {
	if len(s.events) == 0 {
		return assistant.ModelEvent{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *scriptedStream) Close() error { return nil }

func reply(text string) []assistant.ModelEvent {
	return []assistant.ModelEvent{{Kind: assistant.ModelTextDelta, Text: text},
		{Kind: assistant.ModelUsageEvent, Usage: assistant.ModelUsage{Input: 40, Output: 12}},
		{Kind: assistant.ModelStop, Reason: "end_turn"}}
}

// transcriptLedger holds one Attempt's transcript as a consistent store
// would, with one turn at a time and the recipe admission froze.
type transcriptLedger struct {
	store.AssistantTranscriptStore
	f        *attemptFixture
	mu       sync.Mutex
	recipe   store.AssistantRecipe
	turn     model.AssistantTurnID
	token    string
	active   bool
	sealed   bool
	keys     map[string]*store.AssistantRunStart
	record   []model.AssistantRecord
	cancel   []store.AssistantRunCancel
	sessions []model.AssistantSession
}

func newTranscriptLedger(f *attemptFixture, level model.AssistantLevel) *transcriptLedger {
	policy := model.DefaultAssistantPolicy()
	policy.Enabled, policy.Level, policy.ProfileID = true, level, model.NewAssistantProfileID()
	policy.Instructions, policy.ConceptsToAvoid = "Do not explain recursion.", []string{"memoization"}
	policy.Budgets = model.AssistantBudgets{Turns: 10, InputTokens: 1_000_000, OutputTokens: 100_000, CommandTimeoutSeconds: 60}
	return &transcriptLedger{f: f, keys: map[string]*store.AssistantRunStart{}, recipe: store.AssistantRecipe{Policy: policy,
		Provider: "campus", Model: "teaching-model", DisplayName: "Campus tutor", Pseudonymize: true}}
}

func (l *transcriptLedger) append(record model.AssistantRecord) *model.AssistantRecord {
	record.Sequence, record.CreatedAt = int64(len(l.record)+1), time.Now().UTC()
	l.record = append(l.record, record)
	return &l.record[len(l.record)-1]
}

func (l *transcriptLedger) Start(_ context.Context, input *store.AssistantMessageStart, command *store.CommandIdempotency) (*store.AssistantRunStart, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if input.Access != l.f.editorAccess() {
		return nil, store.NewErrNotFound("exam_attempt_access", input.Access.AttemptID.String())
	}
	if started := l.keys[string(command.KeyDigest[:])]; started != nil {
		replayed := *started
		replayed.Replayed = true
		return &replayed, nil
	}
	if l.active {
		return nil, store.NewErrConflict("assistant_run", "assistant_busy", nil)
	}
	l.turn, l.token, l.active = input.TurnID, input.OwnerToken, true
	l.append(model.AssistantRecord{Turn: &model.AssistantTurn{ConversationID: input.ConversationID, TurnID: input.TurnID,
		Role: "candidate", Text: input.Text, Final: true}})
	started := &store.AssistantRunStart{RemainingInputTokens: 1000000, RemainingOutputTokens: 10000, Recipe: l.recipe, ConversationID: input.ConversationID, TurnID: input.TurnID}
	l.keys[string(command.KeyDigest[:])] = started
	return started, nil
}

func (l *transcriptLedger) owns(access store.AssistantRunAccess) error {
	if !l.active || access.TurnID != l.turn || access.OwnerToken != l.token {
		return store.NewErrNotFound("assistant_run", access.TurnID.String())
	}
	return nil
}

func (l *transcriptLedger) Renew(_ context.Context, access store.AssistantRunAccess) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owns(access)
}

func (l *transcriptLedger) ReserveModel(_ context.Context, input *store.AssistantModelReservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owns(input.Access)
}

func (l *transcriptLedger) CompleteModel(_ context.Context, input *store.AssistantModelCompletion) (model.AssistantUsage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return input.Usage, l.owns(input.Access)
}

func (l *transcriptLedger) AppendTurn(_ context.Context, input *store.AssistantTurnAppend) (*model.AssistantRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.owns(input.Access); err != nil {
		return nil, err
	}
	turn := input.Turn
	if turn.Final {
		turn.Usage = model.AssistantUsage{Input: 40, Output: 12}
		l.active = false
	}
	return l.append(model.AssistantRecord{Turn: &turn, Gap: input.Gap}), nil
}

func (l *transcriptLedger) AppendCall(_ context.Context, input *store.AssistantCallAppend) (*model.AssistantRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.owns(input.Access); err != nil {
		return nil, err
	}
	call := input.Call
	return l.append(model.AssistantRecord{Call: &call}), nil
}

func (l *transcriptLedger) CompleteCall(ctx context.Context, input *store.AssistantCallAppend) (*model.AssistantRecord, error) {
	return l.AppendCall(ctx, input)
}

func (l *transcriptLedger) Cancel(_ context.Context, input store.AssistantRunCancel) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active || input.TurnID != l.turn {
		return store.NewErrNotFound("assistant_run", input.TurnID.String())
	}
	l.cancel = append(l.cancel, input)
	l.active = false
	return nil
}

func (l *transcriptLedger) ListForAttempt(_ context.Context, query store.AssistantTranscriptQuery) (*store.AssistantTranscriptPage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if query.Owner == nil || query.Owner.CandidateUserID != l.f.candidate.UserID || query.AttemptID != l.f.attempt.ID ||
		(query.Owner.SealedOnly && !l.sealed) {
		return nil, store.NewErrNotFound("assistant_transcript", query.AttemptID.String())
	}
	inventory := model.NewAssistantInventory()
	for _, record := range l.record {
		inventory, _ = inventory.Append(record)
	}
	page := &store.AssistantTranscriptPage{Inventory: inventory}
	for _, record := range l.record {
		if record.Sequence <= query.AfterSequence {
			continue
		}
		if len(page.Records) == query.Limit {
			page.NextSequence = page.Records[len(page.Records)-1].Sequence
			break
		}
		page.Records = append(page.Records, record)
	}
	return page, nil
}

func (l *transcriptLedger) ListSessions(_ context.Context, query store.AssistantSessionQuery) ([]model.AssistantSession, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if query.Owner.AttemptID != l.f.attempt.ID || query.Owner.CandidateUserID != l.f.candidate.UserID {
		return nil, store.NewErrNotFound("assistant_session", query.Tag)
	}
	var sessions []model.AssistantSession
	for _, session := range l.sessions {
		if query.Tag == "" || query.Tag == session.Tag {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

func (l *transcriptLedger) finalTurn() *model.AssistantTurn {
	l.mu.Lock()
	defer l.mu.Unlock()
	for index := len(l.record) - 1; index >= 0; index-- {
		if turn := l.record[index].Turn; turn != nil && turn.Final && turn.Role == "assistant" {
			return turn
		}
	}
	return nil
}

func newAssistantService(t *testing.T, f *attemptFixture, transcripts *transcriptLedger, gateway assistant.ModelGateway, publishers ...realtime.Publisher) *assistant.Service {
	t.Helper()
	publisher := realtime.Publisher(f.publisher)
	if len(publishers) > 0 {
		publisher = publishers[0]
	}
	service, err := assistant.New(assistant.Dependencies{Transcripts: transcripts, Attempts: f.service, Gateway: gateway,
		Auditor: f.access.Auditor, Publisher: publisher, Now: time.Now,
		NewConversationID: model.NewAssistantConversationID, NewTurnID: model.NewAssistantTurnID,
		NewCallID: model.NewAssistantCallID, NewOwnerToken: model.NewCredentialToken})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Close(context.Background())) })
	return service
}

func sendAssistantMessage(t *testing.T, f *attemptFixture, api http.Handler, key, text string) *httptest.ResponseRecorder {
	t.Helper()
	request := f.candidateRequest(t, http.MethodPost, f.candidatePath("/assistant/messages"))
	request.Body = io.NopCloser(strings.NewReader(`{"text":` + string(mustJSON(t, text)) + `}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func waitForAssistantTurnEnded(t *testing.T, publisher *realtimetest.Publisher) {
	t.Helper()
	// The worker commits the final transcript record before publishing its events.
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(publisher.Calls(), func(call realtimetest.Call) bool {
			return call.Method == realtimetest.Publish && call.Event.Name == "exam_attempt_assistant_turn_ended"
		})
	}, 5*time.Second, 10*time.Millisecond, "assistant turn-ended event was not published")
}

func TestCandidateAssistantTutorTurnStreamsAndRecordsAReply(t *testing.T) {
	f := newAttemptFixture(t)
	transcripts := newTranscriptLedger(f, model.AssistantLevelTutor)
	gateway := &scriptedModel{scripts: [][]assistant.ModelEvent{reply("Check the loop's exit condition.")}}
	api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, transcripts, gateway)))

	response := sendAssistantMessage(t, f, api, "tutor-first", "Why does my loop never end?")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var started assistantTurnStartedResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &started))
	require.False(t, started.Replayed)

	waitForAssistantTurnEnded(t, f.publisher)
	turn := transcripts.finalTurn()
	require.NotNil(t, turn)
	require.Equal(t, "end_turn", turn.StopReason)
	asked := gateway.asked()
	require.Len(t, asked, 1)
	require.Empty(t, asked[0].Tools, "Tutor offers the model no tools")
	require.Equal(t, "campus", asked[0].Provider)
	require.Contains(t, asked[0].System, "Do not explain recursion.")
	require.Contains(t, asked[0].System, "memoization")
	require.Equal(t, []assistant.ModelMessage{{Role: "user", Text: "Why does my loop never end?"}}, asked[0].Messages)
	for _, request := range asked {
		require.NotContains(t, request.System, "candidate@", "the provider never learns who the candidate is")
		require.NotContains(t, request.System, f.candidate.UserID.String())
	}

	names := map[string]int{}
	for _, call := range f.publisher.Calls() {
		if call.Method == realtimetest.Publish {
			names[call.Event.Name]++
			require.Equal(t, f.candidate.UserID, call.Event.UserID, "assistant events go to the candidate alone")
			require.Equal(t, f.candidate.SessionID, call.Event.SessionID)
		}
	}
	require.Equal(t, 1, names["exam_attempt_assistant_turn_started"])
	require.Positive(t, names["exam_attempt_assistant_text_delta"])
	require.Equal(t, 1, names["exam_attempt_assistant_turn_ended"])

	replayed := sendAssistantMessage(t, f, api, "tutor-first", "Why does my loop never end?")
	require.Equal(t, http.StatusAccepted, replayed.Code, replayed.Body.String())
	var again assistantTurnStartedResponse
	require.NoError(t, json.Unmarshal(replayed.Body.Bytes(), &again))
	require.True(t, again.Replayed)
	require.Equal(t, started.TurnID, again.TurnID)
	require.Len(t, gateway.asked(), 1, "a replay starts no model call")

	list := f.candidateRequest(t, http.MethodGet, f.candidatePath("/assistant/turns?limit=2"))
	listed := httptest.NewRecorder()
	api.ServeHTTP(listed, list)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var page assistantTranscriptResponse
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.Equal(t, "candidate", page.Items[0].Turn.Role)
	require.Equal(t, "Check the loop's exit condition.", page.Items[1].Turn.Text)
	require.NotEmpty(t, page.NextCursor)
	require.Equal(t, int64(3), page.Inventory.Records)

	export := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, f.candidateRequest(t, http.MethodGet, f.candidatePath("/assistant/export")))
		return response
	}
	require.Equal(t, http.StatusNotFound, export().Code)
	transcripts.mu.Lock()
	transcripts.sealed = true
	transcripts.mu.Unlock()
	download := export()
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	require.Contains(t, download.Header().Get("Content-Disposition"), "attachment;")
	require.Contains(t, download.Header().Get("Cache-Control"), "no-store")
	var document assistantTranscriptExportResponse
	require.NoError(t, json.Unmarshal(download.Body.Bytes(), &document))
	require.Equal(t, f.attempt.ID.String(), document.ExamAttemptID)
	require.Len(t, document.Records, 3)
	require.Equal(t, page.Inventory.Digest, document.Inventory.Digest)
}

func TestCandidateAssistantRefusesASecondTurnWhileOneRuns(t *testing.T) {
	f := newAttemptFixture(t)
	transcripts := newTranscriptLedger(f, model.AssistantLevelTutor)
	transcripts.active = true
	api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, transcripts, &scriptedModel{})))

	response := sendAssistantMessage(t, f, api, "busy", "Another question")
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "exam.attempt.assistant_busy")
}

func TestCandidateAssistantRecordsAProviderFailureAsAGap(t *testing.T) {
	f := newAttemptFixture(t)
	transcripts := newTranscriptLedger(f, model.AssistantLevelTutor)
	failing := &scriptedModel{scripts: [][]assistant.ModelEvent{{
		{Kind: assistant.ModelTextDelta, Text: "Unchecked private solution recursion"},
		{Kind: assistant.ModelErrorEvent, Failure: &assistant.ModelError{Class: assistant.ModelRateLimited, RetryAfter: 20 * time.Second}},
		{Kind: assistant.ModelStop, Reason: "error"},
	}}}
	api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, transcripts, failing)))

	response := sendAssistantMessage(t, f, api, "failing", "Hello?")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	waitForAssistantTurnEnded(t, f.publisher)
	turn := transcripts.finalTurn()
	require.NotNil(t, turn)
	require.Equal(t, "rate_limited", turn.StopReason)
	transcripts.mu.Lock()
	last := transcripts.record[len(transcripts.record)-1]
	transcripts.mu.Unlock()
	require.True(t, last.Gap)
	transcripts.mu.Lock()
	for _, record := range transcripts.record {
		if record.Turn != nil {
			require.NotContains(t, record.Turn.Text, "Unchecked private solution")
		}
	}
	transcripts.mu.Unlock()
	unavailable := 0
	for _, call := range f.publisher.Calls() {
		if call.Method == realtimetest.Publish && call.Event.Name == "exam_attempt_assistant_unavailable" {
			unavailable++
			require.Contains(t, string(call.Event.Data), `"retry_after_seconds":20`)
		}
	}
	require.Equal(t, 1, unavailable)
}

func TestCandidateAssistantReplacesAReplyThatFailsTheOutputCheckTwice(t *testing.T) {
	f := newAttemptFixture(t)
	transcripts := newTranscriptLedger(f, model.AssistantLevelTutor)
	solution := "```\n" + strings.Repeat("x := solve()\n", 12) + "```"
	gateway := &scriptedModel{scripts: [][]assistant.ModelEvent{reply(solution), reply(solution)}}
	api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, transcripts, gateway)))

	response := sendAssistantMessage(t, f, api, "solution", "Write it for me")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Eventually(t, func() bool { return transcripts.finalTurn() != nil }, 5*time.Second, 10*time.Millisecond)
	require.Len(t, gateway.asked(), 2, "one rewrite, and no other model call")
	transcripts.mu.Lock()
	defer transcripts.mu.Unlock()
	var checks []model.AssistantCall
	var text string
	for _, record := range transcripts.record {
		if record.Call != nil && record.Call.Tool == "output_check" {
			checks = append(checks, *record.Call)
		}
		if record.Turn != nil && record.Turn.Role == "assistant" && record.Turn.Text != "" {
			text = record.Turn.Text
		}
	}
	require.Len(t, checks, 2)
	require.Equal(t, "replaced", checks[1].Outcome)
	require.NotContains(t, text, "solve()", "the candidate never sees a reply that failed")
	for _, call := range f.publisher.Calls() {
		if call.Method == realtimetest.Publish && call.Event.Name == "exam_attempt_assistant_text_delta" {
			require.NotContains(t, string(call.Event.Data), "solve()")
		}
	}
}

func TestCandidateAssistantCancelStopsTheRunningTurn(t *testing.T) {
	f := newAttemptFixture(t)
	transcripts := newTranscriptLedger(f, model.AssistantLevelTutor)
	transcripts.active, transcripts.turn = true, model.NewAssistantTurnID()
	api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, transcripts, &scriptedModel{})))

	request := f.candidateRequest(t, http.MethodPost, f.candidatePath("/assistant/turns/"+transcripts.turn.String()+"/cancel"))
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	require.Len(t, transcripts.cancel, 1)
	require.Equal(t, f.editorAccess(), transcripts.cancel[0].Editor)

	again := httptest.NewRecorder()
	api.ServeHTTP(again, f.candidateRequest(t, http.MethodPost, f.candidatePath("/assistant/turns/"+transcripts.turn.String()+"/cancel")))
	require.Equal(t, http.StatusNotFound, again.Code, again.Body.String())
	require.Contains(t, again.Body.String(), "exam.attempt.assistant_turn_not_found")
}

func TestCandidateAssistantRoutesAnswerUnavailableWhenTheInstallationHasItOff(t *testing.T) {
	f := newAttemptFixture(t)
	api := f.api(t, examAttemptAssistantResource(nil))

	response := sendAssistantMessage(t, f, api, "off", "Hello")
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "exam.attempt.assistant_unavailable")

	decide := f.candidateRequest(t, http.MethodPost, f.candidatePath("/assistant/approvals/"+model.NewAssistantCallID().String()))
	decide.Body = io.NopCloser(strings.NewReader(`{"decision":"maybe"}`))
	decide.Header.Set("Content-Type", "application/json")
	decided := httptest.NewRecorder()
	api.ServeHTTP(decided, decide)
	require.Equal(t, http.StatusBadRequest, decided.Code, decided.Body.String())
}

type durableAssistantPublisher struct {
	realtime.Publisher
	ledger       *transcriptLedger
	observations chan bool
}

func (p durableAssistantPublisher) Publish(ctx context.Context, event realtime.Event) error {
	if event.Name == "exam_attempt_assistant_text_delta" {
		var data struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(event.Data, &data)
		p.ledger.mu.Lock()
		committed := false
		for _, record := range p.ledger.record {
			if record.Turn != nil && record.Turn.Role == "assistant" && record.Turn.Text == data.Text {
				committed = true
			}
		}
		p.ledger.mu.Unlock()
		p.observations <- committed
	}
	return p.Publisher.Publish(ctx, event)
}
func TestAssistantPublishesOnlyCommittedReplyChunks(t *testing.T) {
	for _, level := range []model.AssistantLevel{model.AssistantLevelTutor, model.AssistantLevelPair} {
		t.Run(string(level), func(t *testing.T) {
			f := newAttemptFixture(t)
			ledger := newTranscriptLedger(f, level)
			publisher := durableAssistantPublisher{Publisher: f.publisher, ledger: ledger, observations: make(chan bool, 16)}
			gateway := &scriptedModel{scripts: [][]assistant.ModelEvent{reply("A durable hint.")}}
			api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, ledger, gateway, publisher)))
			response := sendAssistantMessage(t, f, api, "durable", "Help")
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			select {
			case committed := <-publisher.observations:
				require.True(t, committed, "the reply must be committed before the event")
			case <-time.After(5 * time.Second):
				t.Fatal("no reply event")
			}
			require.Eventually(t, func() bool { return ledger.finalTurn() != nil }, 5*time.Second, time.Millisecond)
		})
	}
}

type heldAssistantGateway struct {
	entered chan struct{}
	release chan struct{}
}

func (g heldAssistantGateway) Stream(context.Context, assistant.ModelRequest) (assistant.ModelStream, error) {
	close(g.entered)
	return heldAssistantStream{g.release}, nil
}

type heldAssistantStream struct{ release <-chan struct{} }

func (s heldAssistantStream) Next() (assistant.ModelEvent, error) {
	<-s.release
	return assistant.ModelEvent{}, io.EOF
}
func (heldAssistantStream) Close() error { return nil }
func TestAssistantShutdownHonorsTheRemainingServerBudget(t *testing.T) {
	f := newAttemptFixture(t)
	ledger := newTranscriptLedger(f, model.AssistantLevelTutor)
	gateway := heldAssistantGateway{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(gateway.release)
	service := newAssistantService(t, f, ledger, gateway)
	api := f.api(t, examAttemptAssistantResource(service))
	response := sendAssistantMessage(t, f, api, "shutdown", "Help")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	select {
	case <-gateway.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	require.Error(t, service.Close(ctx))
}

func TestCandidateAssistantUsesVisibleResourcesAndProposesWithoutExecution(t *testing.T) {
	for _, level := range []model.AssistantLevel{model.AssistantLevelReviewer, model.AssistantLevelPair} {
		t.Run(string(level), func(t *testing.T) {
			f := newAttemptFixture(t)
			transcripts := newTranscriptLedger(f, level)
			name := "read_file"
			arguments := string(mustJSON(t, map[string]string{"path": "resource:" + f.attemptStore.resource.Resource.ResourceID.String()}))
			if level == model.AssistantLevelPair {
				name = "propose_edit"
				arguments = `{"path":"new.txt","content":"a hint\n"}`
			}
			gateway := &scriptedModel{scripts: [][]assistant.ModelEvent{{
				{Kind: assistant.ModelToolCallStart, ID: "first", Name: name},
				{Kind: assistant.ModelToolCallArgsDelta, ID: "first", Text: arguments},
				{Kind: assistant.ModelToolCallEnd, ID: "first"},
				{Kind: assistant.ModelUsageEvent, Usage: assistant.ModelUsage{Input: 40, Output: 12}},
				{Kind: assistant.ModelStop, Reason: "tool_use"},
			}, reply("Consider the information above.")}}
			api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, transcripts, gateway)))
			response := sendAssistantMessage(t, f, api, "use-tool", "Help with this task.")
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			require.Eventually(t, func() bool { return transcripts.finalTurn() != nil }, 5*time.Second, 10*time.Millisecond)
			require.Equal(t, "end_turn", transcripts.finalTurn().StopReason)
			asked := gateway.asked()
			require.Len(t, asked, 2)
			for _, tool := range asked[0].Tools {
				require.NotEqual(t, "run_command", tool.Name)
			}
			result := asked[1].Messages[len(asked[1].Messages)-1]
			require.Equal(t, "tool", result.Role)
			require.False(t, result.IsError, result.Text)
			if level == model.AssistantLevelReviewer {
				require.Contains(t, result.Text, "protected")
			} else {
				require.Contains(t, result.Text, "candidate")
				require.Empty(t, f.workspaceStore.batches, "Pair proposes through the transcript without writing")
				transcripts.mu.Lock()
				defer transcripts.mu.Unlock()
				found := false
				for _, record := range transcripts.record {
					if record.Call != nil && record.Call.Outcome == "proposed" {
						require.Contains(t, record.Call.Result, "+a hint")
						found = true
					}
				}
				require.True(t, found, "the complete proposal is in the candidate's transcript")
			}
		})
	}
}

func TestCandidateAssistantListsAndRepeatsAStopWithinTheAttempt(t *testing.T) {
	f := newAttemptFixture(t)
	ledger := newTranscriptLedger(f, model.AssistantLevelPair)
	now := time.Now().UTC()
	ledger.sessions = []model.AssistantSession{{Tag: "assistant:1", Command: "cat", State: "ended",
		StartedAt: now.Add(-time.Second), EndedAt: now, Outcome: "stopped"}}
	api := f.api(t, examAttemptAssistantResource(newAssistantService(t, f, ledger, &scriptedModel{})))
	send := func(method, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, f.candidateRequest(t, method, f.candidatePath(path)))
		return response
	}
	list := send(http.MethodGet, "/assistant/sessions")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.Contains(t, list.Body.String(), "assistant:1")
	stop := send(http.MethodPost, "/assistant/sessions/assistant:1/stop")
	require.Equal(t, http.StatusOK, stop.Code, stop.Body.String())
	require.Contains(t, stop.Body.String(), "stopped")
	repeat := send(http.MethodPost, "/assistant/sessions/assistant:1/stop")
	require.Equal(t, stop.Body.String(), repeat.Body.String())
	unknown := send(http.MethodPost, "/assistant/sessions/assistant:2/stop")
	require.Equal(t, http.StatusNotFound, unknown.Code, unknown.Body.String())
	require.Contains(t, unknown.Body.String(), "exam.attempt.assistant_session_not_found")
	otherAttempt := httptest.NewRecorder()
	otherPath := "/api/v1/exam-attempts/" + model.NewExamAttemptID().String() + "/assistant/sessions/assistant:1/stop"
	api.ServeHTTP(otherAttempt, f.candidateRequest(t, http.MethodPost, otherPath))
	require.Equal(t, http.StatusNotFound, otherAttempt.Code, otherAttempt.Body.String())
	terminal := send(http.MethodPost, "/assistant/sessions/terminal:1/stop")
	require.Equal(t, http.StatusNotFound, terminal.Code, terminal.Body.String())
}
