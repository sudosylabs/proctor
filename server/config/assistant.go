// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Assistant is operator-owned; provider credentials never enter application storage.
type Assistant struct {
	Enabled   bool                `json:"Enabled"`
	Providers []AssistantProvider `json:"Providers"`
}

type AssistantCapabilities struct {
	ToolChoice    bool `json:"ToolChoice"`
	ParallelTools bool `json:"ParallelTools"`
	Streaming     bool `json:"Streaming"`
}

type AssistantProvider struct {
	Name             string                `json:"Name"`
	Kind             string                `json:"Kind"`
	BaseURL          string                `json:"BaseURL"`
	APIKey           string                `json:"APIKey"`
	Capabilities     AssistantCapabilities `json:"Capabilities"`
	Timeout          Duration              `json:"Timeout"`
	MaxResponseBytes int64                 `json:"MaxResponseBytes"`
}

func validateAssistant(assistant Assistant, add func(string, string)) {
	if len(assistant.Providers) > 64 {
		add("assistant.providers", "must list at most 64 providers")
	}
	if assistant.Enabled && len(assistant.Providers) == 0 {
		add("assistant.providers", "must list a provider when enabled")
	}
	seen := make(map[string]bool, len(assistant.Providers))
	for index, provider := range assistant.Providers {
		field := fmt.Sprintf("assistant.providers[%d]", index)
		if !executionHostNamePattern.MatchString(provider.Name) || seen[provider.Name] {
			add(field+".name", "must be a unique lowercase name of at most 63 bytes")
		}
		seen[provider.Name] = true
		if provider.Kind != "openai_compatible" && provider.Kind != "anthropic" {
			add(field+".kind", "must be openai_compatible or anthropic")
		}
		base, err := url.Parse(provider.BaseURL)
		if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") ||
			base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Opaque != "" {
			add(field+".base_url", "must be an absolute HTTP or HTTPS URL without credentials, query or fragment")
		}
		if strings.ContainsAny(provider.APIKey, "\r\n") || len(provider.APIKey) > 8192 {
			add(field+".api_key", "must be a bounded single-line credential")
		}
		if provider.Timeout.Duration < time.Second || provider.Timeout.Duration > 10*time.Minute {
			add(field+".timeout", "must be from one second to ten minutes")
		}
		if provider.MaxResponseBytes < 1024 || provider.MaxResponseBytes > 64<<20 {
			add(field+".max_response_bytes", "must be from 1024 bytes to 64 MiB")
		}
	}
}

func redactAssistant(assistant *Assistant) {
	for index := range assistant.Providers {
		assistant.Providers[index].APIKey = redactSecret(assistant.Providers[index].APIKey)
	}
}
