// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/identityprovider"
)

// ProviderAdmissionMode is the closed policy vocabulary for one configured
// external provider. An absent provider entry is disabled.
type ProviderAdmissionMode string

const (
	ProviderAdmissionLinkedOnly         ProviderAdmissionMode = "linked_only"
	ProviderAdmissionInvitationRequired ProviderAdmissionMode = "invitation_required"
	ProviderAdmissionAutoProvision      ProviderAdmissionMode = "auto_provision"
)

const (
	AccessPolicyProviderMaxCount       = identityprovider.MaximumCount
	AccessPolicyTransitionHistoryLimit = 100
	// AccessPolicyTransitionFieldMaxCount bounds the changed field names one
	// transition records: every replaceable setting and revoke_existing_sessions.
	AccessPolicyTransitionFieldMaxCount = 10
	// AccessPolicySessionMinutesMaximum bounds a Session length the policy
	// can store, a little over a year; deployment configuration bounds it
	// further.
	AccessPolicySessionMinutesMaximum = 527040
)

var (
	ErrAccessPolicyRevisionConflict  = errors.New("access policy revision conflict")
	ErrAccessPolicyInvalidTransition = errors.New("access policy transition is invalid")
)

// AccessPolicy is the singleton institution application policy selecting the
// authentication and account-admission capabilities currently available.
// Deployment protocol material and secrets never belong here.
type AccessPolicy struct {
	ID                               AccessPolicyID
	Revision                         int64
	CreatedAt                        time.Time
	UpdatedAt                        time.Time
	LocalLoginEnabled                bool
	PublicRegistrationEnabled        bool
	InvitationAdmissionEnabled       bool
	InvitationLocalCredentialEnabled bool
	DesktopAuthorizationEnabled      bool
	ProviderAdmissions               map[string]ProviderAdmissionMode
	// RoleHolderMFARequired requires two-step verification of every role holder:
	// a User holding a current Role Binding at any scope, system
	// administrators included.
	RoleHolderMFARequired bool
	// SessionIdleMinutes and SessionAbsoluteMinutes shorten the idle and
	// absolute Session lifetimes; zero keeps the deployment default, which is
	// also the ceiling.
	SessionIdleMinutes     int
	SessionAbsoluteMinutes int
}

// AccessPolicySettings is the complete replaceable portion of AccessPolicy.
// A complete value keeps policy updates explicit as new capabilities are added.
type AccessPolicySettings struct {
	LocalLoginEnabled                bool
	PublicRegistrationEnabled        bool
	InvitationAdmissionEnabled       bool
	InvitationLocalCredentialEnabled bool
	DesktopAuthorizationEnabled      bool
	ProviderAdmissions               map[string]ProviderAdmissionMode
	RoleHolderMFARequired            bool
	SessionIdleMinutes               int
	SessionAbsoluteMinutes           int
}

// SessionLengthLimits are the deployment bounds of the Access Policy Session
// lengths in whole minutes. A default is the length a Session gets when the
// policy leaves it unset; the policy may only shorten it, down to the minimum.
type SessionLengthLimits struct {
	IdleMinimumMinutes     int
	IdleDefaultMinutes     int
	IdleMaximumMinutes     int
	AbsoluteMinimumMinutes int
	AbsoluteDefaultMinutes int
	AbsoluteMaximumMinutes int
}

// Validate reports limits that cannot describe a deployment: every bound is
// positive, a maximum never exceeds its default or the storable cap, and the
// idle default never exceeds the absolute default.
func (l SessionLengthLimits) Validate() error {
	if l.IdleMinimumMinutes < 1 ||
		l.AbsoluteMinimumMinutes < 1 ||
		l.IdleDefaultMinutes < 1 ||
		l.AbsoluteDefaultMinutes < l.IdleDefaultMinutes ||
		l.IdleMaximumMinutes < 1 ||
		l.AbsoluteMaximumMinutes < 1 ||
		l.IdleMaximumMinutes > l.IdleDefaultMinutes ||
		l.AbsoluteMaximumMinutes > l.AbsoluteDefaultMinutes ||
		l.AbsoluteMaximumMinutes > AccessPolicySessionMinutesMaximum {
		return invalidModelError("SessionLengthLimits.Validate", "session_length_limits", "", "are inconsistent", "")
	}

	return nil
}

type AccessPolicyTransitionOutcome string

const AccessPolicyTransitionApplied AccessPolicyTransitionOutcome = "applied"

// AccessPolicyTransition is the bounded, secret-free history fact retained for
// a successful policy replacement. Failed attempts remain in durable audit.
type AccessPolicyTransition struct {
	PolicyID      AccessPolicyID
	FromRevision  int64
	ToRevision    int64
	ActorID       UserID
	ChangedFields []string
	ChangedAt     time.Time
	Outcome       AccessPolicyTransitionOutcome
}

