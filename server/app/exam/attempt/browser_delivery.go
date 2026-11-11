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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type BrowserDeliveryGapsCommand struct {
	Query          BrowserSourceQuery
	Declaration    model.DeclareDeliveryGaps
	IdempotencyKey string
}
type BrowserDeliveryFinalCommand struct {
	Query          BrowserSourceQuery
	Declaration    model.FinalDeliveryDeclaration
	IdempotencyKey string
}
type BrowserDeliverySummaryCommand struct {
	Query          BrowserSourceQuery
	Summary        model.UnretainedDeliverySummary
	IdempotencyKey string
}
type BrowserDeliveryAppendCommand struct {
	Query          BrowserSourceQuery
	Batch          model.BrowserActivityBatch
	IdempotencyKey string
}

func (s *Service) beginBrowserDeliveryAudit(
	ctx context.Context,
	invocation invocation.Invocation,
	deliveryAccess store.BrowserDeliveryAccess,
	operation string,
) (model.AuditEventID, error) {
	target, err := s.attempts.ResolveBrowserDeliveryTarget(ctx, deliveryAccess)
	if err != nil {
		return "", deliveryStoreError(err)
	}
	if target == nil || !target.SittingID.IsValid() || !target.ClassID.IsValid() {
		return "", apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
	}

	return s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    target.ClassID.String(),
		Operation:  operation,
		Value:      map[string]any{"source_session_id": string(deliveryAccess.SourceSessionID)},
	})
}

// DeclareBrowserDeliveryGaps idempotently records Browser Activity the client
// declares it can no longer deliver for one source session, so the gaps are
// known rather than silently missing.
func (s *Service) DeclareBrowserDeliveryGaps(
	ctx context.Context,
	invocation invocation.Invocation,
	command BrowserDeliveryGapsCommand,
) (_ *model.BrowserDeliveryGapResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	deliveryAccess, err := browserSourceSelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}
	if command.Declaration.Validate() != nil {
		return nil, apperror.Invalid("exam.attempt.invalid", "delivery_gaps")
	}

	commandIdempotency, err := idempotency.Require(invocation, store.BrowserDeliveryGapsOperation, command.IdempotencyKey, struct {
		AttemptID   model.ExamAttemptID
		StreamID    string
		Declaration model.DeclareDeliveryGaps
	}{deliveryAccess.Access.AttemptID, string(deliveryAccess.SourceSessionID), command.Declaration})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginBrowserDeliveryAudit(ctx, invocation, deliveryAccess, store.BrowserDeliveryGapsOperation)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.DeclareBrowserDeliveryGaps(
		ctx,
		&store.BrowserDeliveryGapDeclaration{Access: deliveryAccess, Declaration: command.Declaration, AuditEventID: audit, AuditAt: s.now()},
		commandIdempotency,
	)
	if err != nil {
		var capacity *model.DeliveryMetadataCapacity
		if errors.As(err, &capacity) {
			return nil, s.browserDeliveryFailure(ctx, deliveryAccess, err)
		}

		var refusal *store.BrowserDeliveryRefusal
		if errors.As(err, &refusal) {
			return nil, s.browserDeliveryFailure(ctx, deliveryAccess, err)
		}

		return nil, s.failAudit(ctx, audit, s.browserDeliveryFailure(ctx, deliveryAccess, err))
	}

	if value == nil ||
		value.Receipt.DeclarationID != command.Declaration.DeclarationID ||
		value.Receipt.DeclarationRevision < 1 ||
		!model.IsValidSHA256Fingerprint(value.Receipt.RequestDigest) ||
		value.Status.Validate() != nil ||
		value.Status.SourceSessionID != deliveryAccess.SourceSessionID {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
	}

	return value, nil
}

// SealBrowserDelivery idempotently records the final delivered sequence for
// one Browser Activity source session, fenced by the declaration's expected
// revision.
func (s *Service) SealBrowserDelivery(
	ctx context.Context,
	invocation invocation.Invocation,
	command BrowserDeliveryFinalCommand,
) (_ *model.BrowserSourceStatus, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	deliveryAccess, err := browserSourceSelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}
	if command.Declaration.Validate() != nil {
		return nil, apperror.Invalid("exam.attempt.invalid", "delivery_final")
	}

	commandIdempotency, err := idempotency.Require(invocation, store.BrowserDeliveryFinalOperation, command.IdempotencyKey, struct {
		AttemptID   model.ExamAttemptID
		StreamID    string
		Declaration model.FinalDeliveryDeclaration
	}{deliveryAccess.Access.AttemptID, string(deliveryAccess.SourceSessionID), command.Declaration})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginBrowserDeliveryAudit(ctx, invocation, deliveryAccess, store.BrowserDeliveryFinalOperation)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.SealBrowserDelivery(
		ctx,
		&store.BrowserDeliveryFinalDeclaration{Access: deliveryAccess, Declaration: command.Declaration, AuditEventID: audit, AuditAt: s.now()},
		commandIdempotency,
	)
	if err != nil {
		var capacity *model.DeliveryMetadataCapacity
		if errors.As(err, &capacity) {
			return nil, s.browserDeliveryFailure(ctx, deliveryAccess, err)
		}

		var refusal *store.BrowserDeliveryRefusal
		if errors.As(err, &refusal) {
			return nil, s.browserDeliveryFailure(ctx, deliveryAccess, err)
		}

		return nil, s.failAudit(ctx, audit, s.browserDeliveryFailure(ctx, deliveryAccess, err))
	}

	if value == nil || value.Validate() != nil || value.SourceSessionID != deliveryAccess.SourceSessionID {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
	}

	return value, nil
}

