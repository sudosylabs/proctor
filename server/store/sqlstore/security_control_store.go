// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type nativeControlOwnerRow struct {
	Binding      []byte `db:"binding_canonical"`
	Ledger       []byte `db:"control_ledger_canonical"`
	Allowed      bool   `db:"security_interaction_allowed"`
	Allocated    int64  `db:"allocated_through_sequence"`
	Acknowledged int64  `db:"acknowledged_through_sequence"`
	SummaryOnly  bool   `db:"summary_only"`
}

// securityControlBudget is the locked Attempt delivery budget a security
// control charges.
type securityControlBudget struct {
	Metadata    int64 `db:"control_metadata_bytes"`
	Positions   int64 `db:"native_allocated_positions"`
	SummaryOnly bool  `db:"native_summary_only"`
}

// processSecurityRenewal runs under the existing Attempt/Participation/Session
// locks. It deliberately does not renew or expire the Participation lease.
// It checks the control against the admitted policy, replays a processed
// control, and applies the delivery watermarks.
func (s *SQLExamAttemptStore) processSecurityRenewal(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamAttemptParticipationRenewal,
	sittingOpen bool,
	now time.Time,
) (model.SecurityRenewalResult, error) {
	var zero model.SecurityRenewalResult
	control := input.Security
	raw, err := control.Canonical()
	if err != nil {
		return zero, store.NewErrInvalidInput("security_control", "body", nil)
	}

	digest := model.SHA256Fingerprint(raw)
	budget, owner, binding, err := lockSecurityControlOwner(ctx, tx, input)
	if err != nil {
		return zero, err
	}

	admitted := binding.Security
	if err = checkSecurityControlPolicy(ctx, tx, input, admitted, now); err != nil {
		return zero, err
	}
	if err = checkSecurityControlWatermarks(control, admitted, owner); err != nil {
		return zero, err
	}

	browserSources, err := lockBrowserControlSources(ctx, tx, input)
	if err != nil {
		return zero, err
	}

	var ledger nativeControlLedger
	if err = json.Unmarshal(owner.Ledger, &ledger); err != nil || ledger.Validate() != nil {
		return zero, invalidPersistedState("security_owner", "control_ledger", model.ErrSecurityControlInvalid)
	}

	receipt, processed, err := ledger.Lookup(control.ControlSequence, digest)
	if err != nil {
		return zero, store.NewErrConflict("security_control", "control_conflict", nil)
	}

	if processed {
		return projectSecurityControl(ctx, tx, input.ParticipationID, admitted.DeliveryStreamID, ledger, receipt, owner.Allowed, sittingOpen)
	}

	outcome := securityControlReceipt{Sequence: control.ControlSequence, Digest: digest, Rejections: [][2]int64{}}
	if err = applySecurityControlWatermarks(ctx, tx, input, browserSources, &owner, &budget, &outcome); err != nil {
		return zero, err
	}

	ledger, err = storeSecurityControl(ctx, tx, input, raw, &owner, budget, ledger, outcome)
	if err != nil {
		return zero, err
	}

	return projectSecurityControl(ctx, tx, input.ParticipationID, admitted.DeliveryStreamID, ledger, &outcome, owner.Allowed, sittingOpen)
}

