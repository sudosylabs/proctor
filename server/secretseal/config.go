// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package secretseal

import (
	"fmt"

	"github.com/sudosylabs/proctor/server/config"
)

// mfaMaximumPlaintextBytes bounds a TOTP secret, far below the mail limit.
const mfaMaximumPlaintextBytes = 128

// mailSettings reports false when no ring is configured, so mail runs
// without sealing. A configured ring is validated when the server is built.
func mailSettings(mailConfig config.Mail) (Settings, bool) {
	keys := mailConfig.SecretSealing
	if keys.EncryptionKey == "" && len(keys.DecryptionKeys) == 0 {
		return Settings{}, false
	}
	return Settings{
		EncryptionKey:    keys.EncryptionKey,
		DecryptionKeys:   append([]string(nil), keys.DecryptionKeys...),
		MaximumPlaintext: MaximumPlaintextBytes,
	}, true
}

// NewMailSealer seals mail secrets with the configured key ring, or is nil
// when none is configured.
func NewMailSealer(mailConfig config.Mail) (*Sealer, error) {
	settings, configured := mailSettings(mailConfig)
	if !configured {
		return nil, nil
	}
	sealer, err := New(settings)
	if err != nil {
		return nil, fmt.Errorf("configure mail secret sealing: %w", err)
	}
	return sealer, nil
}

// mfaSettings uses a key ring separate from mail's.
func mfaSettings(mfaConfig config.MFA) (Settings, bool) {
	if mfaConfig.EncryptionKey == "" && len(mfaConfig.DecryptionKeys) == 0 {
		return Settings{}, false
	}
	return Settings{
		EncryptionKey:    mfaConfig.EncryptionKey,
		DecryptionKeys:   append([]string(nil), mfaConfig.DecryptionKeys...),
		MaximumPlaintext: mfaMaximumPlaintextBytes,
	}, true
}

// NewMFASealer seals TOTP secrets with the MFA key ring, or is nil when none
// is configured.
func NewMFASealer(mfaConfig config.MFA) (*Sealer, error) {
	settings, configured := mfaSettings(mfaConfig)
	if !configured {
		return nil, nil
	}
	sealer, err := New(settings)
	if err != nil {
		return nil, fmt.Errorf("configure MFA secret sealing: %w", err)
	}
	return sealer, nil
}
