// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package operational carries bounded use-case outcomes to metrics. It is a
// leaf so every application package can report without importing the
// application root or Prometheus.
package operational

import (
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/store"
)

// Event is a bounded application outcome. Every value is chosen by
// application code, so it never carries a resource, principal, request or
// error text.
type Event struct {
	subsystem string
	event     string
	outcome   string
}

// NewEvent classifies err into a bounded outcome label.
func NewEvent(subsystem, event string, err error) Event {
	return Event{subsystem: subsystem, event: event, outcome: outcome(err)}
}

// Subsystem is the application area that emitted the event.
func (e Event) Subsystem() string { return e.subsystem }

// Event is what happened within the subsystem.
func (e Event) Event() string { return e.event }

// Outcome is a bounded classification of the result, never error text.
func (e Event) Outcome() string { return e.outcome }

// Recorder receives operational events; metrics implements it.
type Recorder interface {
	RecordOperationalEvent(Event)
}

// Record reports one use-case outcome. A nil recorder records nothing.
func Record(recorder Recorder, subsystem, event string, err error) {
	if recorder != nil {
		recorder.RecordOperationalEvent(NewEvent(subsystem, event, err))
	}
}

func outcome(err error) string {
	if err == nil {
		return "success"
	}
	failure, ok := apperror.As(err)
	if !ok {
		return "error"
	}
	switch failure.Code() {
	case "authorization.denied":
		return "denied"
	case "authentication.credentials_invalid", "authentication.token_invalid":
		return "rejected"
	case "authentication.rate_limited":
		return "rate_limited"
	case "resource.not_found", "user.not_found":
		return "not_found"
	}
	var conflict *store.ErrConflict
	if errors.As(err, &conflict) {
		return "conflict"
	}
	return "error"
}
