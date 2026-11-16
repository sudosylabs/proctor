// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type PersonalAccessTokenResolution struct {
	Token *model.PersonalAccessToken
	User  *model.User
}

// PersonalAccessTokenSecurityNotice is the mail sent with a successful token
// change. It deliberately carries neither the token, its hash nor the full
// scope list.
type PersonalAccessTokenSecurityNotice struct {
	Occurrence *model.MailOccurrence
	Delivery   *model.MailDelivery
	Job        *model.Job
	// ExpiresAt is the expiry rendered into the mail; the store checks it
	// equals the committed token deadline.
	ExpiresAt time.Time
}

type PersonalAccessTokenMutationKind string

const (
	PersonalAccessTokenMutationCreate  PersonalAccessTokenMutationKind = "create"
	PersonalAccessTokenMutationEnable  PersonalAccessTokenMutationKind = "enable"
	PersonalAccessTokenMutationDisable PersonalAccessTokenMutationKind = "disable"
	PersonalAccessTokenMutationRevoke  PersonalAccessTokenMutationKind = "revoke"
)

// PersonalAccessTokenMutationPreparation is recorded before the notice is
// rendered; the store sets ActionAt. AuditEvent holds no secret and is written
// once, when the preparation succeeds, fails, or is reconciled after expiry.
type PersonalAccessTokenMutationPreparation struct {
	UserID     model.UserID
	TokenID    model.PersonalAccessTokenID
	Kind       PersonalAccessTokenMutationKind
	AuditEvent *model.AuditEvent
	Lifetime   time.Duration
}

type PreparedPersonalAccessTokenMutation struct {
	ID        model.PersonalAccessTokenPreparationID
	ActionAt  time.Time
	ExpiresAt time.Time
}

type PersonalAccessTokenMutationFailure struct {
	PreparationID model.PersonalAccessTokenPreparationID
	ErrorCode     string
}

type PersonalAccessTokenPreparationMaintenanceResult struct {
	Failed int
	More   bool
}

type PersonalAccessTokenMutationResult struct {
	Token *model.PersonalAccessToken
	Fresh bool
}

// PersonalAccessTokenCreationMutation stores the hashed token, its audit and
// its notice in one transaction.
type PersonalAccessTokenCreationMutation struct {
	Token           *model.PersonalAccessToken
	MaximumActive   int
	MinimumLifetime time.Duration
	MaximumLifetime time.Duration
	PreparationID   model.PersonalAccessTokenPreparationID
	Notice          PersonalAccessTokenSecurityNotice
}

// PersonalAccessTokenStateMutation enables or disables a token with its audit
// and notice in one transaction.
type PersonalAccessTokenStateMutation struct {
	ID            model.PersonalAccessTokenID
	UserID        model.UserID
	Disabled      bool
	MaximumActive int
	PreparationID model.PersonalAccessTokenPreparationID
	Notice        PersonalAccessTokenSecurityNotice
}

// PersonalAccessTokenRevocation revokes a token with its audit and notice in
// one transaction.
type PersonalAccessTokenRevocation struct {
	ID            model.PersonalAccessTokenID
	UserID        model.UserID
	PreparationID model.PersonalAccessTokenPreparationID
	Notice        PersonalAccessTokenSecurityNotice
}

// PersonalAccessTokenStore persists hashed, scoped tokens. Resolve also
// updates last-used, debounced. Times compare at UTC microsecond precision and
// expiry is exclusive.
type PersonalAccessTokenStore interface {
	PrepareMutation(context.Context, *PersonalAccessTokenMutationPreparation) (*PreparedPersonalAccessTokenMutation, error)
	FailMutation(context.Context, *PersonalAccessTokenMutationFailure) error
	MaintainMutationPreparations(context.Context, int) (*PersonalAccessTokenPreparationMaintenanceResult, error)
	Create(context.Context, *PersonalAccessTokenCreationMutation) (*PersonalAccessTokenMutationResult, error)
	Get(context.Context, model.PersonalAccessTokenID) (*model.PersonalAccessToken, error)
	ListByUser(context.Context, model.UserID) ([]*model.PersonalAccessToken, error)
	Resolve(context.Context, string, time.Time, time.Duration) (*PersonalAccessTokenResolution, error)
	ChangeState(context.Context, *PersonalAccessTokenStateMutation) (*PersonalAccessTokenMutationResult, error)
	Revoke(context.Context, *PersonalAccessTokenRevocation) (*PersonalAccessTokenMutationResult, error)
}
