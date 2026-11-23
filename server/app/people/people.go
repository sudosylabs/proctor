// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package people names the users and records a record refers to, such as who granted a
// Role or who paused a Sitting. Who acted is part of a record, so a reader of
// the record always gets the person's presented name; the username and the
// profile picture follow the reader's own user.view visibility.
package people

import (
	"context"
	"errors"
	"slices"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Ref is one person referenced in a record. A display name can be absent
// when an account was removed or a username is its only name and is hidden.
type Ref struct {
	ID          model.UserID
	DisplayName string
	// Username is set only when the reader can see the person through their
	// current user.view or class.members.view visibility.
	Username string
	Visible  bool
}

// Named reports whether the caller has a name it may show for this person.
func (r Ref) Named() bool { return r.DisplayName != "" }

// Refs maps each requested ID to its Ref. A missing key is a zero ID.
type Refs map[model.UserID]Ref

// Get returns the Ref for id, and false for a zero ID.
func (r Refs) Get(id model.UserID) (Ref, bool) {
	if id.IsZero() {
		return Ref{}, false
	}
	if ref, ok := r[id]; ok {
		return ref, true
	}
	return Ref{ID: id}, true
}

// Resolver reads names for one page at a time.
type Resolver struct {
	users        store.UserStore
	audits       store.AuditStore
	institutions store.InstitutionStore
	authorizer   *access.Authorizer
	// unavailable is the code a failed read is reported as: the one the
	// calling area's endpoints already declare.
	unavailable apperror.Code
}

// Reporting returns a copy of r that reports a failed read as code.
func (r *Resolver) Reporting(code apperror.Code) *Resolver {
	reporting := *r
	reporting.unavailable = code
	return &reporting
}

// fail reports a failed read as the configured code, when one is set.
func (r *Resolver) fail(err error) error {
	if r != nil && r.unavailable != "" {
		return apperror.New(r.unavailable).Wrap(err)
	}
	return err
}

// New returns a Resolver over the catalog. A service built without a user or
// audit store still builds; naming then fails, so only a projection that
// names people or records is unavailable.
func New(catalog store.Catalog, authorizer *access.Authorizer) *Resolver {
	if catalog == nil {
		return &Resolver{authorizer: authorizer}
	}
	return &Resolver{
		users: catalog.User(), audits: catalog.Audit(), institutions: catalog.Institution(), authorizer: authorizer,
	}
}

var errUnavailable = errors.New("people resolver is not configured")

// Resolve names the people an authorized record refers to. Resolve visibility
// once so even an import can name its people with bounded batches of reads.
func (r *Resolver) Resolve(ctx context.Context, principal model.Principal, ids []model.UserID) (Refs, error) {
	distinct := distinctUserIDs(ids)
	refs := make(Refs, len(distinct))
	if len(distinct) == 0 {
		return refs, nil
	}
	if r == nil || r.users == nil || r.authorizer == nil {
		return nil, r.fail(errUnavailable)
	}

	visibility, err := r.authorizer.GrantedUserVisibility(ctx, principal)
	if err != nil {
		return nil, r.fail(err)
	}
	for batch := range slices.Chunk(distinct, store.MaximumUserDisplayNames) {
		if err := r.resolveBatch(ctx, refs, batch, visibility); err != nil {
			return nil, r.fail(err)
		}
	}
	return refs, nil
}

func (r *Resolver) resolveBatch(ctx context.Context, refs Refs, ids []model.UserID, visibility store.UserVisibilityScope) error {
	names, err := r.users.ListDisplayNames(ctx, ids)
	if err != nil {
		return err
	}
	for _, name := range names {
		ref := Ref{ID: name.UserID}
		if !name.UsernameFallback {
			ref.DisplayName = name.DisplayName
		}
		refs[name.UserID] = ref
	}

	visible, err := r.users.ListVisibleDisplayNames(ctx, ids, visibility)
	if err != nil {
		return err
	}
	visibleIDs := make([]model.UserID, 0, len(visible))
	for _, name := range visible {
		visibleIDs = append(visibleIDs, name.UserID)
	}
	if len(visibleIDs) == 0 {
		return nil
	}
	identities, err := r.users.ListIdentities(ctx, visibleIDs)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		ref, ok := refs[identity.ID]
		if !ok {
			continue
		}
		ref.Username = identity.Username
		if ref.DisplayName == "" {
			ref.DisplayName = identity.Username
		}
		ref.Visible = true
		refs[identity.ID] = ref
	}
	return nil
}

func distinctUserIDs(ids []model.UserID) []model.UserID {
	seen := make(map[model.UserID]struct{}, len(ids))
	result := make([]model.UserID, 0, len(ids))
	for _, id := range ids {
		if id.IsZero() {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// Records lists the roles, units and classes a page of authorized records
// refers to, so they are named in one bounded read per kind.
type Records struct {
	Roles         []model.RoleID
	AcademicUnits []model.AcademicUnitID
	Classes       []model.ClassID
}

// RecordNames are the names Records found. Archived roles keep their names
// on historical references. A role keeps its machine name so the protected
// System Administrator role can be shown localized.
type RecordNames struct {
	Roles         map[model.RoleID]store.AuditRoleName
	AcademicUnits map[model.AcademicUnitID]string
	Classes       map[model.ClassID]string
}

// NameRecords names what an authorized record refers to. The reader already
// sees the IDs, so the names add nothing the reader could not learn; it
// grants nothing itself.
func (r *Resolver) NameRecords(ctx context.Context, records Records) (RecordNames, error) {
	if len(records.Roles)+len(records.AcademicUnits)+len(records.Classes) == 0 {
		return RecordNames{}, nil
	}
	if r == nil || r.audits == nil {
		return RecordNames{}, r.fail(errUnavailable)
	}
	names, err := r.audits.ListNames(ctx, store.AuditNameOptions{
		RoleIDs:                     records.Roles,
		IncludeArchivedRoles:        true,
		AcademicUnitIDs:             records.AcademicUnits,
		AcademicUnitInstitutionWide: true,
		ClassIDs:                    records.Classes,
		ClassVisibility:             store.ClassVisibilityScope{InstitutionWide: true},
	})
	if err != nil {
		return RecordNames{}, r.fail(err)
	}
	return RecordNames{Roles: names.Roles, AcademicUnits: names.AcademicUnits, Classes: names.Classes}, nil
}

// InstitutionName is the Institution's display name, for a record scoped to
// the Institution.
func (r *Resolver) InstitutionName(ctx context.Context) (string, error) {
	if r == nil || r.institutions == nil {
		return "", r.fail(errUnavailable)
	}
	institution, err := r.institutions.GetSingleton(ctx)
	if err != nil {
		return "", r.fail(err)
	}
	return institution.DisplayName, nil
}