// NewInitialAccessPolicy returns the conservative policy established by the
// one-time installation bootstrap.
func NewInitialAccessPolicy(id AccessPolicyID, at time.Time) *AccessPolicy {
	at = TimeUTC(at)
	return &AccessPolicy{
		ID:                               id,
		Revision:                         1,
		CreatedAt:                        at,
		UpdatedAt:                        at,
		LocalLoginEnabled:                true,
		InvitationAdmissionEnabled:       true,
		InvitationLocalCredentialEnabled: true,
		DesktopAuthorizationEnabled:      true,
		ProviderAdmissions:               map[string]ProviderAdmissionMode{},
	}
}

func (p *AccessPolicy) Validate() error {
	const where = "AccessPolicy.Validate"
	if p == nil {
		return invalidModelError(where, "access_policy", "value", "is required", "")
	}
	if !p.ID.IsValid() {
		return invalidModelError(where, "access_policy", "id", "must be a valid identifier", "")
	}

	details := "id=" + p.ID.String()
	if p.Revision < 1 {
		return invalidModelError(where, "access_policy", "revision", "must be positive", details)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return invalidModelError(where, "access_policy", "timestamps", "must be ordered and set", details)
	}
	if len(p.ProviderAdmissions) > AccessPolicyProviderMaxCount {
		return invalidModelError(where, "access_policy", "provider_admissions", "contains too many providers", details)
	}

	for providerID, mode := range p.ProviderAdmissions {
		if !IsValidIdentityProviderID(providerID) {
			return invalidModelError(where, "access_policy", "provider_id", "is invalid", details)
		}

		switch mode {
		case ProviderAdmissionLinkedOnly, ProviderAdmissionInvitationRequired, ProviderAdmissionAutoProvision:
		default:
			return invalidModelError(where, "access_policy", "provider_admission", fmt.Sprintf("%q is invalid", mode), details)
		}
	}

	if err := p.Settings().Validate(); err != nil {
		return invalidModelError(where, "access_policy", "settings", err.Error(), details)
	}

	return nil
}

func (s AccessPolicySettings) Validate() error {
	if len(s.ProviderAdmissions) > AccessPolicyProviderMaxCount {
		return invalidModelError("AccessPolicySettings.Validate", "access_policy_settings", "", "contains too many provider admissions", "")
	}
	if s.PublicRegistrationEnabled && !s.LocalLoginEnabled {
		return invalidModelError("AccessPolicySettings.Validate", "access_policy_settings", "", "public registration requires local login", "")
	}

	if s.SessionIdleMinutes < 0 ||
		s.SessionIdleMinutes > AccessPolicySessionMinutesMaximum ||
		s.SessionAbsoluteMinutes < 0 ||
		s.SessionAbsoluteMinutes > AccessPolicySessionMinutesMaximum {
		return invalidModelError("AccessPolicySettings.Validate", "access_policy_settings", "", "session lengths are out of range", "")
	}

	if s.SessionIdleMinutes != 0 && s.SessionAbsoluteMinutes != 0 && s.SessionIdleMinutes > s.SessionAbsoluteMinutes {
		return invalidModelError(
			"AccessPolicySettings.Validate",
			"access_policy_settings",
			"session_idle_minutes",
			"exceeds the absolute session length",
			"",
		)
	}

	if s.InvitationLocalCredentialEnabled &&
		(!s.InvitationAdmissionEnabled || !s.LocalLoginEnabled) {
		return invalidModelError(
			"AccessPolicySettings.Validate",
			"access_policy_settings",
			"",
			"invitation local credentials require invitation admission and local login",
			"",
		)
	}

	for providerID, mode := range s.ProviderAdmissions {
		if !IsValidIdentityProviderID(providerID) {
			return invalidModelError("AccessPolicySettings.Validate", "access_policy_settings", "", "provider ID is invalid", "")
		}

		switch mode {
		case ProviderAdmissionLinkedOnly, ProviderAdmissionInvitationRequired, ProviderAdmissionAutoProvision:
		default:
			return invalidModelError(
				"AccessPolicySettings.Validate",
				"access_policy_settings",
				"",
				fmt.Sprintf("provider admission %q is invalid", mode),
				"",
			)
		}
	}

	return nil
}

