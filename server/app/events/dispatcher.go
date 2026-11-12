// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package events delivers committed domain events to plugin event handlers.
//
// A domain event is written to the outbox in the same transaction as the fact
// it describes; the domain-event dispatch Job then loads it and hands it to a
// Dispatcher, which offers it to every plugin subscribed to its topic. Each
// subscription accepts or fails on its own, so a failing plugin delays only
// its own delivery: the Job remembers which subscriptions accepted the event
// and offers it again only to the others.
//
// The package is a leaf: it imports only model and the plugin contracts.
package events

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// Subscription is one plugin's handler for one topic.
type Subscription struct {
	// Key identifies the subscription across deliveries, so a retry can skip
	// the subscriptions that already accepted an event.
	Key     string
	Topic   plugin.EventTopic
	Handler plugin.EventHandler
}

// Dispatcher offers domain events to the subscriptions its source reports,
// read again for every event so it follows plugins being turned on and off.
type Dispatcher struct {
	source func() []Subscription
}

// NewDispatcher returns a Dispatcher that offers each event to the
// subscriptions source returns at that moment, in order. A nil source means
// no subscriptions.
func NewDispatcher(source func() []Subscription) *Dispatcher {
	return &Dispatcher{source: source}
}

// subscriptions reads the source and rejects a subscription without a key
// or handler, or a repeated key.
func (d *Dispatcher) subscriptions() ([]Subscription, error) {
	if d.source == nil {
		return nil, nil
	}
	subscriptions := d.source()
	seen := make(map[string]struct{}, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.Key == "" || subscription.Handler == nil {
			return nil, errors.New("domain event subscription needs a key and a handler")
		}
		if _, repeated := seen[subscription.Key]; repeated {
			return nil, fmt.Errorf("domain event subscription %q is repeated", subscription.Key)
		}
		seen[subscription.Key] = struct{}{}
	}
	return subscriptions, nil
}

// Dispatch offers event to every subscription to its topic whose key is not
// in delivered, in order, each with its own copy of the data. It offers it to
// all of them even when one fails, and returns delivered extended with the
// keys that accepted it, together with every failure. A subscription that
// failed sees the same event ID again when it is offered next.
func (d *Dispatcher) Dispatch(ctx context.Context, event *model.DomainEvent, delivered []string) ([]string, error) {
	offered, err := pluginEvent(event)
	if err != nil {
		return delivered, err
	}
	subscriptions, err := d.subscriptions()
	if err != nil {
		return delivered, err
	}
	accepted := slices.Clone(delivered)
	var failures error
	for _, subscription := range subscriptions {
		if subscription.Topic != offered.Topic || slices.Contains(accepted, subscription.Key) {
			continue
		}
		copied := offered
		copied.Data = slices.Clone(offered.Data)
		if handleErr := subscription.Handler.HandleEvent(ctx, copied); handleErr != nil {
			failures = errors.Join(failures, fmt.Errorf("domain event subscription %q: %w", subscription.Key, handleErr))
			continue
		}
		accepted = append(accepted, subscription.Key)
	}
	return accepted, failures
}

func pluginEvent(event *model.DomainEvent) (plugin.Event, error) {
	if err := event.Validate(); err != nil {
		return plugin.Event{}, fmt.Errorf("invalid domain event: %w", err)
	}
	var topic plugin.EventTopic
	switch event.Topic {
	case model.DomainEventTopicExamAttemptSubmitted:
		topic = plugin.TopicExamAttemptSubmitted
	default:
		return plugin.Event{}, fmt.Errorf("domain event topic %q has no plugin topic", event.Topic)
	}
	return plugin.Event{ID: event.ID.String(), Topic: topic, OccurredAt: event.OccurredAt,
		Data: slices.Clone(event.Data)}, nil
}
