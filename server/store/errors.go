// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/store/errors.go. Proctor keeps a
// smaller error vocabulary and preserves wrapped driver errors for internal
// errors.Is/errors.As inspection.

package store

import (
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
)

// ErrAuthenticationMethodDisabled means the access policy stopped permitting
// the method before the final commit. It deliberately carries no provider or
// account detail.
var ErrAuthenticationMethodDisabled = errors.New("authentication method is disabled by current access policy")

// ErrPasswordCredentialChanged means a verified password proof no longer names
// the active credential. Public sign-in must report it as a generic failure.
var ErrPasswordCredentialChanged = errors.New("password credential has changed")

// ErrPasswordReused means the new password matches one of the user's retained
// recent passwords.
var ErrPasswordReused = errors.New("password matches a recent password")

// ErrLastUsableAuthenticationMethod means removing the credential would leave
// the user no method the policy and deployment admit.
var ErrLastUsableAuthenticationMethod = errors.New("cannot remove the last usable authentication method")

// ErrInvalidState means stored data failed validation on read. Error mapping
// must check for it before inspecting validation causes.
var ErrInvalidState = errors.New("invalid persisted state")

// ErrInvalidInput means the store was given a value it cannot persist.
type ErrInvalidInput struct {
	Entity     string
	Field      string
	Value      any
	cause      error
	constraint bool
}

func NewErrInvalidInput(entity, field string, value any) *ErrInvalidInput {
	return &ErrInvalidInput{Entity: entity, Field: field, Value: value}
}

// NewErrConstraintViolation reports a write the database rejected under a
// CHECK, NOT NULL or length constraint; field names the constraint or column.
// A concurrent transition or clock skew can trip one, so unlike other invalid
// input the same request may succeed when retried.
func NewErrConstraintViolation(entity, field string) *ErrInvalidInput {
	return &ErrInvalidInput{Entity: entity, Field: field, constraint: true}
}

// IsConstraintViolation reports whether err holds invalid input the database,
// not the store, rejected.
func IsConstraintViolation(err error) bool {
	for err != nil {
		var invalid *ErrInvalidInput
		if !errors.As(err, &invalid) {
			return false
		}
		if invalid.constraint {
			return true
		}
		err = invalid.cause
	}
	return false
}

func (e *ErrInvalidInput) Error() string {
	return fmt.Sprintf("invalid input: entity=%s field=%s", e.Entity, e.Field)
}

func (e *ErrInvalidInput) Wrap(cause error) *ErrInvalidInput {
	e.cause = cause
	return e
}

func (e *ErrInvalidInput) Unwrap() error {
	return e.cause
}

// ErrNotFound means the resource does not exist.
type ErrNotFound struct {
	Resource string
	ID       string
	cause    error
}

func NewErrNotFound(resource, id string) *ErrNotFound {
	return &ErrNotFound{Resource: resource, ID: id}
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("resource %q not found: %s", e.Resource, e.ID)
}

func (e *ErrNotFound) Wrap(cause error) *ErrNotFound {
	e.cause = cause
	return e
}

func (e *ErrNotFound) Unwrap() error {
	return e.cause
}

// ErrConflict means a uniqueness or state conflict; Constraint names which.
type ErrConflict struct {
	Resource   string
	Constraint string
	cause      error
}

// ErrIdempotencyConflict means a live command key was reused with different
// input.
type ErrIdempotencyConflict struct{}

func (*ErrIdempotencyConflict) Error() string {
	return "idempotency key conflicts with another command"
}

// ErrIdempotencyInProgress means another transaction still held the same
// command when the bounded wait ran out.
type ErrIdempotencyInProgress struct{}

func (*ErrIdempotencyInProgress) Error() string { return "idempotent command is still in progress" }

// ErrUserSettingsRevisionConflict carries only the current opaque revision,
// never the settings themselves.
type ErrUserSettingsRevisionConflict struct {
	CurrentRevision model.UserSettingsRevision
}

func (e *ErrUserSettingsRevisionConflict) Error() string {
	return "user settings revision conflicts with current state"
}

func NewErrConflict(resource, constraint string, cause error) *ErrConflict {
	return &ErrConflict{Resource: resource, Constraint: constraint, cause: cause}
}

func (e *ErrConflict) Error() string {
	if e.Constraint == "" {
		return fmt.Sprintf("resource %q conflicts with existing state", e.Resource)
	}
	return fmt.Sprintf("resource %q conflicts with constraint %q", e.Resource, e.Constraint)
}

func (e *ErrConflict) Unwrap() error {
	return e.cause
}

// ErrReference means a write violated a foreign key.
type ErrReference struct {
	Resource   string
	Constraint string
	cause      error
}

func NewErrReference(resource, constraint string, cause error) *ErrReference {
	return &ErrReference{Resource: resource, Constraint: constraint, cause: cause}
}

func (e *ErrReference) Error() string {
	return fmt.Sprintf("resource %q has an invalid reference (%s)", e.Resource, e.Constraint)
}

func (e *ErrReference) Unwrap() error {
	return e.cause
}

func IsNotFound(err error) bool {
	var target *ErrNotFound
	return errors.As(err, &target)
}

func IsConflict(err error) bool {
	var target *ErrConflict
	return errors.As(err, &target)
}

// IsUserIdentityConflict reports whether err means the username or email is
// already taken. Public entry points use it so they neither learn constraint
// names nor hide unrelated failures.
func IsUserIdentityConflict(err error) bool {
	var target *ErrConflict
	if !errors.As(err, &target) || target.Resource != "user" {
		return false
	}
	return target.Constraint == "users_username_key" || target.Constraint == "users_email_key"
}
