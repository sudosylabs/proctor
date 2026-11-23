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

// RoleCreation creates a custom role and completes its recorded audit attempt.
type RoleCreation struct {
	Role         *model.Role
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// RoleUpdate updates a custom role and completes its recorded audit attempt.
type RoleUpdate struct {
	Role         *model.Role
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// RoleArchive archives a custom role and completes its recorded audit attempt.
type RoleArchive struct {
	ID           model.RoleID
	ArchiveAt    time.Time
	ArchivedBy   model.UserID
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// RoleStore persists permission sets; scopes belong to role bindings.
type RoleStore interface {
	Save(context.Context, *RoleCreation) (*model.Role, error)
	Get(context.Context, model.RoleID) (*model.Role, error)
	GetIncludingArchived(context.Context, model.RoleID) (*model.Role, error)
	GetByName(context.Context, string) (*model.Role, error)
	GetByIDs(context.Context, []model.RoleID) ([]*model.Role, error)
	List(context.Context) ([]*model.Role, error)
	Update(context.Context, *RoleUpdate) (*model.Role, error)
	Archive(context.Context, *RoleArchive) (*model.Role, error)
	// SeedBuiltIn inserts the built-in role for test fixtures only; production
	// creates it through InstallationStore.Bootstrap.
	SeedBuiltIn(context.Context, *model.Role) (*model.Role, error)
	// SeedUpdate is an unaudited test fixture write; production code never
	// calls it.
	SeedUpdate(context.Context, *model.Role) (*model.Role, error)
}

// RoleBindingCreation creates a binding and completes its recorded audit
// attempt.
type RoleBindingCreation struct {
	Binding                 *model.RoleBinding
	ExpectedRoleUpdatedAt   time.Time
	ExpectedRolePermissions []string
	// Recipient is the User the Notice was written for; the write refuses it
	// if anything the mail was written from has changed since.
	Recipient    *model.User
	Notice       *PreparedMail
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Command      *CommandIdempotency
	Replayed     bool
	NoOp         bool
}

// RoleBindingEnd ends a binding and completes its recorded audit attempt.
type RoleBindingEnd struct {
	ID    model.RoleBindingID
	EndAt time.Time
	// Recipient is the User the Notice was written for; the write refuses it
	// if anything the mail was written from has changed since.
	Recipient    *model.User
	Notice       *PreparedMail
	Capabilities AccessDeploymentCapabilities
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Command      *CommandIdempotency
	Replayed     bool
	NoOp         bool
}

// RoleBindingSummaryOptions selects, for a page of users, up to Limit current
// bindings each whose scope lies within Visibility (as ListVisibleByUser
// defines it). Current means effective at Visibility.ActiveAt, which is
// required, with an unarchived role and scope. A binding outside Visibility is
// neither returned nor counted. ScopeType with ScopeID narrows to one scope.
type RoleBindingSummaryOptions struct {
	UserIDs    []model.UserID
	Visibility UserVisibilityScope
	ScopeType  model.RoleScopeType
	ScopeID    string
	Limit      int
}

// UserRoleBindingSummary is one user's visible current bindings, by start then
// ID; More counts the visible bindings past the limit.
type UserRoleBindingSummary struct {
	UserID   model.UserID
	Bindings []RoleBindingSummary
	More     int
}

// RoleBindingSummary is a binding's role name and the name of the
// institution, academic unit or class it is scoped to.
type RoleBindingSummary struct {
	RoleID      model.RoleID
	DisplayName string
	ScopeType   model.RoleScopeType
	ScopeID     string
	ScopeName   string
}

// MaximumRoleBindingListRoles is the most roles one ListCurrentByRoles call
// accepts.
const MaximumRoleBindingListRoles = 16

// RoleBindingRoleListOptions selects the visible current bindings (as
// RoleBindingSummaryOptions defines both) of any of RoleIDs, newest first by
// created_at then ID. BeforeCreatedAt and BeforeID are both set or both zero.
// Limit (1 to 201) includes the one-row look-ahead.
type RoleBindingRoleListOptions struct {
	RoleIDs         []model.RoleID
	Visibility      UserVisibilityScope
	BeforeCreatedAt time.Time
	BeforeID        model.RoleBindingID
	Limit           int
}

// RoleHolderCountOptions counts, for each of up to 200 RoleIDs, the visible
// current bindings (as RoleBindingSummaryOptions defines both). Maximum (1 to
// MaximumListCount) caps each role's count.
type RoleHolderCountOptions struct {
	RoleIDs    []model.RoleID
	Visibility UserVisibilityScope
	Maximum    int
}

// RoleHolderCount is one role's visible current holders. Bindings is capped
// at Maximum; Users and Scopes count distinct holders and scopes among those
// bindings only, so they are lower bounds when Bindings reached Maximum.
type RoleHolderCount struct {
	RoleID   model.RoleID
	Bindings int
	Users    int
	Scopes   int
}

// MaximumClassTeacherClasses is the most classes one ListTeachersByClasses
// call reads: one page of a class listing.
const MaximumClassTeacherClasses = 200

// ClassTeacherOptions selects, for a page of classes, up to Limit (1 to 10)
// teachers each, as current at ActiveAt.
type ClassTeacherOptions struct {
	ClassIDs []model.ClassID
	ActiveAt time.Time
	Limit    int
}

// ClassTeachers is one class's teachers: the distinct people holding a
// current Role Binding of an unarchived role at the class itself, by sort name
// then ID. More counts the teachers past the limit.
type ClassTeachers struct {
	ClassID model.ClassID
	UserIDs []model.UserID
	More    int
}

// RoleBindingStore persists time-bounded role assignments. Scope references
// are checked in the transaction because PostgreSQL cannot express a foreign
// key whose target table depends on scope_type.
type RoleBindingStore interface {
	Save(context.Context, *RoleBindingCreation) (*model.RoleBinding, error)
	Get(context.Context, model.RoleBindingID) (*model.RoleBinding, error)
	ListByUser(context.Context, model.UserID) ([]*model.RoleBinding, error)
	ListVisibleByUser(context.Context, model.UserID, UserVisibilityScope) ([]*model.RoleBinding, error)
	// ListVisibleSummariesByUsers omits a user with no visible current
	// binding.
	ListVisibleSummariesByUsers(context.Context, RoleBindingSummaryOptions) ([]UserRoleBindingSummary, error)
	ListByScope(context.Context, model.RoleScopeType, string) ([]*model.RoleBinding, error)
	// ListTeachersByClasses omits a class with no teacher.
	ListTeachersByClasses(context.Context, ClassTeacherOptions) ([]ClassTeachers, error)
	// CountCurrentByRoles counts what ListCurrentByRoles would return across
	// every page, ignoring the keyset and limit, capped at maximum (see
	// MaximumListCount).
	ListCurrentByRoles(context.Context, RoleBindingRoleListOptions) ([]*model.RoleBinding, error)
	CountCurrentByRoles(context.Context, RoleBindingRoleListOptions, int) (int, error)
	// CountHoldersByRoles omits a role with no visible current holder.
	CountHoldersByRoles(context.Context, RoleHolderCountOptions) ([]RoleHolderCount, error)
	// ListActiveByUser compares at UTC microsecond precision over [start, end)
	// and excludes archived bindings.
	ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.RoleBinding, error)
	End(context.Context, *RoleBindingEnd) (*model.RoleBinding, error)
}
