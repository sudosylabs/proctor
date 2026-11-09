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

// RoleCreation is the durable input for creating a custom role under an
// already-persisted audit attempt.
type RoleCreation struct {
	Role         *model.Role
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// RoleUpdate is the durable input for updating a custom role under an
// already-persisted audit attempt.
type RoleUpdate struct {
	Role         *model.Role
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// RoleArchive is the durable input for archiving a custom role under an
// already-persisted audit attempt.
type RoleArchive struct {
	ID           model.RoleID
	ArchiveAt    time.Time
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// RoleStore persists reusable permission sets independently of their scopes.
type RoleStore interface {
	Save(context.Context, *RoleCreation) (*model.Role, error)
	Get(context.Context, model.RoleID) (*model.Role, error)
	GetIncludingArchived(context.Context, model.RoleID) (*model.Role, error)
	GetByName(context.Context, string) (*model.Role, error)
	GetByIDs(context.Context, []model.RoleID) ([]*model.Role, error)
	List(context.Context) ([]*model.Role, error)
	Update(context.Context, *RoleUpdate) (*model.Role, error)
	Archive(context.Context, *RoleArchive) (*model.Role, error)
	// SeedBuiltIn inserts the protected built-in Role for fixtures only;
	// production creates it through InstallationStore.Bootstrap.
	SeedBuiltIn(context.Context, *model.Role) (*model.Role, error)
	// SeedUpdate is an unaudited fixture write; production code never calls it.
	SeedUpdate(context.Context, *model.Role) (*model.Role, error)
}

// RoleBindingCreation is the durable input for creating a binding under an
// already-persisted audit attempt.
type RoleBindingCreation struct {
	Binding                   *model.RoleBinding
	ExpectedRoleUpdatedAt     time.Time
	ExpectedRolePermissions   []string
	ExpectedRecipientRevision int64
	Notice                    *PreparedMail
	AuditEventID              model.AuditEventID
	AuditAt                   time.Time
	Command                   *CommandIdempotency
	Replayed                  bool
	NoOp                      bool
}

// RoleBindingEnd is the durable input for ending a binding under an
// already-persisted audit attempt.
type RoleBindingEnd struct {
	ID                        model.RoleBindingID
	EndAt                     time.Time
	ExpectedRecipientRevision int64
	Notice                    *PreparedMail
	Capabilities              AccessDeploymentCapabilities
	AuditEventID              model.AuditEventID
	AuditAt                   time.Time
	Command                   *CommandIdempotency
	Replayed                  bool
	NoOp                      bool
}

// RoleBindingSummaryOptions selects the current Role Bindings of one page of
// Users whose scope lies within Visibility, as ListVisibleByUser defines it,
// at most Limit for each User. Current means effective at
// Visibility.ActiveAt, which is required, with an unarchived Role and scope.
// A binding outside Visibility is neither returned nor counted. A non-empty
// ScopeType with ScopeID further keeps only bindings on exactly that scope.
type RoleBindingSummaryOptions struct {
	UserIDs    []model.UserID
	Visibility UserVisibilityScope
	ScopeType  model.RoleScopeType
	ScopeID    string
	Limit      int
}

// UserRoleBindingSummary is one User's visible current Role Bindings ordered
// by start then ID; More counts the visible bindings past the limit.
type UserRoleBindingSummary struct {
	UserID   model.UserID
	Bindings []RoleBindingSummary
	More     int
}

// RoleBindingSummary names one Role Binding: the Role's display name and the
// display name of the Institution, Academic Unit or Class it is scoped to.
type RoleBindingSummary struct {
	RoleID      model.RoleID
	DisplayName string
	ScopeType   model.RoleScopeType
	ScopeID     string
	ScopeName   string
}

// RoleBindingStore persists time-bounded role assignments. Scope references
// are validated transactionally because PostgreSQL cannot express a foreign
// key whose target table depends on scope_type.
type RoleBindingStore interface {
	Save(context.Context, *RoleBindingCreation) (*model.RoleBinding, error)
	Get(context.Context, model.RoleBindingID) (*model.RoleBinding, error)
	ListByUser(context.Context, model.UserID) ([]*model.RoleBinding, error)
	ListVisibleByUser(context.Context, model.UserID, UserVisibilityScope) ([]*model.RoleBinding, error)
	// ListVisibleSummariesByUsers reads the visible current bindings of every
	// listed User in one statement. A User with none has no entry.
	ListVisibleSummariesByUsers(context.Context, RoleBindingSummaryOptions) ([]UserRoleBindingSummary, error)
	ListByScope(context.Context, model.RoleScopeType, string) ([]*model.RoleBinding, error)
	// ListActiveByUser uses UTC microsecond precision and the interval [start, end).
	// Archived bindings are excluded; no active bindings is an empty list.
	ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.RoleBinding, error)
	End(context.Context, *RoleBindingEnd) (*model.RoleBinding, error)
}
