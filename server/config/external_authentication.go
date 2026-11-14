// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// maxProviderDocumentBytes bounds one provider's Settings or Secrets document.
const maxProviderDocumentBytes = 64 << 10

// ExternalAuthenticationProvider is one configured sign-in provider. Proctor
// owns its identity, whether it is on and whether it may create accounts.
// Type names the sign-in plugin that builds it, and everything about the
// protocol, its claim mapping included, is that plugin's own: Settings and
// Secrets are documents only the plugin decodes and validates.
type ExternalAuthenticationProvider struct {
	ID            string `json:"ID"`
	Type          string `json:"Type"`
	DisplayName   string `json:"DisplayName"`
	Enabled       bool   `json:"Enabled"`
	AutoProvision bool   `json:"AutoProvision"`
	// Settings is the protocol's settings document, such as an OIDC
	// issuer and claim mapping.
	Settings json.RawMessage `json:"Settings"`
	// Secrets is the protocol's document of secret values, such as an
	// OIDC client secret. It is always redacted when the configuration is
	// shown.
	Secrets json.RawMessage `json:"Secrets"`
}

// SettingsDocument returns Settings, or nil when it is absent or JSON null.
func (p ExternalAuthenticationProvider) SettingsDocument() json.RawMessage {
	return plugin.PresentDocument(p.Settings)
}

// SecretsDocument returns Secrets, or nil when it is absent or JSON null.
func (p ExternalAuthenticationProvider) SecretsDocument() json.RawMessage {
	return plugin.PresentDocument(p.Secrets)
}

type ExternalAuthentication struct {
	LoginStateTTL Duration                         `json:"LoginStateTTL"`
	Providers     []ExternalAuthenticationProvider `json:"Providers"`
}

// validateExternalAuthentication checks what Proctor owns of each provider
// and bounds its protocol documents. Whether Type names a sign-in plugin,
// and whether the documents suit it, is checked when the plugin builds the
// provider.
func validateExternalAuthentication(
	external ExternalAuthentication,
	add func(string, string),
) {
	if external.LoginStateTTL.Duration < time.Minute ||
		external.LoginStateTTL.Duration > 30*time.Minute {
		add(
			"authentication.external.login_state_ttl",
			"must be between 1m and 30m",
		)
	}

	if len(external.Providers) > model.IdentityProviderMaxCount {
		add(
			"authentication.external.providers",
			fmt.Sprintf("must contain at most %d providers", model.IdentityProviderMaxCount),
		)
	}

	seen := make(map[string]struct{}, len(external.Providers))
	for index, provider := range external.Providers {
		prefix := fmt.Sprintf("authentication.external.providers[%d]", index)
		if !model.IsValidIdentityProviderID(provider.ID) {
			add(prefix+".id", "must contain 1-64 lowercase letters, numbers, dots, underscores, or hyphens")
		} else if _, exists := seen[provider.ID]; exists {
			add(prefix+".id", "must be unique")
		} else {
			seen[provider.ID] = struct{}{}
		}

		if len(provider.DisplayName) == 0 ||
			len(provider.DisplayName) > 128 ||
			strings.ContainsAny(provider.DisplayName, "\x00\r\n") {
			add(prefix+".display_name", "must contain between 1 and 128 safe characters")
		}

		if !plugin.IsValidID(provider.Type) {
			add(prefix+".type", "must name a sign-in plugin")
		}

		validateProviderDocument(prefix+".settings", provider.SettingsDocument(), add)
		validateProviderDocument(prefix+".secrets", provider.SecretsDocument(), add)
	}
}

func validateProviderDocument(field string, document json.RawMessage, add func(string, string)) {
	if document == nil {
		return
	}
	if len(document) > maxProviderDocumentBytes {
		add(field, fmt.Sprintf("must be at most %d bytes", maxProviderDocumentBytes))
	} else if !json.Valid(document) || !bytes.HasPrefix(bytes.TrimSpace(document), []byte("{")) {
		add(field, "must be a JSON object")
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
