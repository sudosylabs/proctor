// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"sort"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type AccessPolicyApplication interface {
	GetAccessPolicy(context.Context, invocation.Invocation) (administration.AccessPolicyView, error)
	PreflightAccessPolicy(
		context.Context,
		invocation.Invocation,
		administration.PreflightAccessPolicyCommand,
	) (administration.AccessPolicyPreflightResult, error)
	ReplaceAccessPolicy(
		context.Context,
		invocation.Invocation,
		administration.ReplaceAccessPolicyCommand,
	) (administration.AccessPolicyView, error)
	DiscoverAccess(context.Context, invocation.Invocation) (administration.PublicAccessDiscovery, error)
}

type accessPolicySettingsRequest struct {
	ExpectedRevision                 int64                                                  `json:"expected_revision"`
	RevokeExistingSessions           route.Optional[bool]                                   `json:"revoke_existing_sessions"`
	LocalLoginEnabled                route.Optional[bool]                                   `json:"local_login_enabled"`
	PublicRegistrationEnabled        route.Optional[bool]                                   `json:"public_registration_enabled"`
	InvitationAdmissionEnabled       route.Optional[bool]                                   `json:"invitation_admission_enabled"`
	InvitationLocalCredentialEnabled route.Optional[bool]                                   `json:"invitation_local_credential_enabled"`
	DesktopAuthorizationEnabled      route.Optional[bool]                                   `json:"desktop_authorization_enabled"`
	ProviderAdmissions               route.Optional[map[string]model.ProviderAdmissionMode] `json:"provider_admissions"`
	RoleHolderMFARequired            route.Optional[bool]                                   `json:"role_holder_mfa_required"`
	SessionIdleMinutes               route.Optional[int]                                    `json:"session_idle_minutes"`
	SessionAbsoluteMinutes           route.Optional[int]                                    `json:"session_absolute_minutes"`
}

type accessPolicyResponse struct {
	ID                               string                                 `json:"id"`
	Revision                         int64                                  `json:"revision"`
	CreatedAt                        string                                 `json:"created_at"`
	UpdatedAt                        string                                 `json:"updated_at"`
	LocalLoginEnabled                bool                                   `json:"local_login_enabled"`
	PublicRegistrationEnabled        bool                                   `json:"public_registration_enabled"`
	InvitationAdmissionEnabled       bool                                   `json:"invitation_admission_enabled"`
	InvitationLocalCredentialEnabled bool                                   `json:"invitation_local_credential_enabled"`
	DesktopAuthorizationEnabled      bool                                   `json:"desktop_authorization_enabled"`
	ProviderAdmissions               map[string]model.ProviderAdmissionMode `json:"provider_admissions"`
	RoleHolderMFARequired            bool                                   `json:"role_holder_mfa_required"`
	RoleHolderMFAEnforced            bool                                   `json:"role_holder_mfa_enforced"`
	// SessionIdleMinutes and SessionAbsoluteMinutes are null while the
	// deployment default applies.
	SessionIdleMinutes            *int                                     `json:"session_idle_minutes"`
	SessionAbsoluteMinutes        *int                                     `json:"session_absolute_minutes"`
	SessionIdleMinutesDefault     int                                      `json:"session_idle_minutes_default"`
	SessionIdleMinutesMaximum     int                                      `json:"session_idle_minutes_maximum"`
	SessionAbsoluteMinutesDefault int                                      `json:"session_absolute_minutes_default"`
	SessionAbsoluteMinutesMaximum int                                      `json:"session_absolute_minutes_maximum"`
	History                       []accessPolicyTransitionResponse         `json:"history"`
	AvailableProviders            []externalAuthenticationProviderResponse `json:"available_providers"`
	DurableMail                   bool                                     `json:"durable_mail"`
}

type accessPolicyTransitionResponse struct {
	FromRevision  int64    `json:"from_revision"`
	ToRevision    int64    `json:"to_revision"`
	ActorUserID   string   `json:"actor_user_id"`
	ChangedFields []string `json:"changed_fields"`
	ChangedAt     string   `json:"changed_at"`
	Outcome       string   `json:"outcome"`
}

