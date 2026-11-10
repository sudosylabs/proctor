// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	accessDiscoveryVersion          = 1
	desktopAuthorizationProtocolMin = 1
	desktopAuthorizationProtocolMax = 1
)

// AccessPolicyChange is a committed Access Policy replacement: the Sessions
// it revoked and the Sessions whose deadlines it shortened.
type AccessPolicyChange struct {
	InstitutionID          model.InstitutionID
	Revision               int64
	SessionRevocations     []store.AccessPolicySessionRevocation
	SessionDeadlineChanges []store.AccessPolicySessionDeadlineChange
}

// sessionLengthLimits derives the policy's Session length bounds from the
// deployment lifetimes. The idle length outlasts an access credential and the
// activity debounce; the absolute length outlasts both that floor and the
// recent-authentication window; each default and ceiling is the configured
// lifetime, which the policy can only shorten.
func sessionLengthLimits(sessions identity.SessionPolicy, recentAuthentication time.Duration) model.SessionLengthLimits {
	wholeMinutesUp := func(duration time.Duration) int { return int((duration + time.Minute - 1) / time.Minute) }
	idleMinimum := wholeMinutesUp(max(sessions.AccessTTL, sessions.ActivityUpdateInterval+time.Minute))
	idleDefault, absoluteDefault := int(sessions.IdleTTL/time.Minute), int(sessions.AbsoluteTTL/time.Minute)
	return model.SessionLengthLimits{
		IdleMinimumMinutes: idleMinimum, IdleDefaultMinutes: idleDefault,
		IdleMaximumMinutes:     min(idleDefault, model.AccessPolicySessionMinutesMaximum),
		AbsoluteMinimumMinutes: max(wholeMinutesUp(recentAuthentication), idleMinimum),
		AbsoluteDefaultMinutes: absoluteDefault,
		AbsoluteMaximumMinutes: min(absoluteDefault, model.AccessPolicySessionMinutesMaximum),
	}
}

type AccessPolicyView struct {
	Policy             *model.AccessPolicy
	History            []*model.AccessPolicyTransition
	AvailableProviders []model.ExternalAuthenticationProvider
	DurableMail        bool
	// RoleHolderMFAEnforced is true only while the policy requires two-step
	// verification of role holders and the deployment enables it.
	RoleHolderMFAEnforced bool
	// SessionLimits are the deployment defaults and bounds of the Session
	// lengths.
	SessionLimits model.SessionLengthLimits
}

type PreflightAccessPolicyCommand struct {
	ExpectedRevision       int64
	Settings               model.AccessPolicySettings
	RevokeExistingSessions bool
}

// AccessPolicyPreflightResult reports the blockers of a proposed replacement
// and the role holder two-step verification counts an administrator weighs.
type AccessPolicyPreflightResult struct {
	Blockers                           []store.AccessPolicyBlocker
	RoleHoldersWithoutMFACount         int
	RoleHolderSingleFactorSessionCount int
}

type ReplaceAccessPolicyCommand struct {
	ExpectedRevision       int64
	Settings               model.AccessPolicySettings
	RevokeExistingSessions bool
	IdempotencyKey         string
}

// GetAccessPolicy returns the Access Policy, its recent history and the
// deployment capabilities it is weighed against.
func (s *Service) GetAccessPolicy(ctx context.Context, invocation invocation.Invocation) (AccessPolicyView, error) {
	institution, resource, err := s.authorizeAccessPolicy(ctx, invocation, model.ActionAccessPolicyView)
	_ = institution
	_ = resource
	if err != nil {
		return AccessPolicyView{}, err
	}
	snapshot, err := s.accessPolicies.Get(ctx, model.AccessPolicyTransitionHistoryLimit)
	if err != nil || snapshot == nil || snapshot.Policy == nil {
		return AccessPolicyView{}, accessPolicyError(err)
	}
	return s.view(snapshot), nil
}

