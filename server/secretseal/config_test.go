// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package secretseal

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
)

func TestMailSecretSealingProjectionIsOptionalAndCopiesTheKeyRing(t *testing.T) {
	t.Parallel()

	settings, configured := mailSettings(config.Default().Mail)
	require.False(t, configured, "disabled default projection = %#v", settings)
	require.Empty(t, settings.EncryptionKey, "disabled default projection = %#v", settings)
	require.Empty(t, settings.DecryptionKeys, "disabled default projection = %#v", settings)
	require.Equal(t, 0, settings.MaximumPlaintext, "disabled default projection = %#v", settings)
	sealer, err := NewMailSealer(config.Default().Mail)
	require.NoError(t, err, "NewMailSealer(disabled default)")
	require.Nil(t, sealer, "NewMailSealer(disabled default)")

	mailConfig := config.Default().Mail
	mailConfig.SecretSealing.EncryptionKey = base64.StdEncoding.EncodeToString(
		[]byte(strings.Repeat("p", 32)),
	)
	mailConfig.SecretSealing.DecryptionKeys = []string{base64.StdEncoding.EncodeToString(
		[]byte(strings.Repeat("f", 32)),
	)}
	settings, configured = mailSettings(mailConfig)
	require.True(t, configured, "configured projection = %#v", settings)
	require.Equal(t, MaximumPlaintextBytes, settings.MaximumPlaintext, "configured projection = %#v", settings)
	mailConfig.SecretSealing.DecryptionKeys[0] = "mutated"
	require.NotEqual(t, "mutated", settings.DecryptionKeys[0], "mail key-ring projection retained the configuration slice")
	mailConfig.SecretSealing.DecryptionKeys[0] = settings.DecryptionKeys[0]
	sealer, err = NewMailSealer(mailConfig)
	require.NoError(t, err)
	binding := Binding{Purpose: "mail.delivery", Owner: "delivery-projection"}
	envelope, err := sealer.Seal(binding, []byte("projected secret"))
	require.NoError(t, err)
	opened, err := sealer.Open(binding, envelope)
	require.NoError(t, err, "Open(projected envelope)")
	require.Equal(t, "projected secret", string(opened), "Open(projected envelope)")
	mailConfig.SecretSealing.EncryptionKey = "credential-that-must-not-leak"
	_, err = NewMailSealer(mailConfig)
	require.Error(t, err)
	require.NotContains(t, err.Error(), mailConfig.SecretSealing.EncryptionKey)
}

func TestMFASecretSealingProjectionIsIndependentAndBounded(t *testing.T) {
	t.Parallel()

	settings, configured := mfaSettings(config.Default().Authentication.MFA)
	require.False(t, configured, "disabled default projection = %#v", settings)
	require.Empty(t, settings.EncryptionKey, "disabled default projection = %#v", settings)
	require.Empty(t, settings.DecryptionKeys, "disabled default projection = %#v", settings)
	require.Equal(t, 0, settings.MaximumPlaintext, "disabled default projection = %#v", settings)
	sealer, err := NewMFASealer(config.Default().Authentication.MFA)
	require.NoError(t, err, "NewMFASealer(disabled default)")
	require.Nil(t, sealer, "NewMFASealer(disabled default)")

	mfaConfig := config.Default().Authentication.MFA
	mfaConfig.EncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	mfaConfig.DecryptionKeys = []string{base64.StdEncoding.EncodeToString([]byte(strings.Repeat("f", 32)))}
	settings, configured = mfaSettings(mfaConfig)
	require.True(t, configured, "configured projection = %#v", settings)
	require.Equal(t, mfaMaximumPlaintextBytes, settings.MaximumPlaintext, "configured projection = %#v", settings)
	mfaConfig.DecryptionKeys[0] = "mutated"
	require.NotEqual(t, "mutated", settings.DecryptionKeys[0], "MFA key-ring projection retained the configuration slice")
	mfaConfig.DecryptionKeys[0] = settings.DecryptionKeys[0]
	sealer, err = NewMFASealer(mfaConfig)
	require.NoError(t, err)
	binding := Binding{Purpose: "mfa.totp", Owner: "user-projection"}
	envelope, err := sealer.Seal(binding, []byte("TOTPSECRET"))
	require.NoError(t, err)
	opened, err := sealer.Open(binding, envelope)
	require.NoError(t, err, "Open(projected MFA envelope)")
	require.Equal(t, "TOTPSECRET", string(opened), "Open(projected MFA envelope)")
}
