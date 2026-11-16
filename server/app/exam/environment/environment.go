// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package environment owns the execution machine of each attempt whose
// revision enables execution, kept in step with the attempt and its workspace.
// One node at a time owns an environment under a fenced runtime lease, and
// every write back carries that lease. Terminals are served from any node
// without a lease, so each call re-proves candidate access. Wakes can be lost;
// a slow backstop claims environments nobody holds. The execution host is
// reached only through a port the composition root implements.
package environment

import (
	"context"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Waker wakes the owner of an attempt's environment, wherever it runs. It
// never blocks and never fails; the backstop catches a lost wake.
type Waker interface {
	Wake(attemptID model.ExamAttemptID)
}

// Facts applies committed attempt and sitting facts to environments.
// Delivery is at least once and in any order; the store applies each fact
// idempotently.
type Facts struct {
	environments store.AttemptEnvironmentStore
	waker        Waker
}

// NewFacts returns the fact handler; waker may be nil until the runtime
// exists.
func NewFacts(environments store.AttemptEnvironmentStore, waker Waker) (*Facts, error) {
	if environments == nil {
		return nil, errors.New("attempt environment store is required")
	}
	return &Facts{environments: environments, waker: waker}, nil
}

// HandleEvent applies the environment fact a domain event carries, then
// wakes the owner of every environment it changed.
func (f *Facts) HandleEvent(ctx context.Context, event model.DomainEvent) error {
	fact, err := event.AttemptEnvironmentFact()
	if err != nil {
		return fmt.Errorf("read attempt environment fact: %w", err)
	}
	result, err := f.environments.ApplyFact(ctx, fact)
	if err != nil {
		return fmt.Errorf("apply attempt environment fact: %w", err)
	}
	if f.waker != nil {
		for _, attemptID := range result.Changed {
			f.waker.Wake(attemptID)
		}
	}
	return nil
}

// Topics returns the domain-event topics Facts handles.
func Topics() []model.DomainEventTopic {
	return []model.DomainEventTopic{
		model.DomainEventTopicExamAttemptParticipationStarted,
		model.DomainEventTopicExamAttemptParticipationEnded,
		model.DomainEventTopicExamAttemptSubmitted,
		model.DomainEventTopicExamSittingPaused,
		model.DomainEventTopicExamSittingResumed,
		model.DomainEventTopicExamSittingClosing,
	}
}