// PreflightAccessPolicy reports what would block a proposed replacement. It
// needs a strong, recent Session and access_policy.manage.
func (s *Service) PreflightAccessPolicy(ctx context.Context, invocation invocation.Invocation, command PreflightAccessPolicyCommand) (AccessPolicyPreflightResult, error) {
	if err := s.requireStrongRecent(invocation); err != nil {
		return AccessPolicyPreflightResult{}, err
	}
	if _, _, err := s.authorizeAccessPolicy(ctx, invocation, model.ActionAccessPolicyManage); err != nil {
		return AccessPolicyPreflightResult{}, err
	}
	input, err := s.preflightInput(command.ExpectedRevision, command.Settings, command.RevokeExistingSessions)
	if err != nil {
		return AccessPolicyPreflightResult{}, err
	}
	preflight, err := s.accessPolicies.Preflight(ctx, input)
	if err != nil || preflight == nil {
		return AccessPolicyPreflightResult{}, accessPolicyError(err)
	}
	return AccessPolicyPreflightResult{Blockers: append([]store.AccessPolicyBlocker(nil), preflight.Blockers...),
		RoleHoldersWithoutMFACount:         preflight.RoleHoldersWithoutMFACount,
		RoleHolderSingleFactorSessionCount: preflight.RoleHolderSingleFactorSessionCount}, nil
}

// ReplaceAccessPolicy replaces the Access Policy behind revision and
// idempotency fences, then propagates the Sessions it revoked or shortened.
func (s *Service) ReplaceAccessPolicy(ctx context.Context, invocation invocation.Invocation, command ReplaceAccessPolicyCommand) (AccessPolicyView, error) {
	if err := s.requireStrongRecent(invocation); err != nil {
		return AccessPolicyView{}, err
	}
	institution, _, err := s.authorizeAccessPolicy(ctx, invocation, model.ActionAccessPolicyManage)
	if err != nil {
		return AccessPolicyView{}, err
	}
	if command.IdempotencyKey == "" {
		return AccessPolicyView{}, apperror.New("idempotency.key_required")
	}
	input, err := s.preflightInput(command.ExpectedRevision, command.Settings, command.RevokeExistingSessions)
	if err != nil {
		return AccessPolicyView{}, err
	}
	idempotency, err := idempotency.Prepare(invocation, "access_policy.replace", command.IdempotencyKey,
		struct {
			ExpectedRevision       int64                      `json:"expected_revision"`
			Settings               model.AccessPolicySettings `json:"settings"`
			RevokeExistingSessions bool                       `json:"revoke_existing_sessions"`
		}{command.ExpectedRevision, command.Settings, command.RevokeExistingSessions})
	if err != nil {
		return AccessPolicyView{}, err
	}
	stored, err := access.RunAudited(ctx, s.auditor, access.Mutation{
		Invocation: invocation, Action: model.ActionAccessPolicyManage,
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: institution.ID.String(), Operation: "replace",
		Value: accessPolicyReplacementAuditValue(command),
	}, s.now, func(ctx context.Context, reference access.MutationReference) (*store.AccessPolicyReplacementResult, error) {
		return s.accessPolicies.Replace(ctx, &store.AccessPolicyReplacement{Preflight: *input,
			ActorID: invocation.Principal().UserID, AuditEventID: reference.ID, AuditAt: reference.MutationAt}, idempotency)
	}, accessPolicyError)
	if err != nil {
		return AccessPolicyView{}, err
	}
	if stored == nil || stored.Snapshot == nil || stored.Snapshot.Policy == nil {
		return AccessPolicyView{}, apperror.New("access_policy.unavailable")
	}
	if stored.Changed && !stored.Replayed {
		if effectErr := s.effects.AccessPolicyChanged(ctx, AccessPolicyChange{InstitutionID: institution.ID,
			Revision: stored.Snapshot.Policy.Revision, SessionRevocations: stored.SessionRevocations,
			SessionDeadlineChanges: stored.SessionDeadlineChanges}); effectErr != nil {
			s.effects.Report(ctx, "access_policy_changed", effectErr)
		}
	}
	return s.view(stored.Snapshot), nil
}

