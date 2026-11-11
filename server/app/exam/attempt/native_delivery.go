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
	"slices"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type NativeDeliveryQuery struct {
	Access   CandidateAccess
	StreamID string
}
type NativeDeliveryGapsCommand struct {
	Query          NativeDeliveryQuery
	Declaration    model.DeclareDeliveryGaps
	IdempotencyKey string
}
type NativeDeliveryFinalCommand struct {
	Query          NativeDeliveryQuery
	Declaration    model.FinalDeliveryDeclaration
	IdempotencyKey string
}
type NativeDeliverySummaryCommand struct {
	IdempotencyKey string
	Query          NativeDeliveryQuery
	Summary        model.UnretainedDeliverySummary
}

func nativeDeliverySelector(invocation invocation.Invocation, query NativeDeliveryQuery) (store.NativeDeliveryAccess, error) {
	p := invocation.Principal()
	if p.Validate() != nil || p.CredentialType != model.CredentialSessionAccess {
		return store.NativeDeliveryAccess{}, apperror.New("authentication.token_invalid")
	}
	if !p.HasRegisteredDesktopKey() {
		return store.NativeDeliveryAccess{}, apperror.New("exam.attempt.registered_desktop_required")
	}

	a := query.Access
	if !a.AttemptID.IsValid() ||
		!model.IsValidAgreementID(query.StreamID) ||
		a.ConnectionID != "" && !a.ConnectionID.IsValid() ||
		a.ContinuityCredential != "" && !model.IsValidCredentialToken(a.ContinuityCredential) ||
		(a.ConnectionID == "") != (a.ContinuityCredential == "") {
		return store.NativeDeliveryAccess{}, apperror.Invalid("exam.attempt.invalid", "native_delivery_access")
	}

	credentialHash := ""
	if a.ContinuityCredential != "" {
		credentialHash = model.HashToken(a.ContinuityCredential)
	}

	return store.NativeDeliveryAccess{
		Access: store.CandidateAttemptAccess{
			AttemptID:                a.AttemptID,
			ConnectionID:             a.ConnectionID,
			ContinuityCredentialHash: credentialHash,
			CandidateUserID:          p.UserID,
			SessionID:                p.SessionID,
			DesktopRegistrationID:    p.DesktopRegistrationID,
			DPoPKeyThumbprint:        p.DPoPKeyThumbprint,
		},
		StreamID: query.StreamID,
	}, nil
}

