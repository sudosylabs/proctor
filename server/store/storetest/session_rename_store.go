// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func testSessionRename(t *testing.T, ss store.Store) {
	ctx := context.Background()
	user := saveUser(t, ctx, ss)
	session, _, _ := saveSession(t, ctx, ss, user.ID.String(), 10)
	at := model.NowUTC().Add(100 * time.Millisecond)
	rename := func(userID model.UserID, id model.SessionID, auditID model.AuditEventID, name string) (*model.Session, error) {
		return ss.Session().Rename(ctx, &store.SessionRename{
			SessionID: id, UserID: userID, DeviceName: name, AuditEventID: auditID, AuditAt: at,
		})
	}
	requireDeviceName := func(t *testing.T, want string) {
		t.Helper()
		stored, err := ss.Session().Get(ctx, session.ID)
		require.NoError(t, err)
		require.Equal(t, want, stored.DeviceName)
	}

	t.Run("WithoutItsAuditAttemptChangesNothing", func(t *testing.T) {
		_, err := rename(user.ID, session.ID, model.NewAuditEventID(), "Office browser")
		require.Error(t, err, "rename committed without its audit attempt")
		requireDeviceName(t, session.DeviceName)
	})

	t.Run("RefusesAnUnnormalizedName", func(t *testing.T) {
		for _, name := range []string{"", " Office browser", "line\nbreak"} {
			attempt := saveSessionAuditAttempt(t, ctx, ss, user.ID.String())
			_, err := rename(user.ID, session.ID, attempt.ID, name)
			require.ErrorAs(t, err, new(*store.ErrInvalidInput), "name %q", name)
		}
		requireDeviceName(t, session.DeviceName)
	})

	t.Run("ConcealsAnotherUsersOrAMissingSession", func(t *testing.T) {
		other := saveUser(t, ctx, ss)
		for name, target := range map[string]struct {
			userID model.UserID
			id     model.SessionID
		}{
			"another User": {other.ID, session.ID},
			"missing":      {user.ID, model.NewSessionID()},
		} {
			attempt := saveSessionAuditAttempt(t, ctx, ss, target.userID.String())
			_, err := rename(target.userID, target.id, attempt.ID, "Office browser")
			require.True(t, store.IsNotFound(err), "%s: error = %v", name, err)
			audit, auditErr := ss.Audit().Get(ctx, attempt.ID)
			require.NoError(t, auditErr)
			require.Equal(t, model.AuditStatusAttempt, audit.Status, "%s left its attempt open for the caller to fail", name)
		}
		requireDeviceName(t, session.DeviceName)
	})

	t.Run("RenamesAndCompletesTheAudit", func(t *testing.T) {
		attempt := saveSessionAuditAttempt(t, ctx, ss, user.ID.String())
		renamed, err := rename(user.ID, session.ID, attempt.ID, "Office browser")
		require.NoError(t, err)
		require.Equal(t, "Office browser", renamed.DeviceName)
		require.True(t, at.Equal(renamed.UpdatedAt), "renamed at = %v", renamed.UpdatedAt)
		stored, err := ss.Session().Get(ctx, session.ID)
		require.NoError(t, err)
		require.Equal(t, renamed, stored)
		audit, err := ss.Audit().Get(ctx, attempt.ID)
		require.NoError(t, err)
		require.Equal(t, model.AuditStatusSuccess, audit.Status)
		require.NotContains(t, string(audit.Result), "Office browser", "the chosen name entered the audit")

		repeat := saveSessionAuditAttempt(t, ctx, ss, user.ID.String())
		again, err := rename(user.ID, session.ID, repeat.ID, "Office browser")
		require.NoError(t, err)
		require.Equal(t, renamed.DeviceName, again.DeviceName)
	})

	t.Run("ConcealsARevokedSession", func(t *testing.T) {
		revocation := saveSessionAuditAttempt(t, ctx, ss, user.ID.String())
		_, err := ss.Session().Revoke(ctx, &store.SessionRevocation{
			SessionID: session.ID, UserID: user.ID, RevokedAt: at, Reason: model.SessionRevocationUserSession,
			AuditEventID: revocation.ID, AuditAt: at,
		})
		require.NoError(t, err)
		attempt := saveSessionAuditAttempt(t, ctx, ss, user.ID.String())
		_, err = rename(user.ID, session.ID, attempt.ID, "Revoked browser")
		require.True(t, store.IsNotFound(err), "renaming a revoked Session: error = %v", err)
		requireDeviceName(t, "Office browser")
	})
}
