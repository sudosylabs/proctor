// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func TestListPermissionsUsesRoleViewAndAdvertisesGrantableScopes(t *testing.T) {
	t.Parallel()
	caller, inv := newCaller()
	fixture := accesstest.New(t, accesstest.Options{Grants: []accesstest.Grant{{
		UserID: caller.UserID, Actions: []model.Action{model.ActionRoleView},
	}}})
	s := &Service{institutions: &institutionStoreFake{institution: fixture.Institution}, authorizer: fixture.Authorizer}
	values, err := s.ListPermissions(t.Context(), inv)
	require.NoError(t, err)
	require.Len(t, values, len(model.GrantableActions()))
	for _, value := range values {
		require.NotEmpty(t, value.Area, value.Action)
		require.NotEmpty(t, value.ScopeTypes, value.Action)
	}
	refused, err := s.ListPermissions(t.Context(), strangerTo(inv))
	require.True(t, apperror.Is(err, "authorization.denied"), "%v", err)
	require.Empty(t, refused)
}
