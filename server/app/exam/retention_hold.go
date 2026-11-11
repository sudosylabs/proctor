// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CreateRetentionHoldCommand struct {
	Scope          model.RetentionHoldScope
	ReasonCode     string
	PrivateReason  string
	IdempotencyKey string
}

type ReleaseRetentionHoldCommand struct {
	Scope            model.RetentionHoldScope
	HoldID           model.RetentionHoldID
	ExpectedRevision int64
	ReasonCode       string
	PrivateReason    string
	IdempotencyKey   string
}

// ListRetentionHolds returns one page of Retention Holds within an Exam
// records scope to a caller authorized to manage holds.
func (r *Records) ListRetentionHolds(
	ctx context.Context,
	inv invocation.Invocation,
	query store.ExamRecordsHoldListOptions,
) (_ *store.ExamRecordsHoldPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if query.Limit < 1 || query.Limit > 200 || !query.After.IsZero() && !query.After.IsValid() {
		return nil, apperror.Invalid("exam.invalid", "hold_page")
	}

	if _, _, err := r.authorizeScope(
		ctx,
		inv,
		query.Scope,
		model.ActionExamRecordsHold,
		model.ActionExamRecordsHoldOverride,
		false,
	); err != nil {
		return nil, err
	}

	page, err := r.records.ListHolds(ctx, query)
	if err != nil {
		return nil, examRecordsStoreErrors.Translate(err)
	}
	if page == nil || len(page.Holds) > query.Limit {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Retention hold page is incomplete"))
	}

	for _, hold := range page.Holds {
		if hold.Validate() != nil || hold.Scope.ExamID != query.Scope.ExamID {
			return nil, apperror.New("exam.unavailable").Wrap(errors.New("Retention hold projection is invalid"))
		}
	}

	return page, nil
}

// CreateRetentionHold idempotently places a Retention Hold on an Exam records
// scope with a recorded reason.
func (r *Records) CreateRetentionHold(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateRetentionHoldCommand,
) (_ *model.RetentionHold, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if model.ValidateRecordsReason(command.ReasonCode, command.PrivateReason) != nil {
		return nil, apperror.Invalid("exam.invalid", "retention_hold")
	}

	key := command.IdempotencyKey
	command.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Require(inv, store.ExamRecordsCreateHoldOperation, key, command)
	if err != nil {
		return nil, err
	}

	scope, action, err := r.authorizeScope(ctx, inv, command.Scope, model.ActionExamRecordsHold, model.ActionExamRecordsHoldOverride, false)
	if err != nil {
		return nil, err
	}

	holdID := r.newHoldID()
	if !holdID.IsValid() {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Retention hold identity is invalid"))
	}

	mutation, err := r.beginMutation(
		ctx,
		inv,
		scope,
		action,
		"create_hold",
		map[string]any{"retention_hold_id": holdID.String(), "reason_code": command.ReasonCode},
	)
	if err != nil {
		return nil, err
	}

	result, err := r.records.CreateHold(ctx, &store.ExamRecordsHoldCreation{
		ExamRecordsMutation: mutation,
		HoldID:              holdID,
		ReasonCode:          command.ReasonCode,
		PrivateReason:       command.PrivateReason,
	}, commandIdempotency)
	if err != nil {
		return nil, r.failMutation(ctx, mutation, err)
	}
	if result == nil || result.Hold.Validate() != nil || result.Hold.Scope != command.Scope {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Retention hold creation result is incomplete"))
	}

	return result.Hold, nil
}

// ReleaseRetentionHold idempotently releases a Retention Hold, fenced by
// ExpectedRevision. It requires strong and recent authentication and allows
// no override.
func (r *Records) ReleaseRetentionHold(
	ctx context.Context,
	inv invocation.Invocation,
	command ReleaseRetentionHoldCommand,
) (_ *model.RetentionHold, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.HoldID.IsValid() ||
		command.ExpectedRevision < 1 ||
		model.ValidateRecordsReason(command.ReasonCode, command.PrivateReason) != nil {
		return nil, apperror.Invalid("exam.invalid", "retention_hold")
	}

	key := command.IdempotencyKey
	command.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Require(inv, store.ExamRecordsReleaseHoldOperation, key, command)
	if err != nil {
		return nil, err
	}

	scope, action, err := r.authorizeScope(ctx, inv, command.Scope, model.ActionRetentionHoldRelease, "", false)
	if err != nil {
		return nil, err
	}
	if !inv.Principal().HasStrongAuthentication() {
		return nil, apperror.New("authentication.strong_required")
	}
	if !inv.Principal().IsRecentlyAuthenticated(model.TimeUTC(r.now()), r.recentTTL) {
		return nil, apperror.New("authentication.reauthentication_required")
	}

	mutation, err := r.beginMutation(ctx, inv, scope, action, "release_hold", map[string]any{
		"retention_hold_id": command.HoldID.String(),
		"expected_revision": command.ExpectedRevision,
		"reason_code":       command.ReasonCode,
	})
	if err != nil {
		return nil, err
	}

	result, err := r.records.ReleaseHold(ctx, &store.ExamRecordsHoldRelease{
		ExamRecordsMutation:     mutation,
		HoldID:                  command.HoldID,
		ExpectedRevision:        command.ExpectedRevision,
		ReasonCode:              command.ReasonCode,
		PrivateReason:           command.PrivateReason,
		RecentAuthenticationTTL: r.recentTTL,
	}, commandIdempotency)
	if err != nil {
		return nil, r.failMutation(ctx, mutation, err)
	}

	if !result.Released() {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Retention hold release result is incomplete"))
	}

	return result.Hold, nil
}
