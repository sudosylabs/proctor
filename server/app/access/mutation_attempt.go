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

// Mutation describes the safe, application-owned audit context for one
// named Store mutation. Authorization, candidate preparation, and safe audit
// projections are complete before the runner receives this value.
type Mutation struct {
	Invocation invocation.Invocation
	Action     model.Action
	Resource   model.Resource
	ScopeType  model.RoleScopeType
	ScopeID    string
	Operation  string
	Value      map[string]any
	Prior      map[string]any
	// Subject names the User the mutation concerns when Resource is not that
	// User, such as the bound User of a Role Binding or the member of a Class.
	Subject model.UserID
	// At is the instant the mutation happens. RunAudited hands it to the named
	// Store mutation as MutationReference.MutationAt, normalised to UTC
	// microseconds with model.TimeUTC (a zero At stays zero), and reads no
	// clock of its own.
	At time.Time
}

// MutationReference is the complete audit input a named Store mutation
// needs to commit durable state and successful audit completion atomically.
type MutationReference struct {
	ID         model.AuditEventID
	MutationAt time.Time
}

// RunAudited owns the common attempt and failure protocol. Successful
// completion remains inside the named Store mutation; post-commit effects
// remain with the calling use case. The mapper may preserve contextual wrappers
// but must return an error chain containing an application Error. A Mutation
// whose explicit scope or subject User is malformed is refused with
// audit.internal before the auditor is asked to begin.
func RunAudited[T any](
	ctx context.Context,
	auditor MutationAuditor,
	attempt Mutation,
	mutate func(context.Context, MutationReference) (T, error),
	mapError func(error) error,
) (T, error) {
	var zero T
	scoped := attempt.ScopeType != "" || attempt.ScopeID != ""
	if !attempt.Subject.IsZero() && !attempt.Subject.IsValid() ||
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