func accessPolicyReplacementAuditValue(command ReplaceAccessPolicyCommand) map[string]any {
	settings := command.Settings
	return map[string]any{
		"expected_revision":                   command.ExpectedRevision,
		"revoke_existing_sessions":            command.RevokeExistingSessions,
		"local_login_enabled":                 settings.LocalLoginEnabled,
		"public_registration_enabled":         settings.PublicRegistrationEnabled,
		"invitation_admission_enabled":        settings.InvitationAdmissionEnabled,
		"invitation_local_credential_enabled": settings.InvitationLocalCredentialEnabled,
		"desktop_authorization_enabled":       settings.DesktopAuthorizationEnabled,
		"provider_admissions":                 settings.ProviderAdmissions,
		"role_holder_mfa_required":            settings.RoleHolderMFARequired,
		"session_idle_minutes":                settings.SessionIdleMinutes,
		"session_absolute_minutes":            settings.SessionAbsoluteMinutes,
	}
}

// authorizeAccessPolicy authorizes an Access Policy Action on the singleton
// Institution.
func (s *Service) authorizeAccessPolicy(ctx context.Context, invocation invocation.Invocation, action model.Action) (*model.Institution, model.Resource, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, model.Resource{}, accessPolicyError(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err = s.authorizer.Authorize(ctx, invocation, action, resource); err != nil {
		return nil, model.Resource{}, err
	}
	return institution, resource, nil
}

func (s *Service) requireStrongRecent(invocation invocation.Invocation) error {
	return identity.RequireStrongRecentSession(invocation.Principal(), s.now(), s.recentAuthenticationTTL)
}

func (s *Service) preflightInput(expected int64, settings model.AccessPolicySettings, revokeExistingSessions bool) (*store.AccessPolicyPreflight, error) {
	if expected < 1 {
		return nil, apperror.New("access_policy.invalid")
	}
	if err := settings.Validate(); err != nil {
		return nil, invalidAccessPolicySettings(err)
	}
	if err := settings.ValidateSessionLengths(s.sessionLimits); err != nil {
		return nil, invalidAccessPolicySettings(err)
	}
	capabilities := identity.AccessDeploymentCapabilities(s.capabilities.Snapshot())
	return &store.AccessPolicyPreflight{ExpectedRevision: expected, Settings: settings,
		RevokeExistingSessions: revokeExistingSessions,
		Capabilities:           capabilities, SessionLimits: s.sessionLimits,
		CheckedAt: model.TimeUTC(s.now())}, nil
}

// invalidAccessPolicySettings refuses settings as access_policy.invalid,
// naming the offending field when the model names one.
func invalidAccessPolicySettings(err error) error {
	var invalid *model.ValidationError
	if errors.As(err, &invalid) && invalid.Field != "" {
		return apperror.Invalid("access_policy.invalid", invalid.Field).Wrap(err)
	}
	return apperror.New("access_policy.invalid").Wrap(err)
}

func (s *Service) view(snapshot *store.AccessPolicySnapshot) AccessPolicyView {
	if snapshot == nil || snapshot.Policy == nil {
		return AccessPolicyView{}
	}
	capabilities := s.capabilities.Snapshot()
	providers := make([]model.ExternalAuthenticationProvider, 0, len(capabilities.Providers))
	for _, provider := range capabilities.Providers {
		providers = append(providers, provider.Descriptor)
	}
	return AccessPolicyView{Policy: snapshot.Policy.Clone(), History: cloneAccessPolicyHistory(snapshot.History),
		AvailableProviders: providers, DurableMail: capabilities.DurableMail,
		RoleHolderMFAEnforced: snapshot.Policy.RoleHolderMFARequired && capabilities.MFA, SessionLimits: s.sessionLimits}
}

type PublicInstitutionPresentation struct {
	ID                model.InstitutionID
	Name, DisplayName string
}
type PublicAccessCapabilities struct {
	LocalLogin, PublicRegistration, InvitationAdmission, DesktopAuthorization bool
}
type DesktopAuthorizationCompatibility struct {
	Protocol                       string
	MinimumVersion, MaximumVersion int
}
type PublicAccessDiscovery struct {
	DiscoveryVersion int
	CanonicalOrigin  string
	// SupportAddress is empty when the operator configured none.
	SupportAddress        string
	PasswordMinimumLength int
	InstallationID        string
	Initialized           bool
	Institution           *PublicInstitutionPresentation
	PolicyRevision        int64
	Capabilities          PublicAccessCapabilities
	Providers             []model.ExternalAuthenticationProvider
	DesktopAuthorization  DesktopAuthorizationCompatibility
}

// DiscoverAccess returns the public access discovery document. It is
// anonymous and reports an uninitialized installation without failing.
func (s *Service) DiscoverAccess(ctx context.Context, _ invocation.Invocation) (PublicAccessDiscovery, error) {
	result := PublicAccessDiscovery{DiscoveryVersion: accessDiscoveryVersion, CanonicalOrigin: s.canonicalOrigin,
		SupportAddress: s.supportAddress, PasswordMinimumLength: s.passwordMinimumLength,
		DesktopAuthorization: DesktopAuthorizationCompatibility{Protocol: "proctor-desktop-authorization", MinimumVersion: desktopAuthorizationProtocolMin, MaximumVersion: desktopAuthorizationProtocolMax}}
	institution, err := s.institutions.GetSingleton(ctx)
	if store.IsNotFound(err) {
		return result, nil
	}
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return PublicAccessDiscovery{}, accessPolicyError(err)
	}
	snapshot, err := s.accessPolicies.Get(ctx, 0)
	if err != nil || snapshot == nil || snapshot.Policy == nil {
		return PublicAccessDiscovery{}, accessPolicyError(err)
	}
	result.Initialized, result.InstallationID = true, institution.ID.String()
	result.Institution = &PublicInstitutionPresentation{ID: institution.ID, Name: institution.Name, DisplayName: institution.DisplayName}
	policy := snapshot.Policy
	result.PolicyRevision = policy.Revision
	result.Capabilities = PublicAccessCapabilities{LocalLogin: policy.LocalLoginEnabled, PublicRegistration: policy.PublicRegistrationEnabled,
		InvitationAdmission: policy.InvitationAdmissionEnabled, DesktopAuthorization: policy.DesktopAuthorizationEnabled}
	for _, provider := range s.capabilities.Snapshot().Providers {
		if _, enabled := policy.ProviderAdmissions[provider.Descriptor.ID]; enabled {
			result.Providers = append(result.Providers, provider.Descriptor)
		}
	}
	sort.Slice(result.Providers, func(i, j int) bool { return result.Providers[i].ID < result.Providers[j].ID })
	return result, nil
}

func cloneAccessPolicyHistory(history []*model.AccessPolicyTransition) []*model.AccessPolicyTransition {
	result := make([]*model.AccessPolicyTransition, 0, len(history))
	for _, item := range history {
		if item != nil {
			clone := *item
			clone.ChangedFields = append([]string(nil), item.ChangedFields...)
			result = append(result, &clone)
		}
	}
	return result
}

func blockedAccessPolicyError(blockers []store.AccessPolicyBlocker, cause error) error {
	failure := apperror.New("access_policy.blocked").Wrap(cause)
	if len(blockers) > 0 {
		failure = failure.WithField("blocker", string(blockers[0].Code))
	}
	return failure
}

func accessPolicyError(err error) error {
	var revision *store.ErrAccessPolicyRevisionConflict
	var blocked *store.ErrAccessPolicyBlocked
	switch {
	case errors.As(err, &revision):
		return apperror.New("access_policy.revision_conflict").WithField("current_revision", strconv.FormatInt(revision.CurrentRevision, 10)).Wrap(err)
	case errors.As(err, &blocked):
		return blockedAccessPolicyError(blocked.Blockers, err)
	default:
		return apperror.StoreTable{Unavailable: "access_policy.unavailable"}.Translate(err)
	}
}
