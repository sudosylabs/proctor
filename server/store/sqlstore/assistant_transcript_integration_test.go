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
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
)

func TestAssistantTranscriptStore(t *testing.T) {
	t.Parallel()
	persistence := openTestStore(t)
	for _, test := range []struct {
		Name string
		Wrap func(*testing.T, *SQLStore) store.Store
	}{
		{"SQL", func(_ *testing.T, s *SQLStore) store.Store { return s }}, {"Retry", newRetryConformanceStore}, {"Timer", newTimerConformanceStore},
	} {
		t.Run(test.Name, func(t *testing.T) {
			resetPristineTestStore(t, persistence)
			probe := storetest.AssistantTranscriptProbe{
				ExpireOwner: func(t *testing.T, id model.ExamAttemptID) {
					_, err := persistence.GetMaster().Exec(context.Background(), `UPDATE exam_attempt_assistant_runs SET
					lease_expires_at=statement_timestamp()-interval '1 second' WHERE id=?`, id.String())
					require.NoError(t, err)
				},
				VerifyBound: func(t *testing.T, id model.ExamAttemptID, expected model.AssistantInventory) {
					var value model.AssistantInventory
					err := persistence.GetMaster().Get(context.Background(), &value, `SELECT i.records,i.bytes,i.gaps,i.digest,i.sessions,i.session_inputs FROM
					exam_attempt_assistant_inventories i JOIN exam_submissions sub ON sub.id=i.submission_id WHERE i.id=? AND sub.exam_attempt_id=i.id AND
					sub.sealed=true`, id.String())
					require.NoError(t, err)
					require.Equal(t, expected, value)
					_, err = persistence.GetMaster().Exec(context.Background(), `UPDATE exam_attempt_assistant_turns SET canonical=canonical WHERE
					exam_attempt_id=?`, id.String())
					require.Error(t, err)
					_, err = persistence.GetMaster().Exec(context.Background(), `UPDATE exam_attempt_assistant_inventories SET digest=digest WHERE id=?`, id.String())
					require.Error(t, err)
				},
			}
			storetest.TestAssistantTranscriptStore(t, test.Wrap(t, persistence), probe)
			resetPristineTestStore(t, persistence)
			verifyBudget := func(t *testing.T, id model.ExamAttemptID, commands, milliseconds int64) {
				var row struct {
					Commands     int64 `db:"commands"`
					Milliseconds int64 `db:"command_milliseconds"`
				}
				err := persistence.GetMaster().Get(context.Background(), &row, `SELECT commands,command_milliseconds FROM exam_attempt_assistant_inventories WHERE id=?`, id.String())
				require.NoError(t, err)
				require.Equal(t, commands, row.Commands)
				require.Equal(t, milliseconds, row.Milliseconds)
			}
			storetest.TestAssistantCommandBudgetStore(t, test.Wrap(t, persistence), probe, verifyBudget)
			resetPristineTestStore(t, persistence)
			storetest.TestAssistantSessionStore(t, test.Wrap(t, persistence), verifyBudget)
		})
	}
}
