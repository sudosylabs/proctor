// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAdministratorMFAResetOnlyAvailableOnInertServer(t *testing.T) {
	t.Parallel()
	command := AdministratorMFAResetCommand{InstitutionID: "institution", UserID: "user"}
	for _, state := range []nodeState{nodeInert, nodeStarting, nodeRunning, nodeStopping, nodeClosed} {
		fake := &administratorRecoveryRuntimeFake{mfaResult: &identity.AdministratorMFAResetResult{RecoveryGeneration: 3, ReenrollmentRequired: true}}
		node := &Server{state: state, components: runtimeComponents{administratorRecovery: fake}}
		result, err := node.ResetAdministratorMFA(context.Background(), command)
		if state == nodeInert {
			require.NoError(t, err, "inert result=%#v error=%v forwarded=%q/%q", result, err, fake.mfaInstitutionID, fake.mfaUserID)
			require.NotNil(t, result, "inert result=%#v error=%v forwarded=%q/%q", result, err, fake.mfaInstitutionID, fake.mfaUserID)
			require.True(t, result.ReenrollmentRequired, "inert result=%#v error=%v forwarded=%q/%q", result, err, fake.mfaInstitutionID, fake.mfaUserID)
			require.Equal(t, int64(3), result.RecoveryGeneration, "inert result=%#v error=%v forwarded=%q/%q", result, err, fake.mfaInstitutionID, fake.mfaUserID)
			require.Equal(t, model.InstitutionID(command.InstitutionID), fake.mfaInstitutionID, "inert result=%#v error=%v forwarded=%q/%q", result, err, fake.mfaInstitutionID, fake.mfaUserID)
			require.Equal(t, model.UserID(command.UserID), fake.mfaUserID, "inert result=%#v error=%v forwarded=%q/%q", result, err, fake.mfaInstitutionID, fake.mfaUserID)
		} else {
			require.Error(t, err, "state=%v accepted offline reset", state)
			require.Nil(t, result, "state=%v accepted offline reset", state)
			require.Empty(t, fake.mfaUserID, "state=%v accepted offline reset", state)
		}
	}
}
