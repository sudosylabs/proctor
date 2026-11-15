// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package events delivers committed domain events to the core's handlers.
//
// A domain event is written to the outbox in the same transaction as the fact
// it describes; the domain-event dispatch Job then loads it and hands it to a
// Dispatcher, which offers it to every subscription to its topic. Each
// subscription accepts or fails on its own, so a failing handler delays only
// its own delivery: the Job remembers which subscriptions accepted the event
// and offers it again only to the others. Delivery is at least once and may
// reorder events, so a handler is idempotent and tolerates an older fact
// arriving after a newer one.
//
// The package is a leaf: it imports only model.
package events

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/model"
)

// Handler handles one committed domain event. It runs after the fact has
// committed, never inside it.
type Handler interface {
	HandleEvent(ctx context.Context, event model.DomainEvent) error
}

// Subscription is one handler for one topic.
type Subscription struct {
	// Key identifies the subscription across deliveries, so a retry can skip
	// the subscriptions that already accepted an event.
	Key     string
	Topic   model.DomainEventTopic
	Handler Handler
}

// Dispatcher offers domain events to a fixed list of subscriptions.
type Dispatcher struct {
	subscriptions []Subscription
}

// NewDispatcher returns a Dispatcher that offers each event to subscriptions,
// in order. It refuses a subscription without a key or handler, an unknown
// topic, or a repeated key.
func NewDispatcher(subscriptions []Subscription) (*Dispatcher, error) {
	keys := make(map[string]struct{}, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.Key == "" || subscription.Handler == nil || !subscription.Topic.IsValid() {
			return nil, fmt.Errorf("domain event subscription %q is incomplete", subscription.Key)
		}
		if _, repeated := keys[subscription.Key]; repeated {
			return nil, fmt.Errorf("domain event subscription %q is repeated", subscription.Key)
		}
		keys[subscription.Key] = struct{}{}
	}
	return &Dispatcher{subscriptions: slices.Clone(subscriptions)}, nil
}

// Dispatch offers event to every subscription to its topic whose key is not
// in delivered, in order, each with its own copy of the data. It offers it to
// all of them even when one fails, and returns delivered extended with the
// keys that accepted it, together with every failure. A subscription that
// failed sees the same event ID again when it is offered next.
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
		if handleErr := subscription.Handler.HandleEvent(ctx, copied); handleErr != nil {
			failures = errors.Join(failures, fmt.Errorf("domain event subscription %q: %w", subscription.Key, handleErr))
			continue
		}
		accepted = append(accepted, subscription.Key)
	}
	return accepted, failures
}
