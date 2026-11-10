// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func selfAuditSessionInvocation() invocation.Invocation {
	at := time.Now().UTC()
	return invocation.New(model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: at, ClientType: model.SessionClientWeb,
	}, model.RequestMetadata{RequestID: "self-audit"})
}

type institutionAuditorFake struct {
	events    *[]string
	beginID   string
	beginIDs  []string
	failCode  string
	action    model.Action
	resource  model.Resource
	resources []model.Resource
	scopeType model.RoleScopeType
	scopeID   string
	subjects  []model.UserID
	values    []map[string]any
}

func (a *institutionAuditorFake) BeginForSubject(ctx context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	a.subjects = append(a.subjects, attempt.Subject)
	if attempt.ScopeType != "" {
		return a.BeginAtScope(ctx, attempt.Invocation, attempt.Action, attempt.Resource, attempt.ScopeType, attempt.ScopeID, attempt.Operation, attempt.Value, attempt.Prior)
	}
	return a.Begin(ctx, attempt.Invocation, attempt.Action, attempt.Resource, attempt.Operation, attempt.Value, attempt.Prior)
}

func (a *institutionAuditorFake) Begin(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource, _ string, value, _ map[string]any) (model.AuditEventID, error) {
	*a.events = append(*a.events, "audit-begin")
	a.values = append(a.values, value)
	a.action, a.resource = action, resource
	a.resources = append(a.resources, resource)
	if len(a.beginIDs) > 0 {
		id := a.beginIDs[0]
		a.beginIDs = a.beginIDs[1:]
		return model.AuditEventID(id), nil
	}
	return model.AuditEventID(a.beginID), nil
}

func (a *institutionAuditorFake) BeginAtScope(ctx context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID, operation string, value, prior map[string]any) (model.AuditEventID, error) {
	a.scopeType, a.scopeID = scopeType, scopeID
	return a.Begin(ctx, invocation, action, resource, operation, value, prior)
}

func (a *institutionAuditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	*a.events = append(*a.events, "audit-fail")
	a.failCode = code
	return nil
}
