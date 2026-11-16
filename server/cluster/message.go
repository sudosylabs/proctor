// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// This file contains substantially modified code adapted from Mattermost's
// public cluster-message contract. See server/NOTICE for exact provenance.

package cluster

import (
	"errors"
	"fmt"
)

const (
	MaxEventBytes     = 128
	MaxMessageBytes   = 1 << 20
	MaxMessageProps   = 32
	MaxPropKeyBytes   = 64
	MaxPropValueBytes = 1024
)

// Event is a typed inter-node event name.
type Event string

// EventNone is never valid on the wire. Application events are declared by the
// modules that own them, not here.
const (
	EventNone Event = "none"
)

// Message is the payload a handler sees; the transport keeps protocol version,
// source and target out of it. There is no reliable send class.
type Message struct {
	Event Event             `json:"event"`
	Data  []byte            `json:"data,omitempty"`
	Props map[string]string `json:"props,omitempty"`
}

// Clone returns a deep copy, so concurrent handlers cannot share Data or Props.
func (m *Message) Clone() *Message {
	if m == nil {
		return nil
	}
	cloned := *m
	cloned.Data = append([]byte(nil), m.Data...)
	if m.Props != nil {
		cloned.Props = make(map[string]string, len(m.Props))
		for key, value := range m.Props {
			cloned.Props[key] = value
		}
	}
	return &cloned
}

// Validate checks names and size limits, which matter for messages from peers.
func (m *Message) Validate() error {
	if m == nil {
		return errors.New("cluster message is nil")
	}
	if !validEvent(m.Event) {
		return fmt.Errorf("invalid cluster event %q", m.Event)
	}
	if len(m.Data) > MaxMessageBytes {
		return fmt.Errorf("cluster message data exceeds %d bytes", MaxMessageBytes)
	}
	if len(m.Props) > MaxMessageProps {
		return fmt.Errorf("cluster message properties exceed %d entries", MaxMessageProps)
	}
	for key, value := range m.Props {
		if len(key) == 0 || len(key) > MaxPropKeyBytes || !validName(key) {
			return fmt.Errorf("invalid cluster message property key %q", key)
		}
		if len(value) > MaxPropValueBytes {
			return fmt.Errorf("cluster message property %q exceeds %d bytes", key, MaxPropValueBytes)
		}
	}
	return nil
}

func validEvent(event Event) bool {
	value := string(event)
	return event != EventNone &&
		len(value) > 0 &&
		len(value) <= MaxEventBytes &&
		validName(value)
}

func validName(value string) bool {
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
