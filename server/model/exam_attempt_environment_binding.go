// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"time"
)

const (
	// AttemptEnvironmentGrantIDMaximumBytes bounds the opaque execution
	// environment grant identity.
	AttemptEnvironmentGrantIDMaximumBytes = 64
	// AttemptEnvironmentEpochMaximumBytes bounds the opaque grant epoch.
	AttemptEnvironmentEpochMaximumBytes = 128
)

// IsValidAttemptEnvironmentGrantID reports whether value is a grant identity
// of 1..64 bytes drawn from [A-Za-z0-9._:-].
func IsValidAttemptEnvironmentGrantID(value string) bool {
	return validEnvironmentToken(value, AttemptEnvironmentGrantIDMaximumBytes)
}

// IsValidAttemptEnvironmentEpoch reports whether value is a grant epoch of
// 1..128 bytes drawn from [A-Za-z0-9._:-].
func IsValidAttemptEnvironmentEpoch(value string) bool {
	return validEnvironmentToken(value, AttemptEnvironmentEpochMaximumBytes)
}

func validEnvironmentToken(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum {
		return false
	}
	for index := 0; index < len(value); index++ {
		c := value[index]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}

// AttemptEnvironmentIdentity is the opaque identity an execution environment
// knows itself by: the grant, its epoch and the owner's incarnation counter.
// It carries no authority of its own; only a live binding to the Attempt's
// current Participation generation makes it a writer.
type AttemptEnvironmentIdentity struct {
	GrantID     string
	Epoch       string
	Incarnation int64
}

// Validate checks the bounded opaque identity.
func (i AttemptEnvironmentIdentity) Validate() error {
	if !IsValidAttemptEnvironmentGrantID(i.GrantID) ||
		!IsValidAttemptEnvironmentEpoch(i.Epoch) ||
		i.Incarnation < 0 {
		return invalidModelError(
			"AttemptEnvironmentIdentity.Validate",
			"attempt_environment_binding",
			"",
			"invalid execution environment identity",
			"",
		)
	}
	return nil
}

// AttemptEnvironmentBinding ties one execution environment identity to an
// Attempt and one of its Participation generations. At most one binding per
// Attempt is live (EndedAt unset). It ends when its generation ends, when the
// owner ends it, or when a newer incarnation for the same generation replaces
// it. While live it lets the environment write the Attempt Workspace on the
// candidate's behalf.
type AttemptEnvironmentBinding struct {
	ID              AttemptEnvironmentBindingID
	AttemptID       ExamAttemptID
	ParticipationID AttemptParticipationID
	Generation      int64
	Identity        AttemptEnvironmentIdentity
	CreatedAt       time.Time
	EndedAt         OptionalTime
}

// Live reports whether the binding has not ended.
func (b *AttemptEnvironmentBinding) Live() bool { return b != nil && !b.EndedAt.IsSet() }

// Validate checks rehydrated binding state.
func (b *AttemptEnvironmentBinding) Validate() error {
	if b == nil ||
		!b.ID.IsValid() ||
		!b.AttemptID.IsValid() ||
		!b.ParticipationID.IsValid() ||
		b.Generation < 1 ||
		b.Identity.Validate() != nil ||
		b.CreatedAt.IsZero() ||
		(b.EndedAt.IsSet() && b.EndedAt.Time.Before(b.CreatedAt)) {
		return invalidModelError(
			"AttemptEnvironmentBinding.Validate",
			"attempt_environment_binding",
			"",
			"invalid execution environment binding",
			"",
		)
	}
	return nil
}
