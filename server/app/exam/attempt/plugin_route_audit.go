// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// PluginRouteCallOperation names a candidate's call to a plugin route in
// audit.
const PluginRouteCallOperation = "plugin.route.call"

// PluginRouteCall is one call a candidate makes to a plugin route, after the
// candidate guard admitted it. Route is the mounted template, never the
// request path, so no identifier a candidate typed reaches audit.
type PluginRouteCall struct {
	Access   VerifiedCandidateAccess
	PluginID model.PluginID
	Method   string
	Route    string
	// Socket marks a call that upgrades to a socket; its audit stays open
	// until the socket ends.
	Socket bool
}

// PluginRouteOutcome is how an audited plugin route call ended. ErrorCode is
// empty for success. Status is the HTTP status a plain call answered with;
// End is the socket's end ("done", "access_ended" or "failed").
type PluginRouteOutcome struct {
	ErrorCode string
	Status    int
	End       string
}

// BeginPluginRouteCall opens the audit of one plugin route call before any
// plugin code sees it. The record names the candidate, the Sitting and Class
// the Attempt belongs to, the plugin, and the route template; never the
// request body, query or socket messages. Audit failing refuses the call.
func (s *Service) BeginPluginRouteCall(
	ctx context.Context,
	inv invocation.Invocation,
	call PluginRouteCall,
) (_ model.AuditEventID, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	verified := call.Access
	if !verified.AttemptID.IsValid() ||
		!verified.SittingID.IsValid() ||
		!verified.ClassID.IsValid() ||
		verified.CandidateUserID != inv.Principal().UserID ||
		!call.PluginID.IsValid() ||
		call.Method == "" ||
		!strings.HasPrefix(call.Route, "/") {
		return "", apperror.New("exam.attempt.unavailable").Wrap(errors.New("incomplete plugin route call"))
	}

	value := map[string]any{
		"exam_attempt_id": verified.AttemptID.String(),
		"plugin_id":       call.PluginID.String(),
		"method":          call.Method,
		"route":           call.Route,
	}
	if call.Socket {
		value["socket"] = true
	}
	return s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: verified.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    verified.ClassID.String(),
		Operation:  PluginRouteCallOperation,
		Value:      value,
	})
}

// EndPluginRouteCall closes the audit BeginPluginRouteCall opened, with how
// the call ended. A plain call that cannot close its audit must not report
// success.
func (s *Service) EndPluginRouteCall(
	ctx context.Context,
	_ invocation.Invocation,
	auditID model.AuditEventID,
	outcome PluginRouteOutcome,
) error {
	if outcome.ErrorCode != "" {
		return s.auditor.Fail(ctx, auditID, outcome.ErrorCode)
	}
	result := map[string]any{}
	if outcome.Status != 0 {
		result["status"] = outcome.Status
	}
	if outcome.End != "" {
		result["end"] = outcome.End
	}
	return s.auditor.Complete(ctx, auditID, result)
}
