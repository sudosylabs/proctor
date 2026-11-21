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
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Dependencies are the collaborators New validates once. Terminals is nil
// when the installation runs no execution environments; the assistant then
// offers no terminal or command tool.
type Dependencies struct {
	Wake        func(context.Context, model.ExamAttemptID) error
	Transcripts store.AssistantTranscriptStore
	Attempts    *attempt.Service
	Terminals   *environment.Terminals
	Gateway     ModelGateway
	Auditor     *access.Auditor
	Publisher   realtime.Publisher
	Now         func() time.Time

	NewConversationID func() model.AssistantConversationID
	NewTurnID         func() model.AssistantTurnID
	NewCallID         func() model.AssistantCallID
	NewOwnerToken     func() string

	// Limits zero-valued fields take their defaults.
	Limits Limits
}

// Limits bound one turn. They protect the server and the provider; the
// attempt's budgets bound the candidate's spend.
type Limits struct {
	// MaxOutputTokens is asked of the model, and reserved, per call.
	MaxOutputTokens int64
	// MaxCallsPerTurn bounds the tool calls of one turn.
	MaxCallsPerTurn int
	// RenewEvery keeps the turn's run fence; the store's lease is 20 seconds.
	RenewEvery time.Duration
	// ApprovalTimeout declines a request the candidate leaves unanswered.
	ApprovalTimeout time.Duration
	// HistoryBytes bounds the earlier conversation sent with each call.
	HistoryBytes int
}

func (l Limits) withDefaults() Limits {
	if l.MaxOutputTokens <= 0 {
		l.MaxOutputTokens = 2048
	}
	if l.MaxCallsPerTurn <= 0 {
		l.MaxCallsPerTurn = 16
	}
	if l.RenewEvery <= 0 {
		l.RenewEvery = 5 * time.Second
	}
	if l.ApprovalTimeout <= 0 {
		l.ApprovalTimeout = 5 * time.Minute
	}
	if l.HistoryBytes <= 0 {
		l.HistoryBytes = 48 << 10
	}
	return l
}

// Service runs the candidate's conversation with the assistant. A nil
// Service means the installation has the assistant off; every use case then
// answers exam.attempt.assistant_unavailable.
//
// A turn runs in the background on the node that accepted the message. The
// store's run fence makes it the only turn of its attempt; a seal, a cancel
// from any node or the end of the candidate's participation takes the fence
// away, and the turn stops at its next renewal or store call.
type Service struct {
	wake        func(context.Context, model.ExamAttemptID) error
	transcripts store.AssistantTranscriptStore
	attempts    *attempt.Service
	terminals   *environment.Terminals
	gateway     ModelGateway
	auditor     access.MutationAuditAdapter
	publisher   realtime.Publisher
	now         func() time.Time
	limits      Limits

	newConversationID func() model.AssistantConversationID
	newTurnID         func() model.AssistantTurnID
	newCallID         func() model.AssistantCallID
	newOwnerToken     func() string

	base     context.Context
	shutdown context.CancelFunc
	group    sync.WaitGroup

	mu   sync.Mutex
	runs map[model.ExamAttemptID]*turnRun
}

// New fails when a required collaborator is missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Transcripts == nil || deps.Attempts == nil || deps.Gateway == nil || deps.Auditor == nil || deps.Publisher == nil ||
		deps.Now == nil || deps.NewConversationID == nil || deps.NewTurnID == nil || deps.NewCallID == nil || deps.NewOwnerToken == nil {
		return nil, errors.New("assistant dependencies are required")
	}
	base, shutdown := context.WithCancel(context.Background())
	return &Service{
		wake: deps.Wake, transcripts: deps.Transcripts, attempts: deps.Attempts, terminals: deps.Terminals, gateway: deps.Gateway,
		auditor: deps.Auditor.MutationAudit(), publisher: deps.Publisher, now: deps.Now, limits: deps.Limits.withDefaults(),
		newConversationID: deps.NewConversationID, newTurnID: deps.NewTurnID, newCallID: deps.NewCallID,
		newOwnerToken: deps.NewOwnerToken, base: base, shutdown: shutdown, runs: map[model.ExamAttemptID]*turnRun{},
	}, nil
}

// Close stops every turn this node runs and waits for them to record their
// end. A stopped turn ends as canceled; the candidate may ask again.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.shutdown()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.group.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("assistant turns did not stop within the shutdown budget")
	}
}

func (s *Service) register(run *turnRun) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.base.Err() != nil {
		return false
	}
	if previous := s.runs[run.attemptID()]; previous != nil {
		// The store gave this node the fence, so an older local run has lost it.
		previous.stop(errFenceLost)
	}
	s.runs[run.attemptID()] = run
	s.group.Add(1)
	return true
}

func (s *Service) forget(run *turnRun) {
	s.mu.Lock()
	if s.runs[run.attemptID()] == run {
		delete(s.runs, run.attemptID())
	}
	s.mu.Unlock()
	s.group.Done()
}

func (s *Service) local(attemptID model.ExamAttemptID, turn model.AssistantTurnID) *turnRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[attemptID]
	if run == nil || run.key.TurnID != turn {
		return nil
	}
	return run
}

func (s *Service) publish(ctx context.Context, operation string, event realtime.Event, err error) {
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, operation, err)
	}
}

// NotifyEvent is a hint only; renewal rechecks the candidate proof and run fence.
func (s *Service) NotifyEvent(event realtime.Event) {
	var data struct {
		AttemptID string `json:"exam_attempt_id"`
		SittingID string `json:"exam_sitting_id"`
	}
	if json.Unmarshal(event.Data, &data) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, run := range s.runs {
		if data.AttemptID == id.String() || (data.AttemptID == "" && data.SittingID == run.candidate.SittingID.String()) {
			switch event.Name {
			case "exam_attempt_access_suspended", "exam_attempt_suspended", "exam_attempt_connection_closed", "exam_attempt_submitted":
				if s.terminals != nil && s.base.Err() == nil {
					s.group.Add(1)
					go s.finishParticipationSessions(run)
				}
			}
			// A submitted event follows the durable seal, which already closed the run.
			if event.Name == "exam_attempt_submitted" && data.AttemptID == id.String() {
				run.stop(errFenceLost)
				continue
			}
			select {
			case run.wake <- struct{}{}:
			default:
			}
		}
	}
}
func (s *Service) notifyCommitted(ctx context.Context, id model.ExamAttemptID) {
	data, _ := json.Marshal(struct {
		AttemptID string `json:"exam_attempt_id"`
	}{id.String()})
	s.NotifyEvent(realtime.Event{Data: data})
	if s.wake != nil {
		if err := s.wake(ctx, id); err != nil {
			s.publisher.Report(ctx, "assistant_wake", err)
		}
	}
}

// An environment owner also reconciles sessions while freezing. This path
// shortens cleanup when candidate proof is still usable on this node.
func (s *Service) finishParticipationSessions(run *turnRun) {
	defer s.group.Done()
	ctx, cancel := context.WithTimeout(s.base, 3*time.Second)
	defer cancel()
	_, _ = s.reconcileSessions(ctx, run.inv, run.environmentAccess(), run.target.SittingID, run.key.Editor, "", true)
}