// UpdateBrowserDeliverySummary idempotently records the client's summary of
// Browser Activity it did not retain for one source session.
func (s *Service) UpdateBrowserDeliverySummary(
	ctx context.Context,
	invocation invocation.Invocation,
	command BrowserDeliverySummaryCommand,
) (_ *model.BrowserDeliverySummaryResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	deliveryAccess, err := browserSourceSelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}
	if command.Summary.Validate() != nil {
		return nil, apperror.Invalid("exam.attempt.invalid", "delivery_summary")
	}

	idem, err := idempotency.Require(invocation, store.BrowserDeliverySummaryOperation, command.IdempotencyKey, struct {
		Attempt model.ExamAttemptID
		Source  model.BrowserSourceSessionID
		Summary model.UnretainedDeliverySummary
	}{deliveryAccess.Access.AttemptID, deliveryAccess.SourceSessionID, command.Summary})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginBrowserDeliveryAudit(ctx, invocation, deliveryAccess, store.BrowserDeliverySummaryOperation)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.UpdateBrowserDeliverySummary(
		ctx,
		&store.BrowserDeliverySummaryUpdate{Access: deliveryAccess, Summary: command.Summary, AuditEventID: audit, AuditAt: s.now()},
		idem,
	)
	if err != nil {
		return nil, s.failAudit(ctx, audit, s.browserDeliveryFailure(ctx, deliveryAccess, err))
	}

	if value == nil ||
		value.Summary.Validate() != nil ||
		value.Status.Validate() != nil ||
		value.Status.SourceSessionID != deliveryAccess.SourceSessionID {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
	}

	return value, nil
}

// BrowserDeliveryReceipts returns up to limit (at most 64) acknowledged
// receipts for one Browser Activity source session, starting at sequence
// first, with NextSequence set when another page follows.
func (s *Service) BrowserDeliveryReceipts(
	ctx context.Context,
	invocation invocation.Invocation,
	query BrowserSourceQuery,
	first int64,
	limit int,
) (_ *model.BrowserReceiptPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	deliveryAccess, err := browserSourceSelector(invocation, query)
	if err != nil {
		return nil, err
	}
	if first < 1 || first > model.BrowserParticipationPositionLimit || limit < 1 || limit > 64 {
		return nil, apperror.Invalid("exam.attempt.invalid", "receipt_page")
	}

	value, err := s.attempts.BrowserDeliveryReceipts(ctx, deliveryAccess, first, limit)
	if err != nil {
		return nil, s.browserDeliveryFailure(ctx, deliveryAccess, err)
	}
	if value == nil || value.Receipts == nil || len(value.Receipts) > limit {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
	}

	previous := first - 1
	for _, receipt := range value.Receipts {
		if receipt.Validate() != nil || receipt.Sequence <= previous {
			return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
		}

		previous = receipt.Sequence
	}

	if value.NextSequence != nil &&
		(*value.NextSequence <= previous || *value.NextSequence > model.BrowserParticipationPositionLimit || len(value.Receipts) != limit) {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
	}

	return value, nil
}

// AppendHistoricalBrowserDelivery idempotently delivers a Browser Activity
// batch outside a live Attempt Connection, so the access must carry no
// connection or continuity credential. Delivery refusals are reported without
// marking the audit failed.
func (s *Service) AppendHistoricalBrowserDelivery(
	ctx context.Context,
	invocation invocation.Invocation,
	command BrowserDeliveryAppendCommand,
) (_ *model.BrowserActivityAcknowledgement, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	deliveryAccess, err := browserSourceSelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}

	if command.Batch.Validate() != nil ||
		command.Batch.SourceSessionID != deliveryAccess.SourceSessionID ||
		deliveryAccess.Access.ConnectionID != "" ||
		deliveryAccess.Access.ContinuityCredentialHash != "" {
		return nil, apperror.Invalid("exam.attempt.invalid", "historical_browser_append")
	}

	deliveryAccess.ParticipationID = command.Batch.ParticipationID
	idem, err := idempotency.Require(invocation, store.BrowserDeliveryAppendOperation, command.IdempotencyKey, struct {
		Attempt model.ExamAttemptID
		Batch   model.BrowserActivityBatch
	}{deliveryAccess.Access.AttemptID, command.Batch})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginBrowserDeliveryAudit(ctx, invocation, deliveryAccess, store.BrowserDeliveryAppendOperation)
	if err != nil {
		return nil, err
	}

	b := command.Batch
	value, err := s.attempts.AppendHistoricalBrowserDelivery(
		ctx,
		&store.BrowserActivityAppend{
			Access:           deliveryAccess.Access,
			SourceSessionID:  b.SourceSessionID,
			ParticipationID:  b.ParticipationID,
			Generation:       b.Generation,
			PolicyRevisionID: b.PolicyRevisionID,
			PolicyDigest:     b.PolicyDigest,
			Events:           b.Events,
			AuditEventID:     audit,
			AuditAt:          s.now(),
		},
		idem,
	)
	if err != nil {
		var refusal *store.BrowserDeliveryRefusal
		if errors.As(err, &refusal) {
			return nil, s.browserDeliveryFailure(ctx, deliveryAccess, err)
		}

		return nil, s.failAudit(ctx, audit, s.browserDeliveryFailure(ctx, deliveryAccess, err))
	}

	if !validBrowserActivityAcknowledgement(value, b.SourceSessionID, b.Events) {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrDeliveryInvalid)
	}

	return value, nil
}
