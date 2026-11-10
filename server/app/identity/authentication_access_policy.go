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

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// authenticationAccessPolicy is the consumer-owned view of current access
// policy needed by authentication entry points. Terminal persistence
// operations independently recheck the authoritative policy in their
// transaction.
type authenticationAccessPolicy interface {
	roleHolderMFARequirement
	sessionLengthPolicy
	AllowsLocalLogin(context.Context) (bool, error)
	ExternalProviderAdmission(context.Context, string) (externalProviderAdmissionPolicy, error)
	AvailableExternalProviders(context.Context, []model.ExternalAuthenticationProvider) ([]model.ExternalAuthenticationProvider, error)
}

// roleHolderMFARequirement answers whether the current Access Policy requires the
// User to use two-step verification because the User is a role holder: the User holds
// a current Role Binding at any scope. Every sign-in, refresh and Desktop
// handoff that would otherwise grant single-factor authority asks it.
type roleHolderMFARequirement interface {
	RequiresMFA(context.Context, model.UserID) (bool, error)
}

// sessionLengthPolicy applies the Access Policy Session lengths to the
// deployment lifetimes: a set length shortens its default, never lengthens it.
// Session creation, activity, refresh and the Desktop exchange ask it.
type sessionLengthPolicy interface {
	SessionLifetimes(context.Context, SessionPolicy) (sessionLifetimes, error)
}

// sessionLifetimes are the effective idle and absolute lengths a Session gets
// now.
type sessionLifetimes struct {
	Idle     time.Duration
	Absolute time.Duration
}

// effectiveSessionLifetimes shortens the deployment defaults to the policy's
// set lengths; the idle length never exceeds the absolute one.
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

// currentAuthenticationAccessPolicyDependencies are the collaborators of currentAuthenticationAccessPolicy;
// newCurrentAuthenticationAccessPolicy validates them once and currentAuthenticationAccessPolicy keeps them in deps.
type currentAuthenticationAccessPolicyDependencies struct {
	Policies store.AccessPolicyStore
	// MFAEnabled reports whether the deployment configuration enables
	// two-step verification. While it is false the role holder requirement
	// pauses instead of restricting everyone it covers.
	MFAEnabled  bool
	Diagnostics AuthenticationDiagnostics
	Now         func() time.Time
}

type currentAuthenticationAccessPolicy struct {
	deps currentAuthenticationAccessPolicyDependencies
	// pausedWarning reports the paused role holder requirement once per
	// process rather than on every sign-in.
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

func (p *currentAuthenticationAccessPolicy) ExternalProviderAdmission(ctx context.Context, providerID string) (externalProviderAdmissionPolicy, error) {
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

// RequiresMFA reads the authoritative policy and Role Bindings on every call;
// nothing is cached, so a newly granted role or a policy change applies to the
// next sign-in or refresh. While the deployment disables two-step
// verification the requirement pauses: nobody could satisfy it, and
// restricting every role holder would leave no administrator able to change
// the policy.
func (p *currentAuthenticationAccessPolicy) RequiresMFA(ctx context.Context, userID model.UserID) (bool, error) {
	if !p.deps.MFAEnabled {
		policy, err := p.current(ctx)
		if err != nil {
			return false, err
		}
		if policy.RoleHolderMFARequired {
			p.pausedWarning.Do(func() {
				p.deps.Diagnostics.WarnContext(ctx, "role holder two-step verification is paused",
					errors.New("the access policy requires two-step verification of role holders, but the deployment configuration disables MFA"))
			})
		}
		return false, nil
	}
	return p.deps.Policies.HasRoleHolderMFARequirement(ctx, userID, model.TimeUTC(p.deps.Now()))
}

// SessionLifetimes reads the current policy on every call. Activity reaches
// it only after the activity debounce, so the read stays bounded.
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
// whether it may provision new Users.
type AccessPolicyProviderCapability struct {
	Descriptor    model.ExternalAuthenticationProvider
	AutoProvision bool
}

// AccessPolicyCapabilitySnapshot is what this deployment can offer the Access
// Policy: its external providers, durable mail and two-step verification.
type AccessPolicyCapabilitySnapshot struct {
	Providers   []AccessPolicyProviderCapability
	DurableMail bool
	// MFA reports whether the server configuration enables two-step
	// verification, so role holders could enroll a second factor.
	MFA bool
}

// AccessPolicyCapabilitySource reports the deployment's current capabilities.
type AccessPolicyCapabilitySource interface {
	Snapshot() AccessPolicyCapabilitySnapshot
}

// AccessDeploymentCapabilities projects a capability snapshot into the form
// the Store rechecks inside the transaction that relies on it.
func AccessDeploymentCapabilities(snapshot AccessPolicyCapabilitySnapshot) store.AccessDeploymentCapabilities {
	providers := make(map[string]store.AccessProviderCapability, len(snapshot.Providers))
	for _, provider := range snapshot.Providers {
		if provider.Descriptor.ID != "" {
			providers[provider.Descriptor.ID] = store.AccessProviderCapability{AutoProvision: provider.AutoProvision}
		}
	}
	return store.AccessDeploymentCapabilities{Providers: providers, DurableMail: snapshot.DurableMail, MFA: snapshot.MFA}
}
