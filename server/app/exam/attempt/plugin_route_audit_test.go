// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func pluginRouteCallFixture(f *fixture) PluginRouteCall {
	return PluginRouteCall{
		Access: VerifiedCandidateAccess{
			AttemptID: model.NewExamAttemptID(), SittingID: model.NewExamSittingID(), ClassID: model.NewClassID(),
			CandidateUserID: f.inv.Principal().UserID, ParticipationID: model.NewAttemptParticipationID(), Generation: 2,
		},
		PluginID: "execution",
		Method:   "POST",
		Route:    "/plugins/execution/exam-attempts/{exam_attempt_id}/flush",
	}
}

func TestPluginRouteCallAuditNamesTheCandidateSittingAndRoute(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	call := pluginRouteCallFixture(f)

	auditID, err := f.service.BeginPluginRouteCall(context.Background(), f.inv, call)
	require.NoError(t, err)
	require.True(t, auditID.IsValid())
	require.Equal(t, model.ActionExamSittingParticipate, f.audit.action)
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: call.Access.SittingID.String()}, f.audit.resource)
	require.Equal(t, model.RoleScopeClass, f.audit.scopeType)
	require.Equal(t, call.Access.ClassID.String(), f.audit.scopeID)
	require.Equal(t, PluginRouteCallOperation, f.audit.operation)
	require.Equal(t, map[string]any{
		"exam_attempt_id": call.Access.AttemptID.String(),
		"plugin_id":       "execution",
		"method":          "POST",
		"route":           call.Route,
	}, f.audit.values, "only identifiers and the route template reach audit")

	socket := call
	socket.Socket = true
	_, err = f.service.BeginPluginRouteCall(context.Background(), f.inv, socket)
	require.NoError(t, err)
	require.Equal(t, true, f.audit.values["socket"])
}

func TestPluginRouteCallAuditRefusesACallItCannotAttribute(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*PluginRouteCall){
		"another candidate": func(call *PluginRouteCall) { call.Access.CandidateUserID = model.NewUserID() },
		"no Sitting":        func(call *PluginRouteCall) { call.Access.SittingID = "" },
		"no Class":          func(call *PluginRouteCall) { call.Access.ClassID = "" },
		"bad plugin ID":     func(call *PluginRouteCall) { call.PluginID = "Not An ID" },
		"no method":         func(call *PluginRouteCall) { call.Method = "" },
		"relative route":    func(call *PluginRouteCall) { call.Route = "flush" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			call := pluginRouteCallFixture(f)
			change(&call)
			_, err := f.service.BeginPluginRouteCall(context.Background(), f.inv, call)
			require.True(t, apperror.Is(err, "exam.attempt.unavailable"), "error = %v", err)
			require.Nil(t, f.audit.values, "nothing was audited")
		})
	}
}

func TestPluginRouteCallAuditClosesWithItsOutcome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	auditID := model.NewAuditEventID()

	require.NoError(t, f.service.EndPluginRouteCall(context.Background(), f.inv, auditID, PluginRouteOutcome{Status: 202}))
	require.NoError(t, f.service.EndPluginRouteCall(context.Background(), f.inv, auditID, PluginRouteOutcome{End: "done"}))
	require.NoError(t, f.service.EndPluginRouteCall(context.Background(), f.inv, auditID, PluginRouteOutcome{ErrorCode: "plugin.route.conflict"}))
	require.Equal(t, []string{"audit.complete", "audit.complete", "audit.fail"}, f.order)
	require.Equal(t, "plugin.route.conflict", f.audit.failedCode)
}
