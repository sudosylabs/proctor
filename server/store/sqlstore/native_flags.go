// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// nativeFlagRaiser raises the native Flags of one interpretation pass. Each
// rule has one Flag per Participation generation; every enforce
// occurrence that opens adds one piece of evidence to it, up to
// model.NativeFlagEvidenceLimit. Observe rules, unknown stretches and closed
// occurrences raise nothing.
type nativeFlagRaiser struct {
	owner   nativeDeliveryOwner
	binding admittedSecurityBinding
	flagged []store.NativeIntegrityFlagged
	// suspended is set once this pass suspended the Attempt.
	suspended bool
}

func (r *nativeFlagRaiser) raise(ctx context.Context, tx *sqlxTxWrapper, transitions []model.NativeRuleTransition) error {
	security := r.binding.Security
	attempt := security.Policy.Scope.AttemptID
	for _, transition := range transitions {
		if !transition.RaisesFlag() {
			continue
		}
		var retired bool
		if err := tx.Get(
			ctx,
			&retired,
			`SELECT EXISTS(
				SELECT 1 FROM exam_submissions
				WHERE exam_attempt_id = ? AND integrity_retired_at IS NOT NULL
			)`,
			attempt.String(),
		); err != nil {
			return err
		}
		if retired {
			return nil
		}

		kind := model.NativeIntegrityPolicyKind(transition.Capability)
		var existing struct {
			ID       string `db:"id"`
			Evidence int    `db:"evidence"`
		}
		err := tx.Get(
			ctx,
			&existing,
			`SELECT f.id, (SELECT count(*) FROM integrity_evidence e WHERE e.integrity_flag_id = f.id) AS evidence
			FROM integrity_flags f
			WHERE f.exam_attempt_id = ? AND f.generation = ? AND f.native_rule_id = ?
			FOR UPDATE OF f`,
			attempt.String(),
			security.Generation,
			transition.RuleID,
		)
		var flagID model.IntegrityFlagID
		clipRequested := false
		switch {
		case errors.Is(err, sql.ErrNoRows):
			flag, flagErr := model.NewNativeIntegrityFlag(model.NewIntegrityFlagID(), attempt, security.Generation,
				transition.Capability, transition.RuleID, r.owner.Now)
			if flagErr != nil {
				return flagErr
			}
			if clipRequested, flagErr = insertIntegrityFlag(ctx, tx, flag); flagErr != nil {
				return flagErr
			}
			flagID = flag.ID
		case err != nil:
			return err
		default:
			if flagID, err = model.ParseIntegrityFlagID(existing.ID); err != nil {
				return invalidPersistedState("integrity_flag", "id", err)
			}
		}

		retained := existing.Evidence
		if retained < model.NativeFlagEvidenceLimit {
			evidence, evidenceErr := model.NewNativeRuleEvidence(model.NewIntegrityEvidenceID(), attempt,
				security.ParticipationID, flagID, security.Generation, transition, r.owner.Now)
			if evidenceErr != nil {
				return evidenceErr
			}
			if evidenceErr = insertIntegrityEvidence(ctx, tx, evidence); evidenceErr != nil {
				return evidenceErr
			}
			retained++
		}
		notice := store.NativeIntegrityFlagged{FlagID: flagID, Kind: kind, RetainedEvidence: retained, ScreenClipRequested: clipRequested}
		if security.Policy.NativeRuleOutcome(transition.RuleID) == model.IntegrityOutcomeFlagAndSuspend && !r.suspended {
			if notice.Suspended, err = suspendForNativeRule(ctx, tx, security, flagID, kind); err != nil {
				return err
			}
			r.suspended = notice.Suspended != nil
		}
		r.note(notice)
	}
	return nil
}