// ValidateSessionLengths checks the Session lengths against the deployment
// limits. A set idle length lies within its bounds and within the effective
// absolute length; a set absolute length lies within its bounds. The returned
// ValidationError names the offending field.
func (s AccessPolicySettings) ValidateSessionLengths(limits SessionLengthLimits) error {
	const where = "AccessPolicySettings.ValidateSessionLengths"
	if s.SessionIdleMinutes == 0 && s.SessionAbsoluteMinutes == 0 {
		return nil
	}

	// A deployment whose own lifetimes the policy cannot express refuses
	// every explicit length.
	valid := limits.Validate() == nil
	_, absolute := s.EffectiveSessionMinutes(limits)
	if s.SessionAbsoluteMinutes != 0 &&
		(!valid ||
			s.SessionAbsoluteMinutes < limits.AbsoluteMinimumMinutes ||
			s.SessionAbsoluteMinutes > limits.AbsoluteMaximumMinutes) {
		return invalidModelError(where, "access_policy_settings", "session_absolute_minutes", "is outside the deployment bounds", "")
	}

	if s.SessionIdleMinutes != 0 &&
		(!valid ||
			s.SessionIdleMinutes < limits.IdleMinimumMinutes ||
			s.SessionIdleMinutes > limits.IdleMaximumMinutes ||
			s.SessionIdleMinutes > absolute) {
		return invalidModelError(
			where,
			"access_policy_settings",
			"session_idle_minutes",
			"is outside the deployment bounds or the absolute length",
			"",
		)
	}

	return nil
}

// EffectiveSessionMinutes returns the idle and absolute Session lengths a new
// Session gets: each set length, otherwise its deployment default, with the
// idle length never longer than the absolute one.
func (s AccessPolicySettings) EffectiveSessionMinutes(limits SessionLengthLimits) (idle, absolute int) {
	idle, absolute = limits.IdleDefaultMinutes, limits.AbsoluteDefaultMinutes
	if s.SessionIdleMinutes != 0 && s.SessionIdleMinutes < idle {
		idle = s.SessionIdleMinutes
	}
	if s.SessionAbsoluteMinutes != 0 && s.SessionAbsoluteMinutes < absolute {
		absolute = s.SessionAbsoluteMinutes
	}
	return min(idle, absolute), absolute
}

func (p *AccessPolicy) Settings() AccessPolicySettings {
	if p == nil {
		return AccessPolicySettings{}
	}
	return AccessPolicySettings{
		LocalLoginEnabled:                p.LocalLoginEnabled,
		PublicRegistrationEnabled:        p.PublicRegistrationEnabled,
		InvitationAdmissionEnabled:       p.InvitationAdmissionEnabled,
		InvitationLocalCredentialEnabled: p.InvitationLocalCredentialEnabled,
		DesktopAuthorizationEnabled:      p.DesktopAuthorizationEnabled,
		ProviderAdmissions:               cloneProviderAdmissions(p.ProviderAdmissions),
		RoleHolderMFARequired:            p.RoleHolderMFARequired,
		SessionIdleMinutes:               p.SessionIdleMinutes,
		SessionAbsoluteMinutes:           p.SessionAbsoluteMinutes,
	}
}

// Replace applies one complete, revision-fenced policy replacement and returns
// the only safe history fields that persistence may retain.
func (p *AccessPolicy) Replace(
	expectedRevision int64,
	settings AccessPolicySettings,
	actorID UserID,
	at time.Time,
) (*AccessPolicyTransition, error) {
	if p == nil || p.Validate() != nil || !actorID.IsValid() || at.IsZero() {
		return nil, ErrAccessPolicyInvalidTransition
	}
	if expectedRevision != p.Revision {
		return nil, ErrAccessPolicyRevisionConflict
	}

	at = TimeUTC(at)
	if at.Before(p.UpdatedAt) || settings.Validate() != nil {
		return nil, ErrAccessPolicyInvalidTransition
	}

	changed := changedAccessPolicyFields(p.Settings(), settings)
	if len(changed) == 0 {
		return nil, nil
	}

	from := p.Revision
	p.LocalLoginEnabled = settings.LocalLoginEnabled
	p.PublicRegistrationEnabled = settings.PublicRegistrationEnabled
	p.InvitationAdmissionEnabled = settings.InvitationAdmissionEnabled
	p.InvitationLocalCredentialEnabled = settings.InvitationLocalCredentialEnabled
	p.DesktopAuthorizationEnabled = settings.DesktopAuthorizationEnabled
	p.ProviderAdmissions = cloneProviderAdmissions(settings.ProviderAdmissions)
	p.RoleHolderMFARequired = settings.RoleHolderMFARequired
	p.SessionIdleMinutes = settings.SessionIdleMinutes
	p.SessionAbsoluteMinutes = settings.SessionAbsoluteMinutes
	p.Revision++
	p.UpdatedAt = at
	if err := p.Validate(); err != nil {
		return nil, ErrAccessPolicyInvalidTransition
	}

	return &AccessPolicyTransition{
		PolicyID:      p.ID,
		FromRevision:  from,
		ToRevision:    p.Revision,
		ActorID:       actorID,
		ChangedFields: changed,
		ChangedAt:     at,
		Outcome:       AccessPolicyTransitionApplied,
	}, nil
}

