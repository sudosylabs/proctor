// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryAuditActionHasOneKind fails when an audit action, event-only ones
// included, is added without a kind, or a kind outlives its action.
func TestEveryAuditActionHasOneKind(t *testing.T) {
	t.Parallel()

	classified := make([]string, 0, len(auditActionKinds))
	for action, kind := range auditActionKinds {
		require.True(t, kind.IsValid(), action)
		classified = append(classified, string(action))
	}
	sort.Strings(classified)
	require.Equal(t, AuditActions(), classified)

	for _, action := range AuditActions() {
		kind, known := AuditActionKind(action)
		require.True(t, known, action)
		require.True(t, kind.IsValid(), action)
	}
	_, known := AuditActionKind("not.an_action")
	require.False(t, known)
}

func TestAuditActionsOfKindPartitionTheCatalog(t *testing.T) {
	t.Parallel()

	reads, changes := AuditActionsOfKind(AuditKindRead), AuditActionsOfKind(AuditKindChange)
	require.True(t, slices.IsSorted(reads))
	require.True(t, slices.IsSorted(changes))
	all := append(slices.Clone(reads), changes...)
	sort.Strings(all)
	require.Equal(t, AuditActions(), all, "every action is exactly one kind")
	require.Empty(t, AuditActionsOfKind("other"))

	for action, want := range map[Action]AuditKind{
		ActionAuditView:                 AuditKindRead,
		ActionUserSearch:                AuditKindRead,
		ActionExamRecordsExport:         AuditKindRead,
		ActionAuthenticationLogin:       AuditKindChange,
		ActionMFAChallenge:              AuditKindChange,
		ActionDesktopRegistrationRename: AuditKindChange,
		ActionUserManage:                AuditKindChange,
	} {
		kind, _ := AuditActionKind(string(action))
		require.Equal(t, want, kind, action)
	}
}
