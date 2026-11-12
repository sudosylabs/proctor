// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuditSelfEventCatalogueIsClosedAndUnambiguous(t *testing.T) {
	t.Parallel()
	name := regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)?$`)
	seen := map[AuditSelfEventKey]AuditSelfEvent{}
	for _, rule := range auditSelfEventRules {
		require.True(t, IsAuditAction(string(rule.action)), "%s maps an unknown action", rule.event)
		require.Regexp(t, name, string(rule.event))
		require.Contains(t, []AuditStatus{"", AuditStatusSuccess, AuditStatusFail}, rule.status)
		for _, key := range AuditSelfEventKeys([]AuditSelfEvent{rule.event}, "") {
			if key.Action != string(rule.action) || key.Operation != rule.operation {
				continue
			}
			previous, duplicate := seen[key]
			require.False(t, duplicate, "%v names both %s and %s", key, previous, rule.event)
			seen[key] = rule.event
		}
	}
	require.Len(t, AuditSelfEventKeys(nil, ""), len(seen))
	for key, event := range seen {
		got, ok := AuditSelfEventFor(key.Action, key.Operation, key.Status)
		require.True(t, ok)
		require.Equal(t, event, got)
	}
}

func TestAuditSelfEventsNameTerminalFactsOnly(t *testing.T) {
	t.Parallel()
	login := string(ActionAuthenticationLogin)
	got, ok := AuditSelfEventFor(login, "", AuditStatusSuccess)
	require.True(t, ok)
	require.Equal(t, AuditSelfEventSignInSucceeded, got)
	got, ok = AuditSelfEventFor(login, "", AuditStatusFail)
	require.True(t, ok)
	require.Equal(t, AuditSelfEventSignInFailed, got)
	_, ok = AuditSelfEventFor(login, "", AuditStatusAttempt)
	require.False(t, ok, "an attempt is never part of self history")
	_, ok = AuditSelfEventFor(string(ActionSessionManage), "revoke_everything", AuditStatusSuccess)
	require.False(t, ok, "an unknown operation must not inherit its action's name")
	require.Equal(t, []AuditSelfEventKey{{Action: login, Status: AuditStatusFail}, {Action: string(ActionAuthenticationExternalLogin), Status: AuditStatusFail}},
		AuditSelfEventKeys([]AuditSelfEvent{AuditSelfEventSignInFailed}, ""))
	require.Empty(t, AuditSelfEventKeys([]AuditSelfEvent{AuditSelfEventSignInFailed}, AuditStatusSuccess))
	for _, key := range AuditSelfEventKeys(nil, "") {
		require.NotEqual(t, AuditStatusAttempt, key.Status)
	}
	require.True(t, AuditSelfEventSessionsRevokedOthers.IsValid())
	require.False(t, AuditSelfEvent("mfa.activate").IsValid())
}

func TestAuditSelfEventsNameDeviceRenames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		action    Action
		operation string
		event     AuditSelfEvent
	}{
		{ActionDesktopRegistrationRename, AuditOperationRenameDesktopRegistration, AuditSelfEventDesktopRegistrationRenamed},
		{ActionSessionManage, AuditOperationRenameOwnSession, AuditSelfEventSessionRenamed},
	} {
		t.Run(string(test.event), func(t *testing.T) {
			t.Parallel()
			for _, status := range []AuditStatus{AuditStatusSuccess, AuditStatusFail} {
				got, ok := AuditSelfEventFor(string(test.action), test.operation, status)
				require.True(t, ok, "%s %s", test.action, status)
				require.Equal(t, test.event, got)
			}
		})
	}
}
