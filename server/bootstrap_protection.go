// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type bootstrapStatusStore interface {
	Get(context.Context) (*model.InstallationState, error)
}

func resolveBootstrapProtection(
	ctx context.Context,
	cfg config.Config,
	installations bootstrapStatusStore,
	output io.Writer,
) (identity.BootstrapProtectionPolicy, error) {
	if cfg.Authentication.Bootstrap.Secret != "" || installations == nil {
		// Without installation state there is nothing to protect beyond the
		// operator's configured secret.
		return identity.BootstrapProtectionPolicy{Secret: cfg.Authentication.Bootstrap.Secret}, nil
	}

	state, err := installations.Get(ctx)
	if err == nil {
		if validateErr := state.Validate(); validateErr != nil {
			return identity.BootstrapProtectionPolicy{}, fmt.Errorf("validate installation bootstrap state: %w", validateErr)
		}
		return identity.BootstrapProtectionPolicy{}, nil
	}
	if !store.IsNotFound(err) {
		return identity.BootstrapProtectionPolicy{}, fmt.Errorf("determine installation bootstrap state: %w", err)
	}

	if cfg.Authentication.Bootstrap.DevelopmentMode {
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return identity.BootstrapProtectionPolicy{}, fmt.Errorf("generate development bootstrap secret: %w", err)
		}
		secret := base64.RawURLEncoding.EncodeToString(secretBytes)
		if output == nil {
			return identity.BootstrapProtectionPolicy{}, errors.New("development bootstrap secret output is unavailable")
		}
		if _, err := fmt.Fprintf(output, "Proctor development bootstrap secret: %s\n", secret); err != nil {
			return identity.BootstrapProtectionPolicy{}, fmt.Errorf("display development bootstrap secret: %w", err)
		}
		return identity.BootstrapProtectionPolicy{Secret: secret}, nil
	}
	return identity.BootstrapProtectionPolicy{}, errors.New("bootstrap secret is required for an uninitialized network-accessible installation")
}
