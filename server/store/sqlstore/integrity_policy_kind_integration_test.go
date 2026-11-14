//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// TestIntegrityPolicyKindConstraintsMatchTheModel proves the baseline accepts
// exactly the policy kinds model.IntegrityPolicyKind.IsValid accepts, and
// holds capability evidence to its bounded detail object. It probes copies of
// the tables that keep their CHECKs but not their foreign keys.
func TestIntegrityPolicyKindConstraintsMatchTheModel(t *testing.T) {
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	ctx := context.Background()
	db := persistence.GetMaster()
	for _, table := range []string{"integrity_flags", "integrity_evidence"} {
		probe := "probe_" + table
		_, err := db.Exec(ctx, `CREATE TABLE `+probe+` (LIKE `+table+` INCLUDING CONSTRAINTS)`)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, dropErr := db.Exec(context.Background(), `DROP TABLE IF EXISTS `+probe)
			require.NoError(t, dropErr)
		})
	}

	insertFlag := func(kind string) error {
		_, err := db.Exec(ctx, `INSERT INTO probe_integrity_flags (id,exam_attempt_id,generation,policy_kind,state,created_at)
			VALUES (?,?,1,?,'open',clock_timestamp())`,
			model.NewIntegrityFlagID().String(), model.NewExamAttemptID().String(), kind)
		return err
	}
	insertEvidence := func(kind string, detail any) error {
		_, err := db.Exec(ctx, `INSERT INTO probe_integrity_evidence
			(id,exam_attempt_id,participation_id,integrity_flag_id,generation,policy_kind,observed_at,recorded_at,detail)
			VALUES (?,?,?,?,1,?,clock_timestamp(),clock_timestamp(),?::jsonb)`,
			model.NewIntegrityEvidenceID().String(), model.NewExamAttemptID().String(),
			model.NewAttemptParticipationID().String(), model.NewIntegrityFlagID().String(), kind, detail)
		return err
	}
	requireCheckViolation := func(err error, message string) {
		t.Helper()
		var postgresErr *pq.Error
		require.ErrorAs(t, err, &postgresErr, message)
		require.Equal(t, "23514", string(postgresErr.Code), message)
	}

	kinds := []string{
		"connection_loss", "focus_loss", "browser_navigation",
		"display.second_screen", "proctor.display-topology.monitor_added",
		strings.Repeat("a", 64) + "." + strings.Repeat("b", 32),
		"", "unknown_core_kind", "Display.second_screen", "display.Second", "display.second-screen",
		"display.", ".second", "1display.second", "display.2nd",
		strings.Repeat("a", 65) + ".b", "a." + strings.Repeat("b", 33),
	}
	for _, kind := range kinds {
		err := insertFlag(kind)
		if model.IntegrityPolicyKind(kind).IsValid() {
			require.NoError(t, err, kind)
			continue
		}
		if len(kind) > model.IntegrityPolicyKindMaxLength {
			require.Error(t, err, kind)
			continue
		}
		requireCheckViolation(err, kind)
	}

	require.NoError(t, insertEvidence("display.second_screen", `{"displays":2}`))
	requireCheckViolation(insertEvidence("display.second_screen", nil), "capability evidence needs its detail")
	requireCheckViolation(insertEvidence("display.second_screen", `[2]`), "capability detail is an object")
	requireCheckViolation(insertEvidence("display.second_screen", `{"padding":"`+strings.Repeat("x", model.IntegrityEvidenceDetailMaxBytes)+`"}`),
		"capability detail is bounded")
	require.NoError(t, insertEvidence("connection_loss", nil))
	requireCheckViolation(insertEvidence("connection_loss", `{}`), "core evidence carries no detail")
}
