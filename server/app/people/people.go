// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package people names the users a record refers to, such as who granted a
// Role or who paused a Sitting. Who acted is part of a record, so a reader of
// the record always gets the person's presented name; the username and the
// profile picture follow the reader's own user.view visibility.
package people

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Ref is one named person. A zero DisplayName means the account was removed:
// the ID is kept so a client can still tell two former users apart.
type Ref struct {
	ID          model.UserID
	DisplayName string
	// Username is set only when the reader can see the person through their
	// current user.view or class.members.view visibility.
	Username string
	Visible  bool
}

// Removed reports whether the account no longer exists to be named.
func (r Ref) Removed() bool { return r.DisplayName == "" }

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
	users      store.UserStore
	authorizer *access.Authorizer
}

// New returns a Resolver. A service built without a user store still builds;
// naming then fails, so only a projection that names people is unavailable.
func New(users store.UserStore, authorizer *access.Authorizer) *Resolver {
	return &Resolver{users: users, authorizer: authorizer}
}

var errUnavailable = errors.New("people resolver is not configured")

// Resolve names up to store.MaximumUserDisplayNames distinct users in at most
// three bounded reads, whatever the page size. Call it only for a record the
// principal is already authorized to read; it grants nothing itself.
func (r *Resolver) Resolve(ctx context.Context, principal model.Principal, ids []model.UserID) (Refs, error) {
	distinct := distinctUserIDs(ids)
	refs := make(Refs, len(distinct))
	if len(distinct) == 0 {
		return refs, nil
	}
	if r == nil || r.users == nil || r.authorizer == nil {
		return nil, errUnavailable
	}
	if len(distinct) > store.MaximumUserDisplayNames {
		return nil, errors.New("too many people to name in one read")
	}

	names, err := r.users.ListDisplayNames(ctx, distinct)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		refs[name.UserID] = Ref{ID: name.UserID, DisplayName: name.DisplayName}
	}

	visibility, err := r.authorizer.GrantedUserVisibility(ctx, principal)
	if err != nil {
		return nil, err
	}
	visible, err := r.users.ListVisibleDisplayNames(ctx, distinct, visibility)
	if err != nil {
		return nil, err
	}
	visibleIDs := make([]model.UserID, 0, len(visible))
	for _, name := range visible {
		visibleIDs = append(visibleIDs, name.UserID)
	}
	if len(visibleIDs) == 0 {
		return refs, nil
	}
	identities, err := r.users.ListIdentities(ctx, visibleIDs)
	if err != nil {
		return nil, err
	}
	for _, identity := range identities {
		ref, ok := refs[identity.ID]
		if !ok {
			continue
		}
		ref.Username = identity.Username
		ref.Visible = true
		refs[identity.ID] = ref
	}
	return refs, nil
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
