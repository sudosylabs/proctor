// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
)

func TestMFASecretSealingAuthenticatesUserAndKeyReference(t *testing.T) {
	t.Parallel()

	mechanics := mustTestMFAMechanics(t)
	userID := model.NewUserID()
	sealed, err := mechanics.sealTOTPSecret(userID.String(), "JBSWY3DPEHPK3PXP")
	require.NoError(t, err)
	var envelope secretseal.Envelope
	require.NoError(t, json.Unmarshal([]byte(sealed.encoded), &envelope))
	require.Equal(t, secretseal.EnvelopeVersion1, envelope.Version)
	require.Equal(t, secretseal.AlgorithmAES256GCM, envelope.Algorithm)
	require.Equal(t, sealed.keyID, envelope.KeyID)
	credential := &model.MFACredential{
		EncryptedSecret: sealed.encoded, EncryptionKeyID: sealed.keyID,
	}
	opened, err := mechanics.decrypt(userID.String(), credential)
	require.NoError(t, err, "decrypt()")
	require.Equal(t, "JBSWY3DPEHPK3PXP", opened, "decrypt()")
	_, err = mechanics.decrypt(model.NewUserID().String(), credential)
	require.Error(t, err, "MFA envelope opened for a different user")
	mismatched := *credential
	mismatched.EncryptionKeyID = strings.Repeat("0", model.MFAEncryptionKeyIDLength)
	_, err = mechanics.decrypt(userID.String(), &mismatched)
	require.Error(t, err, "MFA envelope opened with mismatched durable key reference")
}

func TestMFAMechanicsRequiresSealingOnlyWhenEnabled(t *testing.T) {
	t.Parallel()

	_, err := newMFAMechanics(mfaMechanicsDependencies{Settings: MFAPolicy{}, Sealer: nil})
	require.NoError(t, err)
	_, err = newMFAMechanics(mfaMechanicsDependencies{Settings: MFAPolicy{Enabled: true}, Sealer: nil})
	require.Error(t, err, "newMFAMechanics(enabled without sealer) succeeded")
}
