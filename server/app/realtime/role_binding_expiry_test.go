// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type roleBindingExpiries struct {
	store.RoleBindingStore
	list func(store.RoleBindingExpiryCursor, int) (store.RoleBindingExpiryPage, error)
}

func (f roleBindingExpiries) ListExpiries(_ context.Context, after store.RoleBindingExpiryCursor, limit int) (store.RoleBindingExpiryPage, error) {
	return f.list(after, limit)
}

func TestRoleBindingExpiryContinuesFromLastProcessedDeadlineAfterFailure(t *testing.T) {
	t.Parallel()
	at := model.NowUTC()
	userID, otherID := model.NewUserID(), model.NewUserID()
	first := store.RoleBindingExpiryPage{CheckedAt: at}
	for i := range roleBindingExpiryPageSize {
		first.Items = append(first.Items, store.RoleBindingExpiry{
			Cursor: store.RoleBindingExpiryCursor{At: at.Add(time.Duration(i-roleBindingExpiryPageSize) * time.Microsecond), ID: model.NewRoleBindingID()},
			UserID: userID,
		})
	}
	last := first.Items[len(first.Items)-1].Cursor
	failure := errors.New("postgres unavailable")
	next := store.RoleBindingExpiryPage{CheckedAt: at.Add(time.Millisecond), Items: []store.RoleBindingExpiry{{
		Cursor: store.RoleBindingExpiryCursor{At: at, ID: model.NewRoleBindingID()}, UserID: otherID,
	}}}
	calls := 0
	var closed []string
	runner := roleBindingExpiry{
		bindings: roleBindingExpiries{list: func(cursor store.RoleBindingExpiryCursor, limit int) (store.RoleBindingExpiryPage, error) {
			require.Equal(t, roleBindingExpiryPageSize, limit)
			calls++
			switch calls {
			case 1:
				require.Empty(t, cursor)
				return first, nil
			case 2:
				require.Equal(t, last, cursor)
				return store.RoleBindingExpiryPage{}, failure
			default:
				require.Equal(t, last, cursor)
				return next, nil
			}
		}},
		closeUser: func(id string) { closed = append(closed, id) },
	}

	require.ErrorIs(t, runner.Run(t.Context()), failure)
	require.Equal(t, []string{userID.String()}, closed, "one user's expired bindings cause one close per pass")
	require.NoError(t, runner.Run(t.Context()))
	require.Equal(t, []string{userID.String(), otherID.String()}, closed)
	require.Equal(t, store.RoleBindingExpiryCursor{At: next.CheckedAt}, runner.cursor)
}