// note keeps the latest notice of each Flag.
func (r *nativeFlagRaiser) note(flagged store.NativeIntegrityFlagged) {
	if i := slices.IndexFunc(r.flagged, func(f store.NativeIntegrityFlagged) bool { return f.FlagID == flagged.FlagID }); i >= 0 {
		flagged.ScreenClipRequested = flagged.ScreenClipRequested || r.flagged[i].ScreenClipRequested
		if flagged.Suspended == nil {
			flagged.Suspended = r.flagged[i].Suspended
		}
		r.flagged[i] = flagged
		return
	}
	r.flagged = append(r.flagged, flagged)
}

// suspendForNativeRule suspends the Attempt for an enforce occurrence whose
// family's outcome is flag_and_suspend, the same way Focus Loss does: it
// ends the live Participation, closes its Connection and native delivery,
// and opens the policy Suspension linked to the Flag. An Attempt that is not
// active, or a Participation with no open Connection, is left as it is; its
// Flag stands.
func suspendForNativeRule(
	ctx context.Context,
	tx *sqlxTxWrapper,
	security model.AdmittedSecurity,
	flagID model.IntegrityFlagID,
	kind model.IntegrityPolicyKind,
) (*store.NativeIntegritySuspended, error) {
	var row focusLossAccessRow
	err := tx.Get(
		ctx,
		&row,
		`SELECT `+focusLossAccessColumns+`
		FROM exam_attempts a
		JOIN exam_sittings s ON s.id = a.exam_sitting_id
			AND s.exam_id = a.exam_id
		JOIN exam_revisions r ON r.id = a.admission_revision_id
			AND r.sealed = true
		JOIN exam_attempt_participations p ON p.id = ?
			AND p.exam_attempt_id = a.id
		JOIN exam_attempt_connections co ON co.participation_id = p.id
			AND co.exam_attempt_id = a.id
			AND co.state = 'open'
		JOIN sessions se ON se.id = co.session_id
		JOIN users u ON u.id = se.user_id
		WHERE a.id = ?
		FOR UPDATE OF a, p, co
		FOR SHARE OF s, r, se, u`,
		security.ParticipationID.String(),
		security.Policy.Scope.AttemptID.String(),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.AttemptState != string(model.ExamAttemptActive) ||
		row.ParticipationState != string(model.AttemptParticipationActive) ||
		row.Generation != security.Generation {
		return nil, nil
	}
	if err = tx.Get(ctx, &row.DatabaseNow, `SELECT statement_timestamp()`); err != nil {
		return nil, err
	}
	row.DatabaseNow = model.TimeUTC(row.DatabaseNow)

	suspensionID := model.NewAttemptSuspensionID()
	if _, err = suspendForIntegrityPolicy(ctx, tx, row, flagID.String(), suspensionID, kind); err != nil {
		return nil, err
	}
	suspension, err := model.NewIntegrityPolicySuspension(suspensionID, security.Policy.Scope.AttemptID,
		security.ParticipationID, flagID, security.Generation, kind, row.DatabaseNow)
	if err != nil {
		return nil, err
	}
	connectionID, err := model.ParseAttemptConnectionID(row.ConnectionID)
	if err != nil {
		return nil, invalidPersistedState("attempt_connection", "id", err)
	}
	return &store.NativeIntegritySuspended{
		Suspension: store.ExamAttemptSuspensionView{
			ID: suspension.ID, AttemptID: suspension.AttemptID, ParticipationID: suspension.ParticipationID,
			FlagID: suspension.FlagID, Generation: suspension.Generation, State: suspension.State,
			Source: suspension.Source, CandidateReason: suspension.CandidateReason, StartedAt: suspension.StartedAt,
			EndedAt: suspension.EndedAt,
		},
		AttemptRevision: row.AttemptRevision + 1,
		ClosedConnection: store.ExamAttemptManagerConnection{
			ID: connectionID, State: model.AttemptConnectionClosed, OpenedAt: model.TimeUTC(row.ConnectionOpenedAt),
			ClosedAt: model.OptionalTime{Time: row.DatabaseNow, Valid: true}, CloseReason: model.AttemptConnectionClosePolicySuspended,
		},
	}, nil
}
