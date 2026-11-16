// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package events delivers committed domain events from the outbox to the
// core's handlers.
//
// Delivery is at least once and may reorder events, so handlers must be
// idempotent and tolerate an older fact arriving after a newer one. Each
// subscription succeeds or fails on its own; a retry offers the event only to
// the subscriptions that have not yet accepted it.
package events

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/model"
)

// Subscription is one handler for one topic.
type Subscription struct {
	// Key identifies the subscription across deliveries, so a retry can skip
	// the subscriptions that already accepted an event.
	Key   string
	Topic model.DomainEventTopic
	// Handle runs after the fact has committed, never inside its transaction.
	Handle func(ctx context.Context, event model.DomainEvent) error
}

// Dispatcher offers domain events to a fixed, ordered list of subscriptions.
type Dispatcher struct {
	subscriptions []Subscription
}

// NewDispatcher refuses a subscription without a key or handler, an unknown
// topic, or a repeated key.
func NewDispatcher(subscriptions []Subscription) (*Dispatcher, error) {
	keys := make(map[string]struct{}, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.Key == "" || subscription.Handle == nil || !subscription.Topic.IsValid() {
			return nil, fmt.Errorf("domain event subscription %q is incomplete", subscription.Key)
		}
		if _, repeated := keys[subscription.Key]; repeated {
			return nil, fmt.Errorf("domain event subscription %q is repeated", subscription.Key)
		}
		keys[subscription.Key] = struct{}{}
	}
	return &Dispatcher{subscriptions: slices.Clone(subscriptions)}, nil
}

// Dispatch offers event, in order and each with its own copy of the data, to
// every subscription to its topic whose key is not in delivered. One failure
// does not stop the others. It returns delivered extended with the keys that
// accepted, together with every failure.
func (d *Dispatcher) Dispatch(ctx context.Context, event *model.DomainEvent, delivered []string) ([]string, error) {
	if event == nil || !event.Topic.IsValid() {
		return delivered, errors.New("domain event is invalid")
	}
	accepted := slices.Clone(delivered)
	var failures error
	for _, subscription := range d.subscriptions {
		if subscription.Topic != event.Topic || slices.Contains(accepted, subscription.Key) {
			continue
		}
		copied := *event
		copied.Data = slices.Clone(event.Data)
		if handleErr := subscription.Handle(ctx, copied); handleErr != nil {
			failures = errors.Join(failures, fmt.Errorf("domain event subscription %q: %w", subscription.Key, handleErr))
			continue
		}
		accepted = append(accepted, subscription.Key)
	}
	return accepted, failures
}
