// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountDisableReasonReadsOnlyTheDisableEvent(t *testing.T) {
	t.Parallel()
	disable := &AuditEvent{Action: string(ActionUserManage),
		Parameters: json.RawMessage(`{"operation":"disable_account","value":{"user_id":"x","disabled":true,"reason":"Left the institution"}}`)}
	require.Equal(t, "Left the institution", disable.AccountDisableReason())
	enable := &AuditEvent{Action: string(ActionUserManage),
		Parameters: json.RawMessage(`{"operation":"enable_account","value":{"reason":"ignored"}}`)}
	require.Empty(t, enable.AccountDisableReason())
	other := &AuditEvent{Action: string(ActionRoleManage), Parameters: disable.Parameters}
	require.Empty(t, other.AccountDisableReason())
	projected := disable.Clone()
	projected.Parameters = nil
	require.Empty(t, projected.AccountDisableReason(), "a projection without parameters shows no reason")
}

func TestPermissionCheckRequiresATerminalDecisionWithoutMutationData(t *testing.T) {
	t.Parallel()
	institution := NewInstitutionID().String()
	check := &AuditEvent{PermissionCheck: true, Action: string(ActionInstitutionManage), Status: AuditStatusSuccess, NodeID: "node",
		Resource: Resource{Type: ResourceInstitution, ID: institution}, ScopeType: RoleScopeInstitution, ScopeID: institution,
	}
	check.PrepareCreate(NewAuditEventID(), NowUTC())
	require.NoError(t, check.Validate())
	for name, change := range map[string]func(*AuditEvent){
		"read action": func(event *AuditEvent) { event.Action = string(ActionUserView) },
		"attempt":     func(event *AuditEvent) { event.Status = AuditStatusAttempt },
		"parameters":  func(event *AuditEvent) { event.Parameters = []byte(`{}`) },
		"prior state": func(event *AuditEvent) { event.PriorState = []byte(`{}`) },
		"result":      func(event *AuditEvent) { event.Result = []byte(`{}`) },
	} {
		event := check.Clone()
		change(event)
		require.Error(t, event.Validate(), name)
	}
}