type accessPolicyBlockerResponse struct {
	Code       string `json:"code"`
	ProviderID string `json:"provider_id,omitempty"`
}
type accessPolicyPreflightResponse struct {
	Blockers                           []accessPolicyBlockerResponse `json:"blockers"`
	RoleHoldersWithoutMFACount         int                           `json:"role_holders_without_mfa_count"`
	RoleHolderSingleFactorSessionCount int                           `json:"role_holder_single_factor_session_count"`
}

type publicInstitutionPresentationResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}
type publicAccessCapabilitiesResponse struct {
	LocalLogin           bool `json:"local_login"`
	PublicRegistration   bool `json:"public_registration"`
	InvitationAdmission  bool `json:"invitation_admission"`
	DesktopAuthorization bool `json:"desktop_authorization"`
}
type desktopAuthorizationCompatibilityResponse struct {
	Protocol       string `json:"protocol"`
	MinimumVersion int    `json:"minimum_version"`
	MaximumVersion int    `json:"maximum_version"`
}
type publicAccessDiscoveryResponse struct {
	DiscoveryVersion     int                                       `json:"discovery_version"`
	CanonicalOrigin      string                                    `json:"canonical_origin"`
	InstallationID       string                                    `json:"installation_id,omitempty"`
	Initialized          bool                                      `json:"initialized"`
	Institution          *publicInstitutionPresentationResponse    `json:"institution,omitempty"`
	PolicyRevision       int64                                     `json:"policy_revision,omitempty"`
	Capabilities         publicAccessCapabilitiesResponse          `json:"capabilities"`
	Providers            []externalAuthenticationProviderResponse  `json:"providers"`
	DesktopAuthorization desktopAuthorizationCompatibilityResponse `json:"desktop_authorization"`
	// SupportAddress and PasswordMinimumLength are operator-configured
	// presentation facts for hosted pages.
	SupportAddress        string `json:"support_address,omitempty"`
	PasswordMinimumLength int    `json:"password_minimum_length"`
}

type accessPolicyHandlers struct{ app AccessPolicyApplication }

func accessPolicyResource(app AccessPolicyApplication) route.Resource {
	h := accessPolicyHandlers{app: app}
	replace := route.Endpoint{
		Method:      http.MethodPut,
		Path:        "/access-policy",
		Auth:        route.AuthStrongRecentSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors: []string{
			"authorization.denied",
			"authorization.request.invalid",
			"authorization.unavailable",
			"audit.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
			"request.invalid",
			"access_policy.invalid",
			"access_policy.revision_conflict",
			"access_policy.blocked",
			"access_policy.unavailable",
			"resource.not_found",
		},
		Handle: h.replace,
	}
	return route.NewResource("access-policy",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/discovery",
			Auth:   route.AuthPublic,
			Errors: []string{"access_policy.unavailable", "access_policy.blocked", "access_policy.revision_conflict"},
			Handle: h.discover,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/access-policy",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"access_policy.unavailable",
				"access_policy.blocked",
				"access_policy.revision_conflict",
				"resource.not_found",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/access-policy/preflight",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"access_policy.invalid",
				"access_policy.revision_conflict",
				"access_policy.unavailable",
				"access_policy.blocked",
				"resource.not_found",
			},
			Handle: h.preflight,
		},
		replace,
	)
}

func (h accessPolicyHandlers) get(request route.Request) (route.Result, error) {
	view, err := h.app.GetAccessPolicy(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, accessPolicyResponseFromView(view)), nil
}

