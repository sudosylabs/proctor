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

// TestDesktopRegistrationStore checks the User's rename of a Desktop
// Registration, and that a Desktop Session cannot be renamed apart from it.
func TestDesktopRegistrationStore(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	t.Run("RenameSurvivesDesktopSignIn", func(t *testing.T) {
		testDesktopRegistrationRenameSurvivesSignIn(t, ctx, ss, institution.ID)
	})
	t.Run("RenameRefusals", func(t *testing.T) { testDesktopRegistrationRenameRefusals(t, ctx, ss, institution.ID) })
	t.Run("DesktopSessionIsNamedByItsRegistration", func(t *testing.T) {
		testDesktopSessionIsNamedByItsRegistration(t, ctx, ss, institution.ID)
	})
}

func testDesktopRegistrationRenameSurvivesSignIn(t *testing.T, ctx context.Context, ss store.Store, institutionID model.InstitutionID) {
	user := saveUser(t, ctx, ss)
	session := saveRegisteredDesktopSession(t, ctx, ss, user.ID, institutionID)
	before, err := ss.DesktopRegistration().Get(ctx, session.DesktopRegistrationID)
	require.NoError(t, err)
	require.Equal(t, "Test Desktop", before.DisplayName, "the reported device name names a new registration")
	require.False(t, before.RenamedAt.Valid)

	at := model.NowUTC().Add(100 * time.Millisecond)
	attempt := saveDesktopRegistrationRenameAudit(t, ctx, ss, user.ID)
	renamed, err := ss.DesktopRegistration().Rename(ctx, &store.DesktopRegistrationRename{
		RegistrationID: before.ID, UserID: user.ID, DisplayName: "Library laptop", AuditEventID: attempt.ID, AuditAt: at,
	})
	require.NoError(t, err)
	require.Equal(t, "Library laptop", renamed.DisplayName)
	require.True(t, at.Equal(renamed.RenamedAt.Time), "renamed at = %v", renamed.RenamedAt)
	require.True(t, at.Equal(renamed.UpdatedAt), "updated at = %v", renamed.UpdatedAt)
	stored, err := ss.DesktopRegistration().Get(ctx, before.ID)
	require.NoError(t, err)
	require.Equal(t, renamed, stored)
	audit, err := ss.Audit().Get(ctx, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, audit.Status)
	require.NotContains(t, string(audit.Result), "Library laptop", "the chosen name entered the audit")

	// The same Desktop key signs in again and reports its device name; the
	// name the User chose stays.
	again := saveRegisteredDesktopSession(t, ctx, ss, user.ID, institutionID)
	require.Equal(t, before.ID, again.DesktopRegistrationID)
	signedIn, err := ss.DesktopRegistration().Get(ctx, before.ID)
	require.NoError(t, err)
	require.Equal(t, "Library laptop", signedIn.DisplayName)
	require.True(t, renamed.RenamedAt.Time.Equal(signedIn.RenamedAt.Time))
	require.False(t, signedIn.LastUsedAt.Before(renamed.LastUsedAt))
}

