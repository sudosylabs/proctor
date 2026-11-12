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
// capability has one Flag per Participation generation; every enforce
// occurrence that opens adds one piece of evidence to it, up to
// model.NativeFlagEvidenceLimit. Observe rules, unknown stretches and closed
// occurrences raise nothing.
type nativeFlagRaiser struct {
	owner   nativeDeliveryOwner
	binding admittedSecurityBinding
	flagged []store.NativeIntegrityFlagged
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
			WHERE f.exam_attempt_id = ? AND f.generation = ? AND f.policy_kind = ?
			ORDER BY f.created_at, f.id
			LIMIT 1
			FOR UPDATE OF f`,
			attempt.String(),
			security.Generation,
			kind,
		)
		var flagID model.IntegrityFlagID
		switch {
		case errors.Is(err, sql.ErrNoRows):
			flag, flagErr := model.NewIntegrityFlag(model.NewIntegrityFlagID(), attempt, security.Generation, kind, r.owner.Now)
			if flagErr != nil {
				return flagErr
			}
			if flagErr = insertIntegrityFlag(ctx, tx, flag); flagErr != nil {
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
		r.note(store.NativeIntegrityFlagged{FlagID: flagID, Kind: kind, RetainedEvidence: retained})
	}
	return nil
}

// note keeps the latest notice of each Flag.
func (r *nativeFlagRaiser) note(flagged store.NativeIntegrityFlagged) {
	if i := slices.IndexFunc(r.flagged, func(f store.NativeIntegrityFlagged) bool { return f.FlagID == flagged.FlagID }); i >= 0 {
		r.flagged[i] = flagged
		return
	}
	r.flagged = append(r.flagged, flagged)
}