// lockSecurityControlOwner locks the Attempt delivery budget and the security
// owner and checks the control against the owner's admitted binding.
func lockSecurityControlOwner(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamAttemptParticipationRenewal,
) (securityControlBudget, nativeControlOwnerRow, admittedSecurityBinding, error) {
	control := input.Security
	var budget securityControlBudget
	if err := tx.Get(
		ctx,
		&budget,
		`SELECT control_metadata_bytes, native_allocated_positions, native_summary_only
		FROM exam_attempt_delivery_budgets
		WHERE exam_attempt_id = ?
		FOR UPDATE`,
		input.AttemptID.String(),
	); err != nil {
		return securityControlBudget{}, nativeControlOwnerRow{}, admittedSecurityBinding{}, err
	}

	var owner nativeControlOwnerRow
	if err := tx.Get(
		ctx,
		&owner,
		`SELECT binding_canonical, control_ledger_canonical,
			security_interaction_allowed, allocated_through_sequence, acknowledged_through_sequence,
			summary_only
		FROM exam_attempt_security_owners
		WHERE participation_id = ?
			AND exam_attempt_id = ?
			AND session_id = ?
			AND registration_id = ?
			AND key_thumbprint = ?
		FOR UPDATE`,
		input.ParticipationID.String(),
		input.AttemptID.String(),
		input.SessionID.String(),
		input.DesktopRegistrationID.String(),
		input.DPoPKeyThumbprint,
	); err != nil {
		return securityControlBudget{}, nativeControlOwnerRow{}, admittedSecurityBinding{}, translateError(
			"security_owner",
			input.ParticipationID.String(),
			err,
		)
	}

	var binding admittedSecurityBinding
	if err := json.Unmarshal(owner.Binding, &binding); err != nil {
		return securityControlBudget{}, nativeControlOwnerRow{}, admittedSecurityBinding{}, invalidPersistedState(
			"security_owner",
			"binding",
			err,
		)
	}

	admitted := binding.Security
	if admitted.Validate() != nil || admitted.Generation != input.Generation || admitted.Policy.Scope.AttemptID != input.AttemptID {
		return securityControlBudget{}, nativeControlOwnerRow{}, admittedSecurityBinding{}, invalidPersistedState(
			"security_owner",
			"binding",
			model.ErrSecurityPolicyInvalid,
		)
	}

	if control.PolicyDigest != admitted.Policy.Digest ||
		control.StreamID != admitted.DeliveryStreamID ||
		control.SecuritySessionID != admitted.SecuritySessionID {
		return securityControlBudget{}, nativeControlOwnerRow{}, admittedSecurityBinding{}, preflightConflict("policy_changed")
	}

	if input.DesktopBuild.NativeAgreement == nil || input.DesktopBuild.NativeAgreement.MatrixDigest() != binding.CapabilityMatrixDigest {
		return securityControlBudget{}, nativeControlOwnerRow{}, admittedSecurityBinding{}, preflightConflict("configuration_unsupported")
	}

	return budget, owner, binding, nil
}

// checkSecurityControlPolicy re-resolves the admitted native policy for the
// renewing build: a renewal under a policy that changed is refused.
func checkSecurityControlPolicy(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamAttemptParticipationRenewal,
	admitted model.AdmittedSecurity,
	now time.Time,
) error {
	var examIDString string
	if err := tx.Get(ctx, &examIDString, `SELECT exam_id FROM exam_attempts WHERE id = ?`, input.AttemptID.String()); err != nil {
		return err
	}

	examID, err := model.ParseExamID(examIDString)
	if err != nil {
		return err
	}

	revision, err := getExamRevisionSnapshot(ctx, tx, examID, admitted.Policy.ExamRevisionID)
	if err != nil {
		return err
	}

	selections, err := model.DecodeExamPolicySet(revision.Policy.Bytes)
	if err != nil {
		return err
	}

	policy := admitted.Policy
	resolved, err := model.ResolveNativePolicy(model.NativePolicyResolution{
		PolicyID:       policy.PolicyID,
		Revision:       policy.Revision,
		Ordinal:        policy.Ordinal,
		InstitutionID:  policy.InstitutionID,
		ExamRevisionID: policy.ExamRevisionID,
		SittingID:      policy.SittingID,
		Scope:          policy.Scope,
		IssuedAt:       policy.IssuedAt,
		ActiveFrom:     policy.ActiveFrom,
		ActivationTime: now.UTC().Truncate(time.Millisecond),
		Selections:     selections,
		Build:          input.DesktopBuild,
	})
	if err != nil {
		return err
	}
	if resolved.Policy.Digest != policy.Digest || resolved.PolicyContentDigest != admitted.PolicyContentDigest {
		return preflightConflict("policy_changed")
	}
	return nil
}

// checkSecurityControlWatermarks requires every native watermark to name the
// owner's stream within its acknowledgement. Native ownership exists at
// admission; Browser watermarks are installed by their separate source
// owner, and no caller-supplied selector establishes one.
func checkSecurityControlWatermarks(control model.SecurityRenewal, admitted model.AdmittedSecurity, owner nativeControlOwnerRow) error {
	for _, watermark := range control.DeliveryWatermarks {
		if watermark.Family == "browser" {
			continue
		}
		if watermark.SourceID != admitted.DeliveryStreamID {
			return store.NewErrNotFound("delivery_source", watermark.SourceID)
		}
		if watermark.AcknowledgedThroughSequence > owner.Acknowledged {
			return store.NewErrInvalidInput("security_control", "acknowledgement", nil)
		}
	}
	return nil
}

