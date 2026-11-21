// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost revision
// 10b780cb097b2ec94ab0f9df7ebcbd5b7850f13f, particularly
// server/channels/app/platform/cluster_handlers.go and
// server/channels/app/platform/web_hub.go. See doc.go and the server NOTICE for
// provenance and licensing details.

package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

const (
	clusterEventPublication        = "websocket.publish"
	clusterEventExamAttemptUnbound = "exam_attempt.connection_unbound"
)

// Sink delivers already-authorized events and connection closes to this
// node's sockets. It uses no WebSocket wire types, so this package stays
// transport-neutral.
type Sink interface {
	PublishLocal(context.Context, Event)
	UnbindExamAttemptConnection(model.AttemptConnectionID)
	CloseSession(string, ConnectionCloseReason)
	CloseUser(string, ConnectionCloseReason)
	CloseAll(ConnectionCloseReason)
}

// AuthenticationInvalidator drops this node's cached authentication when
// sessions or credentials are invalidated.
type AuthenticationInvalidator interface {
	InvalidateAccessCredentials(context.Context, []string)
	InvalidateSessionActivity(context.Context, []model.SessionID)
}

// InvalidPublicationError reports an event that failed validation; nothing
// was delivered.
type InvalidPublicationError struct {
	err error
}

func (e *InvalidPublicationError) Error() string { return e.err.Error() }
func (e *InvalidPublicationError) Unwrap() error { return e.err }

// DeliveryError reports a peer encoding or fan-out failure. Local delivery
// has already been attempted when Publish returns it.
type DeliveryError struct {
	err error
}

func (e *DeliveryError) Error() string { return e.err.Error() }
func (e *DeliveryError) Unwrap() error { return e.err }

// DeliveryDependencies are the collaborators NewDelivery requires.
type DeliveryDependencies struct {
	AuthenticationInvalidator AuthenticationInvalidator
	// Diagnostics records non-fatal propagation failures. Never log payloads
	// or security-sensitive identifiers to it.
	Diagnostics *logging.Logger
}

// Delivery is inert until its sink and cluster fan-out are attached, and
// does not own either.
type Delivery struct {
	deps DeliveryDependencies

	mu                sync.RWMutex
	sink              Sink
	fanout            cluster.Transport
	assistantListener func(Event)
}

// NewDelivery returns an inert Delivery. The sink and fan-out are attached
// later because composition builds them after the application graph.
func NewDelivery(deps DeliveryDependencies) (*Delivery, error) {
	if deps.AuthenticationInvalidator == nil {
		return nil, errors.New("realtime authentication invalidator is nil")
	}
	if deps.Diagnostics == nil {
		return nil, errors.New("realtime diagnostics is nil")
	}
	return &Delivery{deps: deps}, nil
}