func (t *AccessPolicyTransition) Validate() error {
	if t == nil ||
		!t.PolicyID.IsValid() ||
		t.FromRevision < 1 ||
		t.ToRevision != t.FromRevision+1 ||
		!t.ActorID.IsValid() ||
		t.ChangedAt.IsZero() ||
		t.Outcome != AccessPolicyTransitionApplied ||
		len(t.ChangedFields) == 0 ||
		len(t.ChangedFields) > AccessPolicyTransitionFieldMaxCount {
		return ErrAccessPolicyInvalidTransition
	}

	previous := ""
	for _, field := range t.ChangedFields {
		if !slices.Contains(accessPolicyTransitionFields, field) || field <= previous {
			return ErrAccessPolicyInvalidTransition
		}

		previous = field
	}

	return nil
}

// accessPolicyTransitionFields are the only names a transition may record:
// each replaceable setting, plus the one-shot revoke_existing_sessions choice.
var accessPolicyTransitionFields = []string{
	"desktop_authorization_enabled",
	"invitation_admission_enabled",
	"invitation_local_credential_enabled",
	"local_login_enabled",
	"provider_admissions",
	"public_registration_enabled",
	"revoke_existing_sessions",
	"role_holder_mfa_required",
	"session_absolute_minutes",
	"session_idle_minutes",
}

func changedAccessPolicyFields(current, next AccessPolicySettings) []string {
	changed := make([]string, 0, len(accessPolicyTransitionFields))
	if current.LocalLoginEnabled != next.LocalLoginEnabled {
		changed = append(changed, "local_login_enabled")
	}

	if current.PublicRegistrationEnabled != next.PublicRegistrationEnabled {
		changed = append(changed, "public_registration_enabled")
	}

	if current.InvitationAdmissionEnabled != next.InvitationAdmissionEnabled {
		changed = append(changed, "invitation_admission_enabled")
	}

	if current.InvitationLocalCredentialEnabled != next.InvitationLocalCredentialEnabled {
		changed = append(changed, "invitation_local_credential_enabled")
	}

	if current.DesktopAuthorizationEnabled != next.DesktopAuthorizationEnabled {
		changed = append(changed, "desktop_authorization_enabled")
	}

	if !providerAdmissionsEqual(current.ProviderAdmissions, next.ProviderAdmissions) {
		changed = append(changed, "provider_admissions")
	}

	if current.RoleHolderMFARequired != next.RoleHolderMFARequired {
		changed = append(changed, "role_holder_mfa_required")
	}

	if current.SessionIdleMinutes != next.SessionIdleMinutes {
		changed = append(changed, "session_idle_minutes")
	}

	if current.SessionAbsoluteMinutes != next.SessionAbsoluteMinutes {
		changed = append(changed, "session_absolute_minutes")
	}

	slices.Sort(changed)
	return changed
}

func providerAdmissionsEqual(left, right map[string]ProviderAdmissionMode) bool {
	if len(left) != len(right) {
		return false
	}
	for id, mode := range left {
		if right[id] != mode {
			return false
		}
	}
	return true
}

func cloneProviderAdmissions(source map[string]ProviderAdmissionMode) map[string]ProviderAdmissionMode {
	clone := make(map[string]ProviderAdmissionMode, len(source))
	for id, mode := range source {
		clone[id] = mode
	}
	return clone
}

func (p *AccessPolicy) Clone() *AccessPolicy {
	if p == nil {
		return nil
	}
	clone := *p
	clone.ProviderAdmissions = cloneProviderAdmissions(p.ProviderAdmissions)
	return &clone
}

func (p *AccessPolicy) Auditable() map[string]any {
	if p == nil {
		return map[string]any{}
	}

	return map[string]any{
		"id":                                    p.ID.String(),
		"revision":                              p.Revision,
		"local_login_enabled":                   p.LocalLoginEnabled,
		"public_registration_enabled":           p.PublicRegistrationEnabled,
		"invitation_admission_enabled":          p.InvitationAdmissionEnabled,
		"invitation_local_credential_enabled":   p.InvitationLocalCredentialEnabled,
		"desktop_authorization_enabled":         p.DesktopAuthorizationEnabled,
		"configured_provider_admission_entries": len(p.ProviderAdmissions),
		"role_holder_mfa_required":              p.RoleHolderMFARequired,
		"session_idle_minutes":                  p.SessionIdleMinutes,
		"session_absolute_minutes":              p.SessionAbsoluteMinutes,
	}
}

var _ Auditable = (*AccessPolicy)(nil)
