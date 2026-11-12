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

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
	"github.com/sudosylabs/proctor/server/model"
)

// Every integrity policy writes its decision through these three functions,
// inside the transaction that decided it, so every Flag, every piece of
// evidence and every policy Suspension has one shape whichever policy wrote
// it. Each policy still owns when it may create a Flag, how much evidence it
// retains, and how the Attempt, Participation and Connection it suspends
// change state. The session-revocation fence in the baseline schema writes
// Connection Loss records itself, in SQL, in the same shape.

// insertIntegrityFlag writes a new open Flag.
func insertIntegrityFlag(ctx context.Context, tx *sqlxTxWrapper, flag *model.IntegrityFlag) error {
	if err := flag.Validate(); err != nil {
		return err
	}

	if _, err := tx.Exec(
		ctx,
		`INSERT INTO integrity_flags (
			id, exam_attempt_id, generation, policy_kind, state, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?)`,
		flag.ID.String(),
		flag.AttemptID.String(),
		flag.Generation,
		flag.Kind,
		flag.State,
		flag.CreatedAt,
	); err != nil {
		return translateError("integrity_flag", flag.ID.String(), err)
	}

	return nil
}

// insertIntegrityEvidence writes one piece of evidence for an open Flag. Only
// the columns of the evidence's own kind are set.
func insertIntegrityEvidence(ctx context.Context, tx *sqlxTxWrapper, evidence *model.IntegrityEvidence) error {
	if err := evidence.Validate(); err != nil {
		return err
	}

	var (
		signalID, sequence, duration, source, missingBefore any
		browserDetail, detail                               any
	)
	switch {
	case evidence.Kind == model.IntegrityPolicyFocusLoss:
		signalID = evidence.SignalID.String()
		sequence = evidence.Sequence
		duration = evidence.DurationMilliseconds
		source = nullableFocusLossSource(evidence.Source)
		missingBefore = evidence.MissingBefore
	case evidence.Kind == model.IntegrityPolicyBrowserNavigation:
		canonical, err := canonicalBrowserIntegrityEvidence(*evidence.Browser)
		if err != nil {
			return err
		}
		browserDetail = canonical
	case evidence.Kind.IsCapability():
		detail = string(evidence.Detail)
	}

	if _, err := tx.Exec(
		ctx,
		`INSERT INTO integrity_evidence (
			id, exam_attempt_id, participation_id, integrity_flag_id, generation, policy_kind,
				focus_loss_signal_id, sequence, duration_milliseconds, source, missing_before,
				observed_at, recorded_at, browser_detail_canonical, detail
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)`,
		evidence.ID.String(),
		evidence.AttemptID.String(),
		evidence.ParticipationID.String(),
		evidence.FlagID.String(),
		evidence.Generation,
		evidence.Kind,
		signalID,
		sequence,
		duration,
		source,
		missingBefore,
		evidence.ObservedAt,
		evidence.RecordedAt,
		browserDetail,
		detail,
	); err != nil {
		return translateError("integrity_evidence", evidence.ID.String(), err)
	}

	return nil
}

// insertPolicySuspension writes the Suspension a policy opened.
// attemptRevision is the Attempt revision the suspension produced.
func insertPolicySuspension(
	ctx context.Context,
	tx *sqlxTxWrapper,
	suspension *model.AttemptSuspension,
	attemptRevision int64,
) error {
	if suspension == nil || suspension.Source != model.AttemptSuspensionSourcePolicy {
		return invalidPersistedState("attempt_suspension", "source", nil)
	}
	if err := suspension.Validate(); err != nil {
		return err
	}

	if _, err := tx.Exec(
		ctx,
		`INSERT INTO exam_attempt_suspensions (
			id, exam_attempt_id, participation_id, integrity_flag_id, generation,
				suspension_attempt_revision, state, source, candidate_reason, started_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		suspension.ID.String(),
		suspension.AttemptID.String(),
		suspension.ParticipationID.String(),
		suspension.FlagID.String(),
		suspension.Generation,
		attemptRevision,
		suspension.State,
		suspension.Source,
		suspension.CandidateReason,
		suspension.StartedAt,
	); err != nil {
		return translateError("attempt_suspension", suspension.ID.String(), err)
	}

	return nil
}

// canonicalBrowserIntegrityEvidence is the stored form of Browser evidence:
// canonical JSON within its byte bound.
func canonicalBrowserIntegrityEvidence(detail model.BrowserIntegrityEvidence) ([]byte, error) {
	if err := detail.Validate(); err != nil {
		return nil, err
	}

	raw, err := json.Marshal(detail)
	if err == nil {
		raw, err = canonicaljson.Canonicalize(raw, model.BrowserIntegrityEvidenceMaxBytes)
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > model.BrowserIntegrityEvidenceMaxBytes {
		return nil, model.ErrDeliveryInvalid
	}

	return raw, nil
}