// SetSink attaches the local sink exactly once.
func (s *Delivery) SetSink(sink Sink) error {
	if sink == nil {
		return errors.New("realtime sink is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sink != nil {
		return errors.New("realtime sink is already attached")
	}
	s.sink = sink
	return nil
}

// SetClusterFanout attaches the cluster once and registers the peer handlers.
// A registration failure is terminal: unwind node construction rather than
// retry.
func (s *Delivery) SetClusterFanout(fanout cluster.Transport) error {
	if fanout == nil {
		return errors.New("realtime cluster fan-out is nil")
	}
	s.mu.Lock()
	if s.fanout != nil {
		s.mu.Unlock()
		return errors.New("realtime cluster fan-out is already attached")
	}
	s.fanout = fanout
	s.mu.Unlock()

	handlers := []struct {
		event   string
		handler func(context.Context, []byte) error
	}{
		{event: clusterEventPublication, handler: s.handlePeerPublication},
		{event: "assistant.changed", handler: s.handleAssistantChanged},
		{event: clusterEventExamAttemptUnbound, handler: s.handlePeerExamAttemptUnbound},
		{event: clusterEventSessionRevoked, handler: s.handlePeerSessionRevocation},
		{event: clusterEventAuthorizationInvalidated, handler: s.handlePeerAuthorizationInvalidation},
	}
	for _, registration := range handlers {
		if err := fanout.RegisterHandler(cluster.Event(registration.event), peerHandler(registration.handler)); err != nil {
			return fmt.Errorf("register %s cluster handler: %w", registration.event, err)
		}
	}
	return nil
}

// UnbindExamAttemptConnection clears one attempt connection's binding,
// locally first and then on peers. The socket stays open for other uses.
func (s *Delivery) UnbindExamAttemptConnection(ctx context.Context, connectionID model.AttemptConnectionID) error {
	if !connectionID.IsValid() {
		return &InvalidPublicationError{err: errors.New("Exam Attempt Connection identity is invalid")}
	}
	s.unbindExamAttemptConnectionLocal(connectionID)
	payload, err := json.Marshal(examAttemptUnboundMessage{AttemptConnectionID: connectionID.String()})
	if err != nil {
		return &DeliveryError{err: err}
	}
	if err = s.broadcast(ctx, clusterEventExamAttemptUnbound, payload); err != nil {
		return &DeliveryError{err: err}
	}
	return nil
}

func (s *Delivery) unbindExamAttemptConnectionLocal(connectionID model.AttemptConnectionID) {
	s.mu.RLock()
	sink := s.sink
	s.mu.RUnlock()
	if sink != nil {
		sink.UnbindExamAttemptConnection(connectionID)
	}
}

type examAttemptUnboundMessage struct {
	AttemptConnectionID string `json:"attempt_connection_id"`
}

func (s *Delivery) handlePeerExamAttemptUnbound(_ context.Context, data []byte) error {
	var message examAttemptUnboundMessage
	if err := decodeStrictPayload(data, &message); err != nil {
		return err
	}
	connectionID, err := model.ParseAttemptConnectionID(message.AttemptConnectionID)
	if err != nil {
		return errors.New("cluster Exam Attempt Connection identity is invalid")
	}
	s.unbindExamAttemptConnectionLocal(connectionID)
	return nil
}

func decodeStrictPayload(data []byte, target any) error {
	if len(data) == 0 {
		return errors.New("realtime cluster payload is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("realtime cluster payload has trailing JSON")
		}
		return err
	}
	return nil
}

// Publish delivers a clone locally first, then to peers. Call it only after
// the change has committed.
func (s *Delivery) Publish(ctx context.Context, event Event) error {
	candidate := event.Clone()
	if candidate.ID == "" {
		candidate.ID = model.NewID()
	}
	if err := candidate.ValidateForPublish(); err != nil {
		return &InvalidPublicationError{err: err}
	}
	// The server's event list is closed where an event starts. A peer forwards
	// what its origin node accepted and checks only the shape.
	if !isDeclaredName(candidate.Name) {
		return &InvalidPublicationError{err: fmt.Errorf("realtime event %q is not a declared message", candidate.Name)}
	}

	// Peer handlers call only publishLocal, so a forwarded event never
	// loops.
	s.publishLocal(ctx, candidate)
	message := realtimeEventMessageFromModel(candidate)
	payload, err := json.Marshal(realtimePublication{Event: &message})
	if err != nil {
		return &DeliveryError{err: err}
	}
	if err := s.broadcast(ctx, clusterEventPublication, payload); err != nil {
		return &DeliveryError{err: err}
	}
	return nil
}

func (s *Delivery) publishLocal(ctx context.Context, event Event) {
	s.mu.RLock()
	sink := s.sink
	listener := s.assistantListener
	s.mu.RUnlock()
	if listener != nil {
		switch event.Name {
		case "exam_attempt_submitted", "exam_attempt_connection_closed", "exam_attempt_access_suspended", "exam_attempt_suspended",
			"exam_sitting_lifecycle_changed", "exam_sitting_content_corrected":
			listener(event)
		}
	}
	if sink != nil {
		sink.PublishLocal(ctx, event.Clone())
	}
}

func (s *Delivery) broadcast(ctx context.Context, event string, data []byte) error {
	s.mu.RLock()
	fanout := s.fanout
	s.mu.RUnlock()
	if fanout == nil {
		return errors.New("realtime cluster fan-out is not attached")
	}
	return fanout.Broadcast(ctx, &cluster.Message{Event: cluster.Event(event), Data: data})
}

// peerHandler refuses a malformed envelope before its payload reaches
// handler, since a peer's message is untrusted input.
func peerHandler(handler func(context.Context, []byte) error) cluster.Handler {
	return func(ctx context.Context, message *cluster.Message) error {
		if message == nil {
			return errors.New("cluster message is nil")
		}
		if err := message.Validate(); err != nil {
			return err
		}
		return handler(ctx, message.Data)
	}
}

func (s *Delivery) handlePeerPublication(ctx context.Context, data []byte) error {
	var publication realtimePublication
	if err := decodePayload(data, &publication); err != nil {
		return err
	}
	if publication.Event == nil {
		return errors.New("cluster realtime publication has no event")
	}
	event := publication.Event.toModel()
	if err := event.ValidateForPublish(); err != nil {
		return err
	}
	s.publishLocal(ctx, event)
	return nil
}

type realtimePublication struct {
	Event *realtimeEventMessage `json:"event"`
}

// realtimeEventMessage is a separate type so domain field renames cannot
// change the snake_case cluster wire format.
type realtimeEventMessage struct {
	ID        string                  `json:"id"`
	Name      string                  `json:"event"`
	UserID    string                  `json:"user_id,omitempty"`
	SessionID string                  `json:"session_id,omitempty"`
	Action    model.Action            `json:"action,omitempty"`
	Resource  realtimeResourceMessage `json:"resource,omitempty"`
	Data      json.RawMessage         `json:"data,omitempty"`
}

type realtimeResourceMessage struct {
	Type model.ResourceType `json:"type"`
	ID   string             `json:"id"`
}

func realtimeEventMessageFromModel(event Event) realtimeEventMessage {
	return realtimeEventMessage{
		ID:        event.ID,
		Name:      event.Name,
		UserID:    event.UserID.String(),
		SessionID: event.SessionID.String(),
		Action:    event.Action,
		Resource:  realtimeResourceMessage{Type: event.Resource.Type, ID: event.Resource.ID},
		Data:      append(json.RawMessage(nil), event.Data...),
	}
}

func (m realtimeEventMessage) toModel() Event {
	return Event{
		ID:        m.ID,
		Name:      m.Name,
		UserID:    model.UserID(m.UserID),
		SessionID: model.SessionID(m.SessionID),
		Action:    m.Action,
		Resource:  model.Resource{Type: m.Resource.Type, ID: m.Resource.ID},
		Data:      append(json.RawMessage(nil), m.Data...),
	}
}

func decodePayload(data []byte, target any) error {
	if len(data) == 0 {
		return errors.New("realtime cluster payload is empty")
	}
	return json.Unmarshal(data, target)
}

// ConnectionCloseReason is mapped by the socket layer to its close codes and
// text.
type ConnectionCloseReason string

// SessionRevoked closes the connections of a revoked session or user;
// AuthorizationChanged closes connections whose subscriptions may no longer be
// authorized.
const (
	ConnectionCloseSessionRevoked       ConnectionCloseReason = "session_revoked"
	ConnectionCloseAuthorizationChanged ConnectionCloseReason = "authorization_changed"
)

// AttachAssistantListener wakes local work after committed lifecycle changes.
func (s *Delivery) AttachAssistantListener(listener func(Event)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if listener == nil || s.assistantListener != nil {
		return errors.New("assistant listener is nil or already attached")
	}
	s.assistantListener = listener
	return nil
}

// NotifyAssistantChanged is a best-effort hint; the owner reads durable state.
func (s *Delivery) NotifyAssistantChanged(ctx context.Context, id model.ExamAttemptID) error {
	if !id.IsValid() {
		return errors.New("assistant wake attempt ID is invalid")
	}
	data, _ := json.Marshal(struct {
		AttemptID string `json:"exam_attempt_id"`
	}{id.String()})
	if err := s.handleAssistantChanged(ctx, data); err != nil {
		return err
	}
	return s.broadcast(ctx, "assistant.changed", data)
}
func (s *Delivery) handleAssistantChanged(_ context.Context, data []byte) error {
	var input struct {
		AttemptID string `json:"exam_attempt_id"`
	}
	if len(data) > 256 || json.Unmarshal(data, &input) != nil || !model.ExamAttemptID(input.AttemptID).IsValid() {
		return errors.New("invalid assistant wake")
	}
	s.mu.RLock()
	listener := s.assistantListener
	s.mu.RUnlock()
	if listener != nil {
		listener(Event{Name: "assistant_changed", Data: append(json.RawMessage(nil), data...)})
	}
	return nil
}
