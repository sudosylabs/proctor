// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	CommandOutcomeMaxBytes = 64 * 1024
)

// CommandIdempotency identifies one retryable command. Only digests of the
// client key and command cross the store boundary, never the raw values.
type CommandIdempotency struct {
	UserID      model.UserID
	Operation   string
	KeyDigest   [sha256.Size]byte
	Fingerprint [sha256.Size]byte
	Retention   time.Duration
	Wait        time.Duration
	// OnboardingImportID and OnboardingImportRowNumber are set only for an
	// import or progression row, whose row fence must commit with the
	// mutation.
	OnboardingImportID        model.OnboardingImportID
	OnboardingImportRowNumber int
	// Authorization is set only for academic-administration batch commands;
	// the store rechecks it in the mutation's transaction.
	Authorization *CommandAuthorization
	Batch         *CommandBatch
}

// CommandBatch ties one batch item's outcome to its duplicate group.
// DuplicateOfKeyDigest is zero for the canonical item; the store sets
// Duplicate when the retained outcome is not the canonical one.
type CommandBatch struct {
	GroupDigest          [sha256.Size]byte
	DuplicateOfKeyDigest [sha256.Size]byte
	Duplicate            bool
}

// CommandAuthorization is the authority one academic-administration batch row
// needs. It carries no role state: the store resolves every action from
// current bindings and roles.
type CommandAuthorization struct {
	Principal        model.Principal
	ScopeType        model.RoleScopeType
	ScopeID          string
	Actions          []model.Action
	DelegatedActions []model.Action
	// ClassMemberID lets a class end or transfer lock the user before the
	// hierarchy, then authorize against the membership's current class.
	ClassMemberID model.ClassMemberID
	// RecipientUserID keeps the user-before-hierarchy lock order for an
	// enrollment.
	RecipientUserID model.UserID
}

// CommandOutcomeStore expires retained command outcomes. Recording and
// replaying an outcome happen inside each mutation, not here.
type CommandOutcomeStore interface {
	Has(context.Context, *CommandIdempotency) (bool, error)
	DeleteExpired(context.Context, int) (int64, error)
}
