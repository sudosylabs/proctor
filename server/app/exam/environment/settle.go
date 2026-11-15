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
	defaultSettlePoll   = 100 * time.Millisecond
	// settleRecordTimeout bounds recording an incomplete settle, which runs
	// even when the caller's context has ended.
	settleRecordTimeout = 5 * time.Second
)

// SettlerConfig is what NewSettler needs.
type SettlerConfig struct {
	Environments store.AttemptEnvironmentStore
	Waker        Waker
	// Budget bounds one settle; zero means DefaultSettleBudget.
	Budget time.Duration
	// Poll is how often the settle rereads the environment; zero means
	// 100 ms.
	Poll          time.Duration
	Now           func() time.Time
	NewEvidenceID func() model.IntegrityEvidenceID
	// Report receives failures nobody else sees. Nil drops them.
	Report func(attemptID model.ExamAttemptID, err error)
}

// Settler settles an Attempt's environment before its seal, wherever its
// owner runs: it asks the environment for a settle through the Store, wakes
// the owner, and waits for the owner to finish within the budget. A settle
// that does not finish in time, or cannot be asked, is recorded as an
// execution gap and the seal goes ahead.
type Settler struct {
	config SettlerConfig
}

// NewSettler validates config.
func NewSettler(config SettlerConfig) (*Settler, error) {
	if config.Environments == nil || config.NewEvidenceID == nil {
		return nil, errors.New("attempt environment settler needs the environment store and an evidence ID source")
	}
	if config.Budget <= 0 {
		config.Budget = DefaultSettleBudget
	}
	if config.Poll <= 0 {
		config.Poll = defaultSettlePoll
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Settler{config: config}, nil
}

// Settle returns once attemptID's environment has settled, the budget is
// spent, or ctx ends. It never refuses the seal.
func (s *Settler) Settle(ctx context.Context, attemptID model.ExamAttemptID) {
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
	if s.wait(ctx, attemptID, settle) {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleRecordTimeout)
	defer cancel()
	if err = s.config.Environments.RecordSettleIncomplete(recordCtx, &store.AttemptEnvironmentSettleGap{
		AttemptID: attemptID, Settle: settle, EvidenceID: s.config.NewEvidenceID(), ObservedAt: s.config.Now(),
	}); err != nil {
		s.report(attemptID, err)
	}
}

// wait reports whether the owner completed settle within the budget.
func (s *Settler) wait(ctx context.Context, attemptID model.ExamAttemptID, settle int64) bool {
	budget, cancel := context.WithTimeout(ctx, s.config.Budget)
	defer cancel()
	ticker := time.NewTicker(s.config.Poll)
	defer ticker.Stop()
	for {
		environment, err := s.config.Environments.Get(budget, attemptID)
		switch {
		case err == nil && environment.SettleCompleted >= settle:
			return true
		case err != nil && budget.Err() == nil:
			s.report(attemptID, err)
		}
		select {
		case <-budget.Done():
			return false
		case <-ticker.C:
		}
	}
}

func (s *Settler) report(attemptID model.ExamAttemptID, err error) {
	if s.config.Report != nil && err != nil {
		s.config.Report(attemptID, err)
	}
}
