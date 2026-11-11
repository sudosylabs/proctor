// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package operator

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/config"
)

// ValidateConfig loads and strictly validates the deployment configuration at
// configPath, or the required configured/default file when configPath is
// empty, without constructing any infrastructure.
func ValidateConfig(ctx context.Context, configPath string) error {
	configuration, err := config.Open(ctx, configPath)
	if err != nil {
		return err
	}
	return configuration.Close()
}

// EffectiveConfiguration is the redacted display of the configuration a node
// would run with: built-in defaults, the required file, then the PROCTOR_
// environment overlay.
type EffectiveConfiguration struct {
	// Document is the indented PascalCase JSON document with every secret
	// replaced by "[redacted]". An empty secret stays empty.
	Document []byte
	// EnvironmentOverrides names, without values, the environment variables
	// that replaced a file value.
	EnvironmentOverrides []string
}

// ShowEffectiveConfiguration loads and strictly validates the configuration
// like ValidateConfig and returns its redacted effective form. It constructs
// no infrastructure and never writes the file.
func ShowEffectiveConfiguration(ctx context.Context, configPath string) (_ EffectiveConfiguration, resultErr error) {
	configuration, err := config.Open(ctx, configPath)
	if err != nil {
		return EffectiveConfiguration{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, configuration.Close()) }()
	document, err := configuration.Get().RedactedJSON()
	if err != nil {
		return EffectiveConfiguration{}, err
	}
	return EffectiveConfiguration{
		Document:             document,
		EnvironmentOverrides: configuration.EnvironmentOverrides(),
	}, nil
}
