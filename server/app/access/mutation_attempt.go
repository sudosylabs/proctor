// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// Mutation is the audit context of one named store mutation. Authorization is
// done, and Value and Prior hold only safe projections, before RunAudited
// receives it.
type Mutation struct {
	Invocation invocation.Invocation
	Action     model.Action
	Resource   model.Resource
	ScopeType  model.RoleScopeType
	ScopeID    string
	Operation  string
	Value      map[string]any
	Prior      map[string]any
	// Subject is the user the mutation concerns when Resource is not that user,
	// such as a role binding's user or a class member.
	Subject model.UserID
	// At becomes MutationReference.MutationAt in UTC microseconds; RunAudited
	// reads no clock of its own and refuses a zero At before Begin.
	At time.Time
}

// MutationReference lets a named store mutation complete its audit event in
// the same commit as its state change.
type MutationReference struct {
	ID         model.AuditEventID
	MutationAt time.Time
}

// RunAudited begins the audit event and records its failure; success is
// completed inside mutate's own commit, and post-commit effects stay with the
// caller. mapError must return a chain containing an *apperror.Error, or the
// failure becomes audit.internal. A malformed scope or subject is refused
// with audit.internal before Begin.
func RunAudited[T any](
	ctx context.Context,
	auditor MutationAuditAdapter,
	attempt Mutation,
	mutate func(context.Context, MutationReference) (T, error),
	mapError func(error) error,
) (T, error) {
	var zero T
	scoped := attempt.ScopeType != "" || attempt.ScopeID != ""
	if attempt.At.IsZero() ||
		!attempt.Subject.IsZero() && !attempt.Subject.IsValid() ||
		scoped && (!attempt.ScopeType.IsValid() || !model.IsValidOpaqueID(attempt.ScopeID)) {
		return zero, apperror.New("audit.internal")
	}

	auditID, err := auditor.Begin(ctx, attempt)
	if err != nil {
		return zero, err
	}

	result, err := mutate(ctx, MutationReference{
		ID:         auditID,
		MutationAt: model.TimeUTC(attempt.At),
	})
	if err == nil {
		return result, nil
	}

	mapped := mapError(err)
	failure, ok := apperror.As(mapped)
	if !ok {
		mapped = apperror.New("audit.internal").Wrap(
			errors.New("mutation error mapper returned no application error"),
		)
		failure, _ = apperror.As(mapped)
	}

	if auditErr := auditor.Fail(ctx, auditID, failure.Code()); auditErr != nil {
		return zero, errors.Join(mapped, auditErr)
	}

	return zero, mapped
}
