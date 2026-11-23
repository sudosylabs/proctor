// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListUserMembershipsQuery pages one user's current memberships. A
// continuation sets all three After fields from the previous page's last
// entry.
type ListUserMembershipsQuery struct {
	UserID       model.UserID
	Limit        int
	AfterKind    store.UserMembershipKind
	AfterStartAt time.Time
	AfterID      string
}

// ListUserMemberships pages a user's current unit memberships and class
// enrolments. The user must be visible to the caller, and each entry is
// listed only where the caller may view that unit's or class's members, so a
// hidden membership is neither shown nor counted.
func (s *Service) ListUserMemberships(
	ctx context.Context,
	inv invocation.Invocation,
	query ListUserMembershipsQuery,
) (paging.Page[store.UserMembership], error) {
	if !query.UserID.IsValid() {
		return paging.Page[store.UserMembership]{}, apperror.Invalid("request.invalid", "user_id")
	}
	limit, probe, err := paging.ResolveLimit(query.Limit, "limit")
	if err != nil {
		return paging.Page[store.UserMembership]{}, err
	}
	continued := query.AfterID != "" || query.AfterKind != "" || !query.AfterStartAt.IsZero()
	if continued && (!query.AfterKind.IsValid() || !model.IsValidOpaqueID(query.AfterID) || query.AfterStartAt.IsZero()) {
		return paging.Page[store.UserMembership]{}, apperror.Invalid("request.invalid", "cursor")
	}

	if err = s.authorizer.AuthorizeUserRead(ctx, inv, query.UserID); err != nil {
		return paging.Page[store.UserMembership]{}, err
	}

	at := model.TimeUTC(s.now())
	units, err := s.authorizer.GrantedScopesAt(ctx, inv.Principal(), model.ActionAcademicUnitMembersView, model.ResourceAcademicUnit, at)
	if err != nil {
		return paging.Page[store.UserMembership]{}, err
	}
	classes, err := s.authorizer.GrantedScopesAt(ctx, inv.Principal(), model.ActionClassMembersView, model.ResourceClass, at)
	if err != nil {
		return paging.Page[store.UserMembership]{}, err
	}

	memberships, err := s.users.ListMemberships(ctx, store.UserMembershipListOptions{
		UserID:   query.UserID,
		ActiveAt: at,
		UnitVisibility: store.ClassVisibilityScope{
			InstitutionWide:     units.InstitutionWide,
			AcademicUnitRootIDs: units.AcademicUnitRootIDs,
		},
		ClassVisibility: store.ClassVisibilityScope{
			InstitutionWide:     classes.InstitutionWide,
			AcademicUnitRootIDs: classes.AcademicUnitRootIDs,
			ClassIDs:            classes.ClassIDs,
		},
		AfterKind:    query.AfterKind,
		AfterStartAt: query.AfterStartAt,
		AfterID:      query.AfterID,
		Limit:        probe,
	})
	if err != nil {
		return paging.Page[store.UserMembership]{}, userMembershipStoreErrors.Translate(err)
	}
	return paging.New(memberships, limit), nil
}

var userMembershipStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Invalid:     "request.invalid",
	Unavailable: "administration.unavailable",
}