func testDesktopRegistrationRenameRefusals(t *testing.T, ctx context.Context, ss store.Store, institutionID model.InstitutionID) {
	user := saveUser(t, ctx, ss)
	session := saveRegisteredDesktopSession(t, ctx, ss, user.ID, institutionID)
	registrationID := session.DesktopRegistrationID
	at := model.NowUTC().Add(100 * time.Millisecond)
	rename := func(userID model.UserID, id model.DesktopRegistrationID, auditID model.AuditEventID, name string) error {
		_, err := ss.DesktopRegistration().Rename(ctx, &store.DesktopRegistrationRename{
			RegistrationID: id, UserID: userID, DisplayName: name, AuditEventID: auditID, AuditAt: at,
		})
		return err
	}
	requireUnchanged := func(t *testing.T) {
		t.Helper()
		stored, err := ss.DesktopRegistration().Get(ctx, registrationID)
		require.NoError(t, err)
		require.Equal(t, "Test Desktop", stored.DisplayName)
		require.False(t, stored.RenamedAt.Valid)
	}

	t.Run("WithoutItsAuditAttempt", func(t *testing.T) {
		require.Error(t, rename(user.ID, registrationID, model.NewAuditEventID(), "Library laptop"))
		requireUnchanged(t)
	})
	t.Run("AnUnnormalizedName", func(t *testing.T) {
		for _, name := range []string{"", "Library laptop ", "tab\tseparated"} {
			attempt := saveDesktopRegistrationRenameAudit(t, ctx, ss, user.ID)
			require.ErrorAs(t, rename(user.ID, registrationID, attempt.ID, name), new(*store.ErrInvalidInput), "name %q", name)
		}
		requireUnchanged(t)
	})
	t.Run("AnotherUsersOrAMissingRegistration", func(t *testing.T) {
		other := saveUser(t, ctx, ss)
		for name, target := range map[string]struct {
			userID model.UserID
			id     model.DesktopRegistrationID
		}{
			"another User": {other.ID, registrationID},
			"missing":      {user.ID, model.NewDesktopRegistrationID()},
		} {
			attempt := saveDesktopRegistrationRenameAudit(t, ctx, ss, target.userID)
			err := rename(target.userID, target.id, attempt.ID, "Library laptop")
			require.True(t, store.IsNotFound(err), "%s: error = %v", name, err)
		}
		requireUnchanged(t)
	})
	t.Run("ARevokedRegistration", func(t *testing.T) {
		revocation := saveDesktopRegistrationRenameAudit(t, ctx, ss, user.ID)
		_, err := ss.DesktopRegistration().Revoke(ctx, &store.DesktopRegistrationRevocation{
			RegistrationID: registrationID, UserID: user.ID, RevokedAt: at, AuditEventID: revocation.ID, AuditAt: at,
		})
		require.NoError(t, err)
		attempt := saveDesktopRegistrationRenameAudit(t, ctx, ss, user.ID)
		err = rename(user.ID, registrationID, attempt.ID, "Library laptop")
		require.ErrorAs(t, err, new(*store.ErrConflict))
		audit, auditErr := ss.Audit().Get(ctx, attempt.ID)
		require.NoError(t, auditErr)
		require.Equal(t, model.AuditStatusAttempt, audit.Status, "a refused rename left its attempt open for the caller to fail")
		requireUnchanged(t)
	})
}

func testDesktopSessionIsNamedByItsRegistration(t *testing.T, ctx context.Context, ss store.Store, institutionID model.InstitutionID) {
	user := saveUser(t, ctx, ss)
	session := saveRegisteredDesktopSession(t, ctx, ss, user.ID, institutionID)
	attempt := saveSessionAuditAttempt(t, ctx, ss, user.ID.String())
	_, err := ss.Session().Rename(ctx, &store.SessionRename{
		SessionID: session.ID, UserID: user.ID, DeviceName: "Library laptop", AuditEventID: attempt.ID, AuditAt: model.NowUTC(),
	})
	require.ErrorAs(t, err, new(*store.ErrConflict))
	stored, err := ss.Session().Get(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, session.DeviceName, stored.DeviceName)
}

func saveDesktopRegistrationRenameAudit(t *testing.T, ctx context.Context, ss store.Store, userID model.UserID) *model.AuditEvent {
	t.Helper()
	attempt, err := ss.Audit().Save(ctx, &model.AuditEvent{
		ActorID:   userID,
		Action:    string(model.ActionDesktopRegistrationRename),
		Resource:  model.Resource{Type: model.ResourceUser, ID: userID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: model.NewID(),
		Status: model.AuditStatusAttempt, NodeID: "storetest",
	})
	require.NoError(t, err)
	return attempt
}
