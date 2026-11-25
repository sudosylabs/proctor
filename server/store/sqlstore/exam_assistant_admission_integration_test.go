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
	"github.com/sudosylabs/proctor/server/store/storetest"
)

func TestAssistantAdmissionStore(t *testing.T) {
	t.Parallel()
	sqlStore := openTestStore(t)
	resetPristineTestStore(t, sqlStore)
	storetest.TestAssistantAdmissionStore(t, sqlStore, func(t *testing.T, id model.ExamAttemptID, expected model.AssistantPolicy) {
		var row struct {
			Policy       []byte `db:"policy"`
			Provider     string `db:"provider"`
			Model        string `db:"model"`
			DisplayName  string `db:"display_name"`
			Pseudonymize bool   `db:"pseudonymize"`
		}
		err := sqlStore.GetMaster().Get(context.Background(), &row, `SELECT policy,provider,model,display_name,pseudonymize FROM exam_attempt_assistants WHERE id=?`, id.String())
		require.NoError(t, err)
		frozen, err := model.DecodeAssistantPolicy(row.Policy)
		require.NoError(t, err)
		require.True(t, expected.Equal(frozen))
		require.Equal(t, "campus", row.Provider)
		require.Equal(t, "teaching-model", row.Model)
		require.Equal(t, "Campus tutor", row.DisplayName)
		require.True(t, row.Pseudonymize)
		_, err = sqlStore.GetMaster().Exec(context.Background(), `UPDATE exam_attempt_assistants SET model='changed' WHERE id=?`, id.String())
		require.Error(t, err)
	})
}
