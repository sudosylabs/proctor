// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

//go:build integration

package sqlstore

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store/storetest"
)

func TestExternalSecondFactorStateStore(t *testing.T) {
	persistence := openTestStore(t)
	resetPristineTestStore(t, persistence)
	storetest.TestExternalSecondFactorStateStore(t, persistence, storetest.ExternalSecondFactorSQLProbe{
		Activate: func(t *testing.T, id model.MFACredentialID) {
			_, err := persistence.GetMaster().Exec(t.Context(), `UPDATE mfa_credentials
                SET state = 'active', pending_expires_at = NULL, activated_at = clock_timestamp(), last_used_time_step = 1 WHERE id = ?`, id.String())
			require.NoError(t, err)
		},
		AddRecoveryCode: func(t *testing.T, userID model.UserID, hash string) {
			_, err := persistence.GetMaster().Exec(t.Context(), `INSERT INTO mfa_recovery_codes
                (id, created_at, updated_at, user_id, code_hash) VALUES (?, now(), now(), ?, ?)`,
				model.NewMFARecoveryCodeID().String(), userID.String(), hash)
			require.NoError(t, err)
		},
		SetGeneration: func(t *testing.T, userID model.UserID) {
			_, err := persistence.GetMaster().Exec(t.Context(), `INSERT INTO user_mfa_recovery
                (user_id, generation, reset_at, reenrollment_required, updated_at) VALUES (?, 1, now(), false, now())`, userID.String())
			require.NoError(t, err)
		},
		Disable: func(t *testing.T, userID model.UserID) {
			_, err := persistence.GetMaster().Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = ?`, userID.String())
			require.NoError(t, err)
		},
		Backdate: func(t *testing.T, id model.ExternalSecondFactorStateID) {
			_, err := persistence.GetMaster().Exec(t.Context(), `UPDATE external_second_factor_states SET
               created_at = now() - interval '2 minutes', expires_at = now() - interval '1 minute' WHERE id = ?`, id.String())
			require.NoError(t, err)
		},
		CheckWebSessionProof: func(t *testing.T, pause *model.ExternalSecondFactorState) {
			session := &model.Session{UserID: pause.UserID, ClientType: model.SessionClientWeb,
				AuthenticationGeneration: pause.AuthenticationGeneration, AuthenticationMethod: pause.AuthenticationMethod,
				AuthenticationProviderID: pause.Provider}
			require.Error(t, requireSessionIssuanceRecovery(t.Context(), persistence.GetMaster(), session, ""))
			require.NoError(t, requireSessionIssuanceRecovery(t.Context(), persistence.GetMaster(), session, pause.ExternalLoginStateID))
		},
	})
}