// applySecurityControlWatermarks applies each delivery watermark, charging
// native positions to the owner and budget and recording rejections.
func applySecurityControlWatermarks(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamAttemptParticipationRenewal,
	browserSources map[string]browserControlSource,
	owner *nativeControlOwnerRow,
	budget *securityControlBudget,
	outcome *securityControlReceipt,
) error {
	for _, watermark := range input.Security.DeliveryWatermarks {
		if watermark.Family == "browser" {
			rejection, err := applyBrowserControlWatermark(ctx, tx, input, watermark, browserSources[watermark.SourceID])
			if err != nil {
				return err
			}
			if rejection != nil {
				outcome.Rejections = append(outcome.Rejections, *rejection)
			}

			continue
		}

		delta := watermark.AllocatedThroughSequence - owner.Allocated
		if delta <= 0 {
			continue
		}

		if owner.SummaryOnly ||
			budget.SummaryOnly ||
			delta > model.NativeParticipationPositionLimit-owner.Allocated ||
			delta > model.NativeAttemptPositionLimit-budget.Positions {
			outcome.Rejections = append(outcome.Rejections, [2]int64{0, 1})
			if delta > model.NativeAttemptPositionLimit-budget.Positions {
				budget.SummaryOnly = true
			} else {
				owner.SummaryOnly = true
			}

			continue
		}

		owner.Allocated = watermark.AllocatedThroughSequence
		budget.Positions += delta
	}

	return nil
}

// storeSecurityControl records the control in the ledger and writes the owner
// and budget, returning the updated ledger. Whether the candidate may
// interact is not the renewal's to decide: Desktop contains the window on the
// device, and closing native delivery withdraws interaction on the server.
func storeSecurityControl(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamAttemptParticipationRenewal,
	raw []byte,
	owner *nativeControlOwnerRow,
	budget securityControlBudget,
	ledger nativeControlLedger,
	outcome securityControlReceipt,
) (nativeControlLedger, error) {
	ledger, err := ledger.Record(outcome)
	if err != nil {
		return nativeControlLedger{}, err
	}

	ledgerJSON, err := json.Marshal(ledger)
	if err != nil {
		return nativeControlLedger{}, err
	}

	ledgerRaw, err := canonicaljson.Canonicalize(ledgerJSON, model.SecurityControlCacheMaxBytes+512)
	if err != nil {
		return nativeControlLedger{}, err
	}

	if _, err := tx.Exec(
		ctx,
		`UPDATE exam_attempt_security_owners
		SET control_ledger_canonical = ?, control_body_canonical = ?,
			allocated_through_sequence = ?, summary_only = ?,
			stop_reason = CASE WHEN ? THEN COALESCE(stop_reason, 'positions') ELSE stop_reason END,
			terminal_missing_through_sequence = CASE WHEN ? THEN ? ELSE terminal_missing_through_sequence END
		WHERE participation_id = ?`,
		ledgerRaw,
		raw,
		owner.Allocated,
		owner.SummaryOnly,
		owner.SummaryOnly,
		owner.SummaryOnly || budget.SummaryOnly,
		owner.Allocated,
		input.ParticipationID.String(),
	); err != nil {
		return nativeControlLedger{}, err
	}

	if _, err := tx.Exec(
		ctx,
		`UPDATE exam_attempt_delivery_budgets
		SET control_metadata_bytes = ?, native_allocated_positions = ?, native_summary_only = ?,
			native_stop_reason = CASE
				WHEN ? THEN COALESCE(native_stop_reason, 'positions')
				ELSE native_stop_reason
			END
		WHERE exam_attempt_id = ?`,
		budget.Metadata,
		budget.Positions,
		budget.SummaryOnly,
		budget.SummaryOnly,
		input.AttemptID.String(),
	); err != nil {
		return nativeControlLedger{}, err
	}

	if budget.SummaryOnly {
		if _, err := tx.Exec(
			ctx,
			`UPDATE exam_attempt_security_owners SET terminal_missing_through_sequence = allocated_through_sequence WHERE exam_attempt_id = ?`,
			input.AttemptID.String(),
		); err != nil {
			return nativeControlLedger{}, err
		}
	}

	return ledger, nil
}
func projectSecurityControl(
	ctx context.Context,
	tx *sqlxTxWrapper,
	participationID model.AttemptParticipationID,
	streamID string,
	ledger nativeControlLedger,
	receipt *securityControlReceipt,
	allowed, sittingOpen bool,
) (model.SecurityRenewalResult, error) {
	result := model.SecurityRenewalResult{
		ProcessedControlSequence:    ledger.ProcessedSequence,
		ProcessedControlDigest:      ledger.ProcessedDigest,
		SecurityInteractionAllowed:  allowed && sittingOpen,
		DeliveryWatermarkRejections: []model.DeliveryWatermarkRejection{},
	}
	if receipt != nil {
		for _, rejection := range receipt.Rejections {
			family, sourceID := "native", streamID
			if rejection[0] != 0 {
				family = "browser"
				if err := tx.Get(
					ctx,
					&sourceID,
					`SELECT id::text
					FROM browser_activity_sources
					WHERE participation_id = ?
						AND start_ordinal = ?
					UNION ALL
					SELECT source_session_id::text
					FROM browser_delivery_retired_sources
					WHERE participation_id = ?
						AND start_ordinal = ?`,
					participationID.String(),
					rejection[0],
					participationID.String(),
					rejection[0],
				); err != nil {
					return result, err
				}
			}

			reason := "closed_source"
			if rejection[1] == 1 {
				reason = "position_limit"
			}

			result.DeliveryWatermarkRejections = append(result.DeliveryWatermarkRejections, model.DeliveryWatermarkRejection{
				Family:   family,
				SourceID: sourceID,
				Reason:   reason,
			})
		}
	}

	return result, nil
}

