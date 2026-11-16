// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/sudosylabs/proctor/server/store"
)

// ResolveBootstrapProtection decides the proof the one-time bootstrap
// command requires. The operator's configured secret wins. Otherwise an
// installation that has bootstrapped needs none, a development installation
// gets a fresh secret written to output, and any other uninitialized
// installation refuses to start, because it would be claimable by whoever
// reaches it first.
func ResolveBootstrapProtection(
	ctx context.Context,
	secret string,
	developmentMode bool,
	installations store.InstallationStore,
	output io.Writer,
) (BootstrapProtectionPolicy, error) {
	if secret != "" || installations == nil {
		// Without installation state there is nothing to protect beyond the
		// operator's configured secret.
		return BootstrapProtectionPolicy{Secret: secret}, nil
	}

	state, err := installations.Get(ctx)
	if err == nil {
		if validateErr := state.Validate(); validateErr != nil {
			return BootstrapProtectionPolicy{}, fmt.Errorf("validate installation bootstrap state: %w", validateErr)
		}
		return BootstrapProtectionPolicy{}, nil
	}

	if !store.IsNotFound(err) {
		return BootstrapProtectionPolicy{}, fmt.Errorf("determine installation bootstrap state: %w", err)
	}

	if developmentMode {
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return BootstrapProtectionPolicy{}, fmt.Errorf("generate development bootstrap secret: %w", err)
		}

		secret := base64.RawURLEncoding.EncodeToString(secretBytes)
		if output == nil {
			return BootstrapProtectionPolicy{}, errors.New("development bootstrap secret output is unavailable")
		}
		if _, err := fmt.Fprintf(output, "Proctor development bootstrap secret: %s\n", secret); err != nil {
			return BootstrapProtectionPolicy{}, fmt.Errorf("display development bootstrap secret: %w", err)
		}

		return BootstrapProtectionPolicy{Secret: secret}, nil
	}

	return BootstrapProtectionPolicy{}, errors.New(
		"bootstrap secret is required for an uninitialized network-accessible installation",
	)
}
