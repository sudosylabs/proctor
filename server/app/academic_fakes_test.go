// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

type academicUnitCommandAuditor struct {
	events    *[]string
	beginID   string
	beginErr  error
	failErr   error
	failCode  string
	beginData academicUnitMutationAudit
}

type academicUnitMutationAudit struct {
	action    model.Action
	resource  model.Resource
	operation string
	value     map[string]any
	prior     map[string]any
}

func (a *academicUnitCommandAuditor) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	// This fake keeps neither a scope nor a subject, so it refuses a Mutation
	// that names one instead of dropping it.
	if attempt.ScopeType != "" || attempt.ScopeID != "" || !attempt.Subject.IsZero() {
		return "", apperror.New("audit.internal")
	}

	*a.events = append(*a.events, "audit-begin")
	a.beginData = academicUnitMutationAudit{
		action: attempt.Action, resource: attempt.Resource, operation: attempt.Operation,
		value: attempt.Value, prior: attempt.Prior,
	}
	return model.AuditEventID(a.beginID), a.beginErr
}

func (a *academicUnitCommandAuditor) Fail(_ context.Context, auditID model.AuditEventID, errorCode string) error {
	*a.events = append(*a.events, "audit-fail")
	a.failCode = errorCode
	if auditID != model.AuditEventID(a.beginID) {
		return errors.New("wrong audit ID")
	}
	return a.failErr
}