func (h accessPolicyHandlers) preflight(request route.Request) (route.Result, error) {
	var body accessPolicySettingsRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	settings, revokeExistingSessions, err := requiredAccessPolicySettings(body)
	if err != nil {
		return route.Result{}, err
	}

	result, err := h.app.PreflightAccessPolicy(request.Context, request.Invocation(), administration.PreflightAccessPolicyCommand{
		ExpectedRevision:       body.ExpectedRevision,
		Settings:               settings,
		RevokeExistingSessions: revokeExistingSessions,
	})
	if err != nil {
		return route.Result{}, err
	}

	response := accessPolicyPreflightResponse{
		Blockers:                           make([]accessPolicyBlockerResponse, 0, len(result.Blockers)),
		RoleHoldersWithoutMFACount:         result.RoleHoldersWithoutMFACount,
		RoleHolderSingleFactorSessionCount: result.RoleHolderSingleFactorSessionCount,
	}
	for _, blocker := range result.Blockers {
		response.Blockers = append(response.Blockers, accessPolicyBlockerResponse{Code: string(blocker.Code), ProviderID: blocker.ProviderID})
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h accessPolicyHandlers) replace(request route.Request) (route.Result, error) {
	var body accessPolicySettingsRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	settings, revokeExistingSessions, err := requiredAccessPolicySettings(body)
	if err != nil {
		return route.Result{}, err
	}

	view, err := h.app.ReplaceAccessPolicy(request.Context, request.Invocation(), administration.ReplaceAccessPolicyCommand{
		ExpectedRevision:       body.ExpectedRevision,
		Settings:               settings,
		RevokeExistingSessions: revokeExistingSessions,
		IdempotencyKey:         request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, accessPolicyResponseFromView(view)), nil
}

func requiredAccessPolicySettings(body accessPolicySettingsRequest) (model.AccessPolicySettings, bool, error) {
	requiredBoolean := func(field string, value route.Optional[bool]) (bool, error) {
		candidate := value.ValuePointer()
		if candidate == nil {
			return false, apperror.New("request.invalid").WithField("field", field)
		}

		return *candidate, nil
	}
	revoke, err := requiredBoolean("revoke_existing_sessions", body.RevokeExistingSessions)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	localLogin, err := requiredBoolean("local_login_enabled", body.LocalLoginEnabled)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	publicRegistration, err := requiredBoolean("public_registration_enabled", body.PublicRegistrationEnabled)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	invitationAdmission, err := requiredBoolean("invitation_admission_enabled", body.InvitationAdmissionEnabled)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	invitationLocalCredential, err := requiredBoolean("invitation_local_credential_enabled", body.InvitationLocalCredentialEnabled)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	desktopAuthorization, err := requiredBoolean("desktop_authorization_enabled", body.DesktopAuthorizationEnabled)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	providerAdmissions := body.ProviderAdmissions.ValuePointer()
	if providerAdmissions == nil {
		return model.AccessPolicySettings{}, false, apperror.New("request.invalid").WithField("field", "provider_admissions")
	}

	roleHolderMFARequired, err := requiredBoolean("role_holder_mfa_required", body.RoleHolderMFARequired)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	// A Session length is required but may be null, which keeps the
	// deployment default; a length below one minute is malformed.
	requiredMinutes := func(field string, value route.Optional[int]) (int, error) {
		if !value.IsSet() {
			return 0, apperror.New("request.invalid").WithField("field", field)
		}

		minutes := value.ValuePointer()
		if minutes == nil {
			return 0, nil
		}
		if *minutes < 1 {
			return 0, apperror.New("request.invalid").WithField("field", field)
		}

		return *minutes, nil
	}
	idleMinutes, err := requiredMinutes("session_idle_minutes", body.SessionIdleMinutes)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	absoluteMinutes, err := requiredMinutes("session_absolute_minutes", body.SessionAbsoluteMinutes)
	if err != nil {
		return model.AccessPolicySettings{}, false, err
	}

	return model.AccessPolicySettings{
		LocalLoginEnabled:                localLogin,
		PublicRegistrationEnabled:        publicRegistration,
		InvitationAdmissionEnabled:       invitationAdmission,
		InvitationLocalCredentialEnabled: invitationLocalCredential,
		DesktopAuthorizationEnabled:      desktopAuthorization,
		ProviderAdmissions:               *providerAdmissions,
		RoleHolderMFARequired:            roleHolderMFARequired,
		SessionIdleMinutes:               idleMinutes,
		SessionAbsoluteMinutes:           absoluteMinutes,
	}, revoke, nil
}

func (h accessPolicyHandlers) discover(request route.Request) (route.Result, error) {
	discovery, err := h.app.DiscoverAccess(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, publicAccessDiscoveryResponseFromView(discovery)), nil
}

func accessPolicyResponseFromView(view administration.AccessPolicyView) accessPolicyResponse {
	response := accessPolicyResponse{
		History:            []accessPolicyTransitionResponse{},
		AvailableProviders: []externalAuthenticationProviderResponse{},
	}
	if view.Policy == nil {
		return response
	}

	p := view.Policy
	response.ID, response.Revision, response.CreatedAt, response.UpdatedAt =
		p.ID.String(),
		p.Revision,
		model.FormatRFC3339(p.CreatedAt),
		model.FormatRFC3339(p.UpdatedAt)
	response.LocalLoginEnabled, response.PublicRegistrationEnabled = p.LocalLoginEnabled, p.PublicRegistrationEnabled
	response.InvitationAdmissionEnabled, response.InvitationLocalCredentialEnabled =
		p.InvitationAdmissionEnabled,
		p.InvitationLocalCredentialEnabled
	response.DesktopAuthorizationEnabled, response.ProviderAdmissions = p.DesktopAuthorizationEnabled, p.Settings().ProviderAdmissions
	response.RoleHolderMFARequired, response.RoleHolderMFAEnforced = p.RoleHolderMFARequired, view.RoleHolderMFAEnforced
	response.SessionIdleMinutes, response.SessionAbsoluteMinutes =
		optionalMinutes(p.SessionIdleMinutes),
		optionalMinutes(p.SessionAbsoluteMinutes)
	response.SessionIdleMinutesDefault, response.SessionIdleMinutesMaximum =
		view.SessionLimits.IdleDefaultMinutes,
		view.SessionLimits.IdleMaximumMinutes
	response.SessionAbsoluteMinutesDefault, response.SessionAbsoluteMinutesMaximum =
		view.SessionLimits.AbsoluteDefaultMinutes,
		view.SessionLimits.AbsoluteMaximumMinutes
	response.DurableMail = view.DurableMail
	for _, transition := range view.History {
		if transition != nil {
			response.History = append(response.History, accessPolicyTransitionResponse{
				FromRevision:  transition.FromRevision,
				ToRevision:    transition.ToRevision,
				ActorUserID:   transition.ActorID.String(),
				ChangedFields: append([]string(nil), transition.ChangedFields...),
				ChangedAt:     model.FormatRFC3339(transition.ChangedAt),
				Outcome:       string(transition.Outcome),
			})
		}
	}

	for _, provider := range view.AvailableProviders {
		response.AvailableProviders = append(response.AvailableProviders, externalAuthenticationProviderResponse{
			ID:          provider.ID,
			DisplayName: provider.DisplayName,
			Type:        provider.Type,
		})
	}

	return response
}

// optionalMinutes presents an unset Session length as null.
func optionalMinutes(minutes int) *int {
	if minutes <= 0 {
		return nil
	}
	return &minutes
}

func publicAccessDiscoveryResponseFromView(view administration.PublicAccessDiscovery) publicAccessDiscoveryResponse {
	response := publicAccessDiscoveryResponse{
		DiscoveryVersion: view.DiscoveryVersion,
		CanonicalOrigin:  view.CanonicalOrigin,
		InstallationID:   view.InstallationID,
		Initialized:      view.Initialized,
		PolicyRevision:   view.PolicyRevision,
		Capabilities: publicAccessCapabilitiesResponse{
			LocalLogin:           view.Capabilities.LocalLogin,
			PublicRegistration:   view.Capabilities.PublicRegistration,
			InvitationAdmission:  view.Capabilities.InvitationAdmission,
			DesktopAuthorization: view.Capabilities.DesktopAuthorization,
		},
		Providers: []externalAuthenticationProviderResponse{},
		DesktopAuthorization: desktopAuthorizationCompatibilityResponse{
			Protocol:       view.DesktopAuthorization.Protocol,
			MinimumVersion: view.DesktopAuthorization.MinimumVersion,
			MaximumVersion: view.DesktopAuthorization.MaximumVersion,
		},
		SupportAddress:        view.SupportAddress,
		PasswordMinimumLength: view.PasswordMinimumLength,
	}
	if view.Institution != nil {
		response.Institution = &publicInstitutionPresentationResponse{
			ID:          view.Institution.ID.String(),
			Name:        view.Institution.Name,
			DisplayName: view.Institution.DisplayName,
		}
	}

	for _, provider := range view.Providers {
		response.Providers = append(response.Providers, externalAuthenticationProviderResponse{
			ID:          provider.ID,
			DisplayName: provider.DisplayName,
			Type:        provider.Type,
		})
	}

	sort.Slice(response.Providers, func(i, j int) bool { return response.Providers[i].ID < response.Providers[j].ID })
	return response
}
