// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The settle's defaults.
const (
	DefaultSettleBudget = 5 * time.Second
	// settleRecordTimeout bounds recording an incomplete settle, which runs
	// even when the caller's context has ended.
	settleRecordTimeout = 5 * time.Second
)

// SettleNotifier tells whoever waits on an Attempt's settle that its owner
// completed one, wherever the waiter runs. It never blocks and never
// fails: a lost signal costs the waiter its budget, never a false gap,
// because the gap is recorded only for a settle the row still has pending.
type SettleNotifier interface {
	Settled(attemptID model.ExamAttemptID)
}

// SettleSubscriber delivers completed settles to waiters on this node.
// SubscribeSettled returns a channel that is signaled, without blocking the
// notifier, after each settle the Attempt's owner completes from then on,
// and a cancel that ends the subscription.
type SettleSubscriber interface {
	SubscribeSettled(attemptID model.ExamAttemptID) (<-chan struct{}, func())
}

// SettlerConfig is what NewSettler needs.
type SettlerConfig struct {
	Environments store.AttemptEnvironmentStore
	Waker        Waker
	// Signals delivers the owner's completed settles; the settler reads the
	// row only when it wakes the owner and when a signal arrives.
	Signals SettleSubscriber
	// Budget bounds one settle; zero means DefaultSettleBudget.
	Budget        time.Duration
	Now           func() time.Time
	NewEvidenceID func() model.IntegrityEvidenceID
	// Report receives failures nobody else sees. Nil drops them.
	Report func(attemptID model.ExamAttemptID, err error)
}

// Settler settles an Attempt's environment before its seal, wherever its
// owner runs: it asks the environment for a settle through the Store, wakes
// the owner, and waits for the owner's completion signal within the budget.
// A settle that does not finish in time, or cannot be asked, is recorded as
// an execution gap and the seal goes ahead.
type Settler struct {
	config SettlerConfig
}

// NewSettler validates config.
func NewSettler(config SettlerConfig) (*Settler, error) {
	if config.Environments == nil || config.Signals == nil || config.NewEvidenceID == nil {
		return nil, errors.New("attempt environment settler needs the environment store, its settle signals and an evidence ID source")
	}
	if config.Budget <= 0 {
		config.Budget = DefaultSettleBudget
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Settler{config: config}, nil
}

// Settle asks for one more settle of attemptID's environment and returns
// once the owner has completed it, the budget is spent, or ctx ends. It
// never refuses the seal.
func (s *Settler) Settle(ctx context.Context, attemptID model.ExamAttemptID) {
	// The subscription comes first, so a completion right after the request
	// is never missed.
	signal, cancel := s.config.Signals.SubscribeSettled(attemptID)
	defer cancel()
	settle, err := s.config.Environments.RequestSettle(ctx, attemptID)
	if err != nil {
		s.report(attemptID, err)
		return
	}
	if settle == 0 {
		return
	}
	if s.config.Waker != nil {
		s.config.Waker.Wake(attemptID)
	}
	budget, stop := context.WithTimeout(ctx, s.config.Budget)
	defer stop()
	// The owner may have finished before the subscription could see it on
	// another node.
	if s.completed(budget, attemptID, settle) || s.await(budget, attemptID, settle, signal) {
		return
	}
	s.recordIncomplete(ctx, attemptID, settle)
}

// AwaitPendingSettle waits for the settle attemptID's environment already
// has pending, without asking for another: a closing Sitting asked every
// environment of the Sitting at once. It waits for the owner's signal
// within what is left of the budget since that settle was asked, then
// records the incomplete settle gap if it is still pending. An environment
// with nothing pending returns at once.
func (s *Settler) AwaitPendingSettle(ctx context.Context, attemptID model.ExamAttemptID) {
	signal, cancel := s.config.Signals.SubscribeSettled(attemptID)
	defer cancel()
	environment, err := s.config.Environments.Get(ctx, attemptID)
	if store.IsNotFound(err) {
		return
	}
	if err != nil {
		s.report(attemptID, err)
		return
	}
	if !environment.SettlePending() {
		return
	}
	settle := environment.SettleRequested
	if remaining := environment.SettleRequestedAt.Add(s.config.Budget).Sub(s.config.Now()); remaining > 0 {
		budget, stop := context.WithTimeout(ctx, remaining)
		defer stop()
		if s.await(budget, attemptID, settle, signal) {
			return
		}
	}
	s.recordIncomplete(ctx, attemptID, settle)
}

// completed reads the row once and reports whether settle is complete.
func (s *Settler) completed(ctx context.Context, attemptID model.ExamAttemptID, settle int64) bool {
	environment, err := s.config.Environments.Get(ctx, attemptID)
	if err != nil {
		if ctx.Err() == nil {
			s.report(attemptID, err)
		}
		return false
	}
	return environment.SettleCompleted >= settle
}

// await waits for a completion signal and confirms it with one read, until
// settle is complete or ctx ends. A signal for an earlier settle keeps it
// waiting.
func (s *Settler) await(ctx context.Context, attemptID model.ExamAttemptID, settle int64, signal <-chan struct{}) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-signal:
			if s.completed(ctx, attemptID, settle) {
				return true
			}
		}
	}
}

// recordIncomplete records the settle gap, even when ctx has ended. The
// Store changes nothing for a settle that completed meanwhile.
func (s *Settler) recordIncomplete(ctx context.Context, attemptID model.ExamAttemptID, settle int64) {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleRecordTimeout)
	defer cancel()
	if err := s.config.Environments.RecordSettleIncomplete(recordCtx, &store.AttemptEnvironmentSettleGap{
		AttemptID: attemptID, Settle: settle, EvidenceID: s.config.NewEvidenceID(), ObservedAt: s.config.Now(),
	}); err != nil {
		s.report(attemptID, err)
	}
}

func (s *Settler) report(attemptID model.ExamAttemptID, err error) {
	if s.config.Report != nil && err != nil {
		s.config.Report(attemptID, err)
	}
}
