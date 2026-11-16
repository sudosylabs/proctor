// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/store"
)

func TestEnvironmentWorkspacePresentsRefusalsToTheOwner(t *testing.T) {
	t.Parallel()
	stale := apperror.New("exam.attempt.connection_closed").Wrap(
		store.NewErrConflict("exam_attempt_workspace", "attempt_environment_owner", nil))
	require.ErrorIs(t, environmentReadError(stale), ErrWorkspaceOwnerStale)
	require.ErrorIs(t, environmentWriteError(stale), ErrWorkspaceOwnerStale)

	sealed := apperror.New("exam.attempt.state_conflict").Wrap(store.NewErrConflict("exam_attempt", "exam_attempt_state", nil))
	require.ErrorIs(t, environmentWriteError(sealed), ErrWorkspaceSealed)

	gone := apperror.New("resource.not_found").Wrap(apperror.New("exam.attempt.not_found"))
	require.ErrorIs(t, environmentReadError(gone), ErrWorkspaceEntryNotFound)

	var refusal *WorkspaceRefusal
	item := apperror.New("exam.attempt.workspace.content_conflict").WithField("item", "2")
	require.ErrorAs(t, environmentWriteError(item), &refusal)
	require.Equal(t, WorkspaceRefusal{Code: "exam.attempt.workspace.content_conflict", Item: 2}, *refusal)
	whole := apperror.New("exam.attempt.workspace.size_limit")
	require.ErrorAs(t, environmentWriteError(whole), &refusal)
	require.Equal(t, -1, refusal.Item, "a refusal of the whole batch names no item")

	plain := errors.New("connection reset")
	require.Same(t, plain, environmentWriteError(plain))
	_, err := newAttemptWorkspace(nil)
	require.Error(t, err)
}
