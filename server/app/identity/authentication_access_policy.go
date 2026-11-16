// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type sessionLifetimes struct {
	Idle     time.Duration
	Absolute time.Duration
}

// effectiveSessionLifetimes lets the policy shorten, never lengthen, the
// deployment defaults; idle never exceeds absolute.
func effectiveSessionLifetimes(settings model.AccessPolicySettings, defaults SessionPolicy) sessionLifetimes {
	result := sessionLifetimes{Idle: defaults.IdleTTL, Absolute: defaults.AbsoluteTTL}
	if minutes := time.Duration(settings.SessionAbsoluteMinutes) * time.Minute; minutes > 0 && minutes < result.Absolute {
		result.Absolute = minutes
	}
	if minutes := time.Duration(settings.SessionIdleMinutes) * time.Minute; minutes > 0 && minutes < result.Idle {
		result.Idle = minutes
	}
	result.Idle = min(result.Idle, result.Absolute)
	return result
}

type externalProviderAdmissionPolicy struct {
	Mode                       model.ProviderAdmissionMode
	InvitationAdmissionEnabled bool
}

type currentAuthenticationAccessPolicyDependencies struct {
	Policies store.AccessPolicyStore
	// While MFAEnabled is false the role holder requirement pauses instead of
	// restricting everyone it covers.
	MFAEnabled  bool
	Diagnostics *logging.Logger
	Now         func() time.Time
}

// currentAuthenticationAccessPolicy answers the sign-in entry points from the
// current access policy. Its answers only gate entry: each terminal store
// operation rechecks the policy in its own transaction.
type currentAuthenticationAccessPolicy struct {
	deps currentAuthenticationAccessPolicyDependencies
	// pausedWarning logs the paused requirement once per process, not per
	// sign-in.
	pausedWarning sync.Once
}

func newCurrentAuthenticationAccessPolicy(deps currentAuthenticationAccessPolicyDependencies) (*currentAuthenticationAccessPolicy, error) {
	if deps.Policies == nil {
		return nil, errors.New("authentication access policy store is required")
	}
	if deps.Now == nil {
		return nil, errors.New("authentication access policy clock is required")
	}
	if deps.Diagnostics == nil {
		return nil, errors.New("authentication access policy diagnostics are required")
	}
	return &currentAuthenticationAccessPolicy{deps: deps}, nil
}

func (p *currentAuthenticationAccessPolicy) AllowsLocalLogin(ctx context.Context) (bool, error) {
	policy, err := p.current(ctx)
	if err != nil {
		return false, err
	}
	return policy.LocalLoginEnabled, nil
}

func (p *currentAuthenticationAccessPolicy) ExternalProviderAdmission(
	ctx context.Context,
	providerID string,
) (externalProviderAdmissionPolicy, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return externalProviderAdmissionPolicy{}, nil
	}

	policy, err := p.current(ctx)
	if err != nil {
		return externalProviderAdmissionPolicy{}, err
	}

	return externalProviderAdmissionPolicy{
		Mode:                       policy.ProviderAdmissions[providerID],
		InvitationAdmissionEnabled: policy.InvitationAdmissionEnabled,
	}, nil
}

func (p *currentAuthenticationAccessPolicy) AllowsDesktopAuthorization(ctx context.Context, method, providerID string) (bool, error) {
	policy, err := p.current(ctx)
	if err != nil || !policy.DesktopAuthorizationEnabled {
		return false, err
	}
	if method == "password" {
		return providerID == "" && policy.LocalLoginEnabled, nil
	}
	if providerID == "" {
		return false, nil
	}
	_, allowed := policy.ProviderAdmissions[strings.ToLower(strings.TrimSpace(providerID))]
	return allowed, nil
}

func (p *currentAuthenticationAccessPolicy) AvailableExternalProviders(
	ctx context.Context,
	configured []model.ExternalAuthenticationProvider,
) ([]model.ExternalAuthenticationProvider, error) {
	policy, err := p.current(ctx)
	if err != nil {
		return nil, err
	}
	available := make([]model.ExternalAuthenticationProvider, 0, len(configured))
	for _, provider := range configured {
		if _, allowed := policy.ProviderAdmissions[provider.ID]; allowed {
			available = append(available, provider)
		}
	}
	return available, nil
}

// RequiresMFA reports whether the user holds a role binding that the policy
// says needs two-step verification. Nothing is cached, so a new role or policy
// change applies to the next sign-in or refresh. While the deployment disables
// MFA the requirement pauses: nobody could satisfy it, and enforcing it would
// leave no administrator able to change the policy.
func (p *currentAuthenticationAccessPolicy) RequiresMFA(ctx context.Context, userID model.UserID) (bool, error) {
	if !p.deps.MFAEnabled {
		policy, err := p.current(ctx)
		if err != nil {
			return false, err
		}

		if policy.RoleHolderMFARequired {
			p.pausedWarning.Do(func() {
				p.deps.Diagnostics.WarnContext(
					ctx,
					"role holder two-step verification is paused",
					logging.Err(
						errors.New("the access policy requires two-step verification of role holders, but the deployment configuration disables MFA"),
					),
				)
			})
		}

		return false, nil
	}

	return p.deps.Policies.HasRoleHolderMFARequirement(ctx, userID, model.TimeUTC(p.deps.Now()))
}

// SessionLifetimes reads the policy on every call; activity updates reach it
// only after the debounce, which keeps that read bounded.
func (p *currentAuthenticationAccessPolicy) SessionLifetimes(ctx context.Context, defaults SessionPolicy) (sessionLifetimes, error) {
	policy, err := p.current(ctx)
	if err != nil {
		return sessionLifetimes{}, err
	}
	return effectiveSessionLifetimes(policy.Settings(), defaults), nil
}

func (p *currentAuthenticationAccessPolicy) current(ctx context.Context) (*model.AccessPolicy, error) {
	snapshot, err := p.deps.Policies.Get(ctx, 0)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.Policy == nil || snapshot.Policy.Validate() != nil {
		return nil, errors.New("current authentication access policy is invalid")
	}
	return snapshot.Policy, nil
}

// AccessPolicyProviderCapability is one configured external provider and
// whether it may provision new users.
type AccessPolicyProviderCapability struct {
	Descriptor    model.ExternalAuthenticationProvider
	AutoProvision bool
}

// AccessPolicyCapabilitySnapshot is what this deployment can offer the access
// policy.
type AccessPolicyCapabilitySnapshot struct {
	Providers   []AccessPolicyProviderCapability
	DurableMail bool
	MFA         bool
}

// AccessDeploymentCapabilities converts a snapshot into the form the store
// rechecks inside the transaction that relies on it.
func AccessDeploymentCapabilities(snapshot AccessPolicyCapabilitySnapshot) store.AccessDeploymentCapabilities {
	providers := make(map[string]store.AccessProviderCapability, len(snapshot.Providers))
	for _, provider := range snapshot.Providers {
		if provider.Descriptor.ID != "" {
			providers[provider.Descriptor.ID] = store.AccessProviderCapability{AutoProvision: provider.AutoProvision}
		}
	}
	return store.AccessDeploymentCapabilities{Providers: providers, DurableMail: snapshot.DurableMail, MFA: snapshot.MFA}
}
