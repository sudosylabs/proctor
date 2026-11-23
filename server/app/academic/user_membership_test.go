// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type membershipPageUsers struct {
	store.UserStore
	read []store.UserMembershipListOptions
}

func (s *membershipPageUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	return &model.User{ID: id}, nil
}

func (s *membershipPageUsers) ListMemberships(_ context.Context, opts store.UserMembershipListOptions) ([]store.UserMembership, error) {
	s.read = append(s.read, opts)
	return []store.UserMembership{}, nil
}

func TestListUserMembershipsUsesCurrentMemberViewScopes(t *testing.T) {
	t.Parallel()
	at := model.NowUTC()
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: at, ClientType: model.SessionClientWeb,
	}
	unitID, classUnitID := model.NewAcademicUnitID(), model.NewAcademicUnitID()
	users := &membershipPageUsers{}
	fixture := accesstest.New(t, accesstest.Options{
		Users: users,
		Now:   func() time.Time { return at },
		Grants: []accesstest.Grant{
			{UserID: principal.UserID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(), Actions: []model.Action{model.ActionAcademicUnitMembersView}},
			{UserID: principal.UserID, Actions: []model.Action{model.ActionUserView}},
			{UserID: principal.UserID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: classUnitID.String(), Actions: []model.Action{model.ActionClassMembersView}},
		},
	})
	s := &Service{users: users, authorizer: fixture.Authorizer, now: func() time.Time { return at }}
	params := ListUserMembershipsQuery{UserID: principal.UserID, Limit: 8}
	page, err := s.ListUserMemberships(t.Context(), invocation.New(principal, model.RequestMetadata{}), params)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Equal(t, []store.UserMembershipListOptions{{
		UserID: principal.UserID, ActiveAt: at, Limit: 9,
		UnitVisibility:  store.ClassVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}},
		ClassVisibility: store.ClassVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{classUnitID}},
	}}, users.read)

	principal.UserID = model.NewUserID()
	_, err = s.ListUserMemberships(t.Context(), invocation.New(principal, model.RequestMetadata{}), params)
	require.True(t, apperror.Is(err, "authorization.denied"), "%v", err)
	require.Len(t, users.read, 1)
}
