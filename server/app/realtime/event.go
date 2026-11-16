// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
)

// Event is a past-tense fact published after commit. It has no sequence:
// each connection stamps its own at the socket.
type Event struct {
	ID       string
	Name     string
	UserID   model.UserID
	Action   model.Action
	Resource model.Resource
	Data     json.RawMessage
}

const (
	maxRealtimeEventNameBytes = 128
	maxRealtimeEventDataBytes = 256 << 10
)

// Clone returns a deep copy safe for concurrent local and cluster delivery.
func (e Event) Clone() Event {
	cloned := e
	if e.Data != nil {
		cloned.Data = append(json.RawMessage(nil), e.Data...)
	}
	return cloned
}

// ValidateForPublish checks the event's invariants without I/O.
func (e Event) ValidateForPublish() error {
	if e.ID != "" && !model.IsValidOpaqueID(e.ID) {
		return errors.New("realtime event ID is invalid")
	}

	if len(e.Name) == 0 ||
		len(e.Name) > maxRealtimeEventNameBytes ||
		!validRealtimeName(e.Name) {
		return fmt.Errorf("invalid realtime event %q", e.Name)
	}

	if e.UserID != "" && !e.UserID.IsValid() {
		return errors.New("realtime event user ID is invalid")
	}

	if e.Action == "" && e.Resource == (model.Resource{}) {
		if e.UserID == "" {
			return errors.New("realtime event requires a user target or authorized resource")
		}
	} else {
		// An event may address an owning scope the action accepts, as an
		// onboarding import's academic unit or class.
		definition, ok := model.DefinitionForAction(e.Action)
		if !ok || e.Resource.Validate() != nil || !definition.AcceptsResource(e.Resource.Type) {
			return errors.New("realtime event authorization target is invalid")
		}
	}

	if len(e.Data) > maxRealtimeEventDataBytes {
		return fmt.Errorf("realtime event data exceeds %d bytes", maxRealtimeEventDataBytes)
	}
	if len(e.Data) != 0 && !json.Valid(e.Data) {
		return errors.New("realtime event data is not valid JSON")
	}

	return nil
}

func validRealtimeName(value string) bool {
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' &&
			character != '_' &&
			character != '-' &&
			character != ':' {
			return false
		}
	}

	return true
}
