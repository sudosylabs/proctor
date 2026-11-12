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
// Dispatcher, which presents it to every registered plugin.EventHandler. The
// handler list is fixed when the Dispatcher is built. In production it is
// empty, because no plugin host exists yet: an event is dispatched to nobody
// and its outbox row is deleted.
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

// Dispatcher presents domain events to a fixed, ordered list of handlers.
type Dispatcher struct {
	handlers []plugin.EventHandler
}

// NewDispatcher returns a Dispatcher that calls handlers in the order given.
// A nil handler is rejected.
func NewDispatcher(handlers ...plugin.EventHandler) (*Dispatcher, error) {
	if slices.Contains(handlers, nil) {
		return nil, errors.New("domain event handler is nil")
	}
	return &Dispatcher{handlers: slices.Clone(handlers)}, nil
}

// Dispatch converts event into a plugin.Event and calls each handler in
// order. It stops at the first handler error and returns it, so the event is
// delivered again later; handlers already called see the same event ID again.
func (d *Dispatcher) Dispatch(ctx context.Context, event *model.DomainEvent) error {
	delivered, err := pluginEvent(event)
	if err != nil {
		return err
	}
	for index, handler := range d.handlers {
		copied := delivered
		copied.Data = slices.Clone(delivered.Data)
		if err = handler.HandleEvent(ctx, copied); err != nil {
			return fmt.Errorf("domain event handler %d: %w", index, err)
		}
	}
	return nil
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
