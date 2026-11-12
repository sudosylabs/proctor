// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/plugin"
)

// DecodeDefinition reads the configured provider a Registry hands a
// plugin.IdentityProviderType. Decoding is strict, and the decoded ID must
// match the definition's.
func DecodeDefinition(definition plugin.IdentityProviderDefinition) (config.ExternalAuthenticationProvider, error) {
	var settings config.ExternalAuthenticationProvider
	decoder := json.NewDecoder(bytes.NewReader(definition.Settings))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return config.ExternalAuthenticationProvider{}, fmt.Errorf("decode provider settings: %w", err)
	}
	if decoder.More() {
		return config.ExternalAuthenticationProvider{}, errors.New("decode provider settings: trailing data")
	}
	if settings.ID != definition.ID {
		return config.ExternalAuthenticationProvider{}, errors.New("provider settings name a different provider")
	}
	return settings, nil
}