// UpdateSecurity records the security control sent over the Candidate's
// open Connection, applying the same Participation, Connection, and
// credential checks as RenewParticipation without extending the lease.
func (s *SQLExamAttemptStore) UpdateSecurity(
	ctx context.Context,
	input *store.ExamAttemptSecurityUpdate,
) (model.SecurityRenewalResult, error) {
	if input == nil {
		return model.SecurityRenewalResult{}, store.NewErrInvalidInput("security_control", "update", nil)
	}
	result, err := s.changeParticipationControl(
		ctx,
		&store.ExamAttemptParticipationRenewal{
			AttemptID:                          input.Access.AttemptID,
			ParticipationID:                    input.ParticipationID,
			ConnectionID:                       input.Access.ConnectionID,
			CandidateUserID:                    input.Access.CandidateUserID,
			SessionID:                          input.Access.SessionID,
			DesktopRegistrationID:              input.Access.DesktopRegistrationID,
			DPoPKeyThumbprint:                  input.Access.DPoPKeyThumbprint,
			Generation:                         input.Generation,
			ContinuityCredentialHash:           input.Access.ContinuityCredentialHash,
			DesktopCompatibilityPolicyRevision: input.DesktopCompatibilityPolicyRevision,
			DesktopBuild:                       input.DesktopBuild,
			Security:                           input.Control,
		},
		false,
	)
	if err != nil {
		return model.SecurityRenewalResult{}, err
	}
	return result.Security, nil
}

func recoverNativeControl(
	ctx context.Context,
	tx *sqlxTxWrapper,
	participationID model.AttemptParticipationID,
	streamID string,
	sittingOpen bool,
) (*store.SecurityPolicyRecovery, error) {
	var owner nativeControlOwnerRow
	if err := tx.Get(
		ctx,
		&owner,
		`SELECT control_ledger_canonical, security_interaction_allowed
		FROM exam_attempt_security_owners
		WHERE participation_id = ?
		FOR SHARE`,
		participationID.String(),
	); err != nil {
		return nil, err
	}

	var ledger nativeControlLedger
	if unmarshalErr := json.Unmarshal(owner.Ledger, &ledger); unmarshalErr != nil || ledger.Validate() != nil {
		return nil, invalidPersistedState("security_owner", "control_ledger", model.ErrSecurityControlInvalid)
	}

	var latest *securityControlReceipt
	if len(ledger.Receipts) > 0 {
		value := ledger.Receipts[len(ledger.Receipts)-1]
		latest = &value
	}

	result, err := projectSecurityControl(ctx, tx, participationID, streamID, ledger, latest, owner.Allowed, sittingOpen)
	if err != nil {
		return nil, err
	}
	return &store.SecurityPolicyRecovery{Renewal: result}, nil
}

// lockSecurityInteraction is called only by mutation owners, after
// their Attempt lock. Recovery, monitoring, and renewal never use this gate.
func lockSecurityInteraction(ctx context.Context, tx *sqlxTxWrapper, attemptID model.ExamAttemptID) error {
	var allowed bool
	err := tx.Get(
		ctx,
		&allowed,
		`SELECT o.security_interaction_allowed AND p.lease_expires_at > clock_timestamp() AS allowed
		FROM exam_attempt_security_owners o
		JOIN exam_attempt_participations p ON p.id = o.participation_id
		WHERE o.exam_attempt_id = ?
			AND p.state = 'active'
		FOR SHARE OF o, p`,
		attemptID.String(),
	)
	if err != nil {
		return translateError("security_owner", attemptID.String(), err)
	}
	if !allowed {
		return preflightConflict("posture_blocked")
	}
	return nil
}
