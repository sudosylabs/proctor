// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package people

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type personProjectionStore struct {
	store.UserStore
	display             map[model.UserID]string
	fallback            []model.UserID
	visible             model.UserID
	nameReadSizes       []int
	visibilityReadSizes []int
	identityReads       []model.UserID
}

func (s *personProjectionStore) ListDisplayNames(_ context.Context, ids []model.UserID) ([]store.UserDisplayName, error) {
	s.nameReadSizes = append(s.nameReadSizes, len(ids))
	result := make([]store.UserDisplayName, 0, len(ids))
	for _, id := range ids {
		if name, ok := s.display[id]; ok {
			result = append(result, store.UserDisplayName{UserID: id, DisplayName: name, UsernameFallback: slices.Contains(s.fallback, id)})
		}
	}
	return result, nil
}

func (s *personProjectionStore) ListVisibleDisplayNames(_ context.Context, ids []model.UserID, _ store.UserVisibilityScope) ([]store.UserDisplayName, error) {
	s.visibilityReadSizes = append(s.visibilityReadSizes, len(ids))
	if slices.Contains(ids, s.visible) {
		return []store.UserDisplayName{{UserID: s.visible, DisplayName: s.display[s.visible]}}, nil
	}
	return nil, nil
}

func (s *personProjectionStore) ListIdentities(_ context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	s.identityReads = append(s.identityReads, ids...)
	return []store.UserIdentity{{ID: s.visible, Username: "known-person"}}, nil
}

func TestResolveNamesBatchesWithoutRepeatingAuthorization(t *testing.T) {
	t.Parallel()
	caller := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, ClientType: model.SessionClientWeb, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: time.Now().UTC(),
	}
	visible, hidden, removed := model.NewUserID(), model.NewUserID(), model.NewUserID()
	users := &personProjectionStore{
		display: map[model.UserID]string{visible: "Visible Person", hidden: "Hidden Person"}, visible: visible,
	}
	grants := 0
	fixture := accesstest.New(t, accesstest.Options{Users: users, Observe: func(event string) {
		if event == "grants" {
			grants++
		}
	}})
	resolver := &Resolver{users: users, authorizer: fixture.Authorizer}
	_, err := resolver.Resolve(t.Context(), caller, []model.UserID{visible})
	require.NoError(t, err)
	singleResolutionReads := grants
	require.Positive(t, singleResolutionReads)
	grants = 0
	users.nameReadSizes, users.visibilityReadSizes, users.identityReads = nil, nil, nil

	ids := make([]model.UserID, store.MaximumUserDisplayNames+1)
	for i := range ids {
		ids[i] = model.NewUserID()
	}
	ids[0], ids[1], ids[len(ids)-1] = visible, hidden, removed
	refs, err := resolver.Resolve(t.Context(), caller, append(ids, visible, ""))
	require.NoError(t, err)
	require.Equal(t, singleResolutionReads, grants)
	require.Equal(t, []int{store.MaximumUserDisplayNames, 1}, users.nameReadSizes)
	require.Equal(t, users.nameReadSizes, users.visibilityReadSizes)
	require.Equal(t, []model.UserID{visible}, users.identityReads)
	require.Equal(t, Ref{ID: visible, DisplayName: "Visible Person", Username: "known-person", Visible: true}, refs[visible])
	require.Equal(t, Ref{ID: hidden, DisplayName: "Hidden Person"}, refs[hidden])
	ref, ok := refs.Get(removed)
	require.True(t, ok)
	require.Equal(t, Ref{ID: removed}, ref)
	require.False(t, ref.Named())

	users.display[visible], users.display[hidden] = "known-person", "private-username"
	users.fallback = []model.UserID{visible, hidden}
	refs, err = resolver.Resolve(t.Context(), caller, []model.UserID{visible, hidden})
	require.NoError(t, err)
	require.Equal(t, Ref{ID: visible, Username: "known-person", DisplayName: "known-person", Visible: true}, refs[visible])
	require.Equal(t, Ref{ID: hidden}, refs[hidden], "an out-of-directory username cannot appear as a display name")
}
