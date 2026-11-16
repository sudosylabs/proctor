// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"sort"

	"github.com/sudosylabs/proctor/server/app/mail"
)

// DeploymentCapabilities exposes what the live provider registry and mail
// transport can do, never their secrets, so policy code can read it safely.
type DeploymentCapabilities struct {
	Providers ExternalProviderSource
	Mail      mail.Sender
	// Health is a *mail.Health in production.
	Health     interface{ Code() string }
	MFAEnabled bool
}

// Snapshot reports durable mail only while the sender is enabled and healthy,
// and each configured provider that is currently available.
func (c DeploymentCapabilities) Snapshot() AccessPolicyCapabilitySnapshot {
	durableMail := c.Mail != nil && c.Mail.Enabled() && c.Health != nil && c.Health.Code() == mail.HealthHealthy
	result := AccessPolicyCapabilitySnapshot{DurableMail: durableMail, MFA: c.MFAEnabled}
	if c.Providers == nil {
		return result
	}

	for _, descriptor := range c.Providers.Descriptors() {
		provider, available := c.Providers.Provider(descriptor.ID)
		if !available || provider == nil {
			continue
		}

		result.Providers = append(result.Providers, AccessPolicyProviderCapability{
			Descriptor:    descriptor,
			AutoProvision: provider.AutoProvision(),
		})
	}

	sort.Slice(result.Providers, func(i, j int) bool {
		return result.Providers[i].Descriptor.ID < result.Providers[j].Descriptor.ID
	})
	return result
}