// NativeDeliveryStatus returns the delivery status of one native security
// stream of the candidate's Attempt, as seen from a registered desktop.
func (s *Service) NativeDeliveryStatus(
	ctx context.Context,
	invocation invocation.Invocation,
	query NativeDeliveryQuery,
) (_ *model.NativeSecurityStreamStatus, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	a, err := nativeDeliverySelector(invocation, query)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.NativeDeliveryStatus(ctx, a)
	if err != nil {
		return nil, s.nativeDeliveryFailure(ctx, a, err)
	}
	if value == nil || value.Validate() != nil || value.StreamID != query.StreamID {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	return value, nil
}

// NativeDeliveryReceipt returns the receipt for the native security batch
// with the given sequence in one stream of the candidate's Attempt.
func (s *Service) NativeDeliveryReceipt(
	ctx context.Context,
	invocation invocation.Invocation,
	query NativeDeliveryQuery,
	sequence int64,
) (_ *model.NativeBatchReceipt, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	a, err := nativeDeliverySelector(invocation, query)
	if err != nil {
		return nil, err
	}
	if sequence < 1 || sequence > model.NativeParticipationPositionLimit {
		return nil, apperror.Invalid("exam.attempt.invalid", "batch_sequence")
	}

	value, err := s.attempts.NativeDeliveryReceipt(ctx, a, sequence)
	if err != nil {
		return nil, s.nativeDeliveryFailure(ctx, a, err)
	}
	if value == nil || value.Validate() != nil || value.StreamID != query.StreamID || value.BatchSequence != sequence {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	return value, nil
}
func (s *Service) beginNativeDeliveryAudit(
	ctx context.Context,
	invocation invocation.Invocation,
	deliveryAccess store.NativeDeliveryAccess,
	operation string,
) (model.AuditEventID, error) {
	target, err := s.attempts.ResolveNativeDeliveryTarget(ctx, deliveryAccess)
	if err != nil {
		return "", deliveryStoreError(err)
	}
	if target == nil || !target.SittingID.IsValid() || !target.ClassID.IsValid() {
		return "", apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	return s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    target.ClassID.String(),
		Operation:  operation,
		Value:      map[string]any{"stream_id": deliveryAccess.StreamID},
	})
}

// DeclareNativeDeliveryGaps idempotently records native security batches the
// client declares it can no longer deliver for one stream.
func (s *Service) DeclareNativeDeliveryGaps(
	ctx context.Context,
	invocation invocation.Invocation,
	command NativeDeliveryGapsCommand,
) (_ *model.DeliveryGapReceipt, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	deliveryAccess, err := nativeDeliverySelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}
	if command.Declaration.Validate() != nil {
		return nil, apperror.Invalid("exam.attempt.invalid", "delivery_gaps")
	}

	commandIdempotency, err := idempotency.Require(invocation, store.NativeDeliveryGapsOperation, command.IdempotencyKey, struct {
		AttemptID   model.ExamAttemptID
		StreamID    string
		Declaration model.DeclareDeliveryGaps
	}{deliveryAccess.Access.AttemptID, deliveryAccess.StreamID, command.Declaration})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginNativeDeliveryAudit(ctx, invocation, deliveryAccess, store.NativeDeliveryGapsOperation)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.DeclareNativeDeliveryGaps(
		ctx,
		&store.NativeDeliveryGapDeclaration{Access: deliveryAccess, Declaration: command.Declaration, AuditEventID: audit, AuditAt: s.now()},
		commandIdempotency,
	)
	if err != nil {
		var capacity *model.DeliveryMetadataCapacity
		if errors.As(err, &capacity) {
			return nil, s.nativeDeliveryFailure(ctx, deliveryAccess, err)
		}

		var refusal *store.NativeDeliveryRefusal
		if errors.As(err, &refusal) {
			return nil, s.nativeDeliveryFailure(ctx, deliveryAccess, err)
		}

		return nil, s.failAudit(ctx, audit, s.nativeDeliveryFailure(ctx, deliveryAccess, err))
	}

	if value == nil ||
		value.DeclarationID != command.Declaration.DeclarationID ||
		value.DeclarationRevision < 1 ||
		!model.IsValidSHA256Fingerprint(value.RequestDigest) {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	return value, nil
}

// SealNativeDelivery idempotently records the final delivered batch sequence
// for one native security stream, fenced by the declaration's expected
// revision.
func (s *Service) SealNativeDelivery(
	ctx context.Context,
	invocation invocation.Invocation,
	command NativeDeliveryFinalCommand,
) (_ *model.NativeSecurityStreamStatus, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	deliveryAccess, err := nativeDeliverySelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}
	if command.Declaration.Validate() != nil {
		return nil, apperror.Invalid("exam.attempt.invalid", "delivery_final")
	}

	commandIdempotency, err := idempotency.Require(invocation, store.NativeDeliveryFinalOperation, command.IdempotencyKey, struct {
		AttemptID   model.ExamAttemptID
		StreamID    string
		Declaration model.FinalDeliveryDeclaration
	}{deliveryAccess.Access.AttemptID, deliveryAccess.StreamID, command.Declaration})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginNativeDeliveryAudit(ctx, invocation, deliveryAccess, store.NativeDeliveryFinalOperation)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.SealNativeDelivery(
		ctx,
		&store.NativeDeliveryFinalDeclaration{Access: deliveryAccess, Declaration: command.Declaration, AuditEventID: audit, AuditAt: s.now()},
		commandIdempotency,
	)
	if err != nil {
		var capacity *model.DeliveryMetadataCapacity
		if errors.As(err, &capacity) {
			return nil, s.nativeDeliveryFailure(ctx, deliveryAccess, err)
		}

		var refusal *store.NativeDeliveryRefusal
		if errors.As(err, &refusal) {
			return nil, s.nativeDeliveryFailure(ctx, deliveryAccess, err)
		}

		return nil, s.failAudit(ctx, audit, s.nativeDeliveryFailure(ctx, deliveryAccess, err))
	}

	if value == nil || value.Validate() != nil || value.StreamID != deliveryAccess.StreamID {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	return value, nil
}

// UpdateNativeDeliverySummary idempotently records the client's summary of
// native security records it did not retain for one stream.
func (s *Service) UpdateNativeDeliverySummary(
	ctx context.Context,
	invocation invocation.Invocation,
	command NativeDeliverySummaryCommand,
) (_ *model.NativeSecurityStreamStatus, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	release, admissionErr := s.enterControl(ctx, invocation, false)
	if admissionErr != nil {
		return nil, admissionErr
	}
	defer release()

	deliveryAccess, err := nativeDeliverySelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}
	if command.Summary.Validate() != nil {
		return nil, apperror.Invalid("exam.attempt.invalid", "delivery_summary")
	}

	commandIdempotency, err := idempotency.Require(invocation, store.NativeDeliverySummaryOperation, command.IdempotencyKey, struct {
		AttemptID model.ExamAttemptID
		StreamID  string
		Summary   model.UnretainedDeliverySummary
	}{deliveryAccess.Access.AttemptID, deliveryAccess.StreamID, command.Summary})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginNativeDeliveryAudit(ctx, invocation, deliveryAccess, store.NativeDeliverySummaryOperation)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.UpdateNativeDeliverySummary(
		ctx,
		&store.NativeDeliverySummaryUpdate{Access: deliveryAccess, Summary: command.Summary, AuditEventID: audit, AuditAt: s.now()},
		commandIdempotency,
	)
	if err != nil {
		return nil, s.failAudit(ctx, audit, s.nativeDeliveryFailure(ctx, deliveryAccess, err))
	}
	if value == nil || value.Validate() != nil || value.StreamID != deliveryAccess.StreamID {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	return value, nil
}

// nativeDeliveryStoreErrors adds the native delivery sentinels and conflict
// reasons to the Attempt table.
var nativeDeliveryStoreErrors = storeErrors.With(slices.Concat(
	[]apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: model.ErrDeliveryExpired, Code: "exam.delivery.upload_expired"},
		{Kind: apperror.StoreSentinel, Sentinel: model.ErrDeliveryConflict, Code: "exam.delivery.declaration_conflict"},
	},
	apperror.Conflicts("native_delivery", deliveryConflictCodes),
)...)

