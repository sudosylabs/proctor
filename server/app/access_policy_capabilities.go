// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"sort"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
)

// deploymentAccessPolicyCapabilities projects only safe process capabilities
// from the live registry and mail transport. Secrets never enter policy code.
type deploymentAccessPolicyCapabilities struct {
	providers  identity.ExternalProviderSource
	mail       mail.Sender
	health     interface{ Code() string }
	mfaEnabled bool
}

func (c deploymentAccessPolicyCapabilities) Snapshot() identity.AccessPolicyCapabilitySnapshot {
	durableMail := c.mail != nil && c.mail.Enabled() && c.health != nil && c.health.Code() == MailHealthHealthy
	result := identity.AccessPolicyCapabilitySnapshot{DurableMail: durableMail, MFA: c.mfaEnabled}
	if c.providers == nil {
		return result
	}
	for _, descriptor := range c.providers.Descriptors() {
		provider, available := c.providers.Provider(descriptor.ID)
		if !available || provider == nil {
			continue
		}
		result.Providers = append(result.Providers, identity.AccessPolicyProviderCapability{
			Descriptor: descriptor, AutoProvision: provider.AutoProvision(),
		})
	}
	sort.Slice(result.Providers, func(i, j int) bool {
		return result.Providers[i].Descriptor.ID < result.Providers[j].Descriptor.ID
	})
	return result
}
