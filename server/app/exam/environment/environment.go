// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package environment owns Attempt Environments: one isolated execution
// machine per Attempt whose Revision enables execution, kept in step with
// the Attempt and its Workspace.
//
// Committed Attempt and Sitting facts reach it through the domain-event
// outbox and set what each environment should be (Facts). One node at a time
// owns an environment, under a fenced runtime lease that the Runtime claims,
// renews and releases; the owner it runs acts on the execution host and
// writes back through the Store, carrying the lease. Workspace commits and
// facts wake the owner; a slow backstop claims what nobody holds.
//
// The package holds no execution-host client. The owner reaches a host
// through a port that the composition root implements.
package environment

import (
	"context"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Waker wakes the owner of an Attempt's environment, wherever it runs. It
// never blocks and never fails: a lost wake is caught by the backstop.
type Waker interface {
	Wake(attemptID model.ExamAttemptID)
}

// Facts applies committed Attempt and Sitting facts to environments. It is
// a domain-event handler: delivery is at least once and in any order, and
// the Store applies each fact idempotently.
type Facts struct {
	environments store.AttemptEnvironmentStore
	waker        Waker
}

// NewFacts returns the fact handler. waker may be nil until the runtime
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

// Topics are the domain-event topics Facts handles.
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