// deliveryStoreError presents native and browser delivery failures.
func deliveryStoreError(err error) error {
	if !errors.Is(err, store.ErrInvalidState) && (errors.Is(err, model.ErrDeliveryInvalid) || errors.Is(err, model.ErrNativeDeliveryInvalid)) {
		return apperror.Invalid("exam.attempt.invalid", "native_delivery").Wrap(err)
	}
	return storeError(nativeDeliveryStoreErrors, err)
}

type NativeDeliveryAppendCommand struct {
	Query          NativeDeliveryQuery
	Batch          model.NativeSecurityBatch
	IdempotencyKey string
}

// AppendNativeDelivery idempotently appends one native security batch to a
// stream of the candidate's Attempt after checking it against the frozen
// policy's desktop build. The batch carries its own participation generation.
func (s *Service) AppendNativeDelivery(
	ctx context.Context,
	invocation invocation.Invocation,
	command NativeDeliveryAppendCommand,
) (_ *model.NativeSecurityAcknowledgement, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	deliveryAccess, err := nativeDeliverySelector(invocation, command.Query)
	if err != nil {
		return nil, err
	}
	if command.Batch.Validate() != nil || command.Batch.StreamID != deliveryAccess.StreamID {
		return nil, apperror.Invalid("exam.attempt.invalid", "native_batch")
	}

	deliveryAccess.ParticipationID, deliveryAccess.Generation = command.Batch.ParticipationID, command.Batch.Generation
	target, err := s.attempts.ResolveNativeDeliveryTarget(ctx, deliveryAccess)
	if err != nil {
		return nil, s.nativeDeliveryFailure(ctx, deliveryAccess, err)
	}
	if target == nil || target.Security.Validate() != nil {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	policy := target.Security.Policy
	build, err := s.desktopBuilds.ResolveNativeDeliveryBuild(ctx, NativeDeliveryBuildIdentity{
		ReleaseID:    policy.ApplicationReleaseID,
		MatrixID:     policy.MatrixID,
		MatrixDigest: target.MatrixDigest,
		TargetTuple:  policy.TargetTuple,
	})
	if err != nil {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(err)
	}

	commandIdempotency, err := idempotency.Require(invocation, store.NativeDeliveryAppendOperation, command.IdempotencyKey, struct {
		AttemptID model.ExamAttemptID
		Batch     model.NativeSecurityBatch
	}{deliveryAccess.Access.AttemptID, command.Batch})
	if err != nil {
		return nil, err
	}

	audit, err := s.beginNativeDeliveryAudit(ctx, invocation, deliveryAccess, store.NativeDeliveryAppendOperation)
	if err != nil {
		return nil, err
	}

	value, err := s.attempts.AppendNativeDelivery(
		ctx,
		&store.NativeDeliveryAppend{Access: deliveryAccess, Batch: command.Batch, DesktopBuild: build, AuditEventID: audit, AuditAt: s.now()},
		commandIdempotency,
	)
	if err != nil {
		var capacity *model.DeliveryMetadataCapacity
		if errors.As(err, &capacity) {
			return nil, s.nativeDeliveryFailure(ctx, deliveryAccess, err)
		}

		var refusal *store.NativeDeliveryRefusal
		if errors.As(err, &refusal) {
			return nil, s.nativeDeliveryFailure(ctx, deliveryAccess, err)
		}

		return nil, s.failAudit(ctx, audit, s.nativeDeliveryFailure(ctx, deliveryAccess, err))
	}

	if value == nil ||
		value.Receipt.Validate() != nil ||
		value.Receipt.StreamID != deliveryAccess.StreamID ||
		value.Receipt.BatchSequence != command.Batch.BatchSequence {
		return nil, apperror.New("exam.attempt.unavailable").Wrap(model.ErrNativeDeliveryInvalid)
	}

	return value, nil
}
