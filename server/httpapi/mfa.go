// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/user.go MFA handlers. Proctor
// exposes the assurance transition explicitly, returns setup secrets and
// recovery codes only once, and classifies sensitive mutations with the
// strong/recent session wrappers.

package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type mfaCodeRequest struct {
	Code string `json:"code"`
}

type mfaRecoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

type mfaSetupResponse struct {
	Secret          string `json:"secret"`
	ProvisioningURI string `json:"provisioning_uri"`
	ExpiresAt       string `json:"expires_at"`
}

type mfaActivationResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

type mfaStatusResponse struct {
	ServiceEnabled           bool   `json:"service_enabled"`
	MFARecoveryRequired      bool   `json:"mfa_recovery_required"`
	MFAEnrollmentRequired    bool   `json:"mfa_enrollment_required"`
	AuthenticationMethod     string `json:"authentication_method"`
	AuthenticationProviderID string `json:"authentication_provider_id,omitempty"`
	AuthenticationStrength   string `json:"authentication_strength"`
	RecentlyAuthenticated    bool   `json:"recently_authenticated"`
	Enabled                  bool   `json:"enabled"`
	Pending                  bool   `json:"pending"`
	PendingExpiresAt         string `json:"pending_expires_at,omitempty"`
	Method                   string `json:"method,omitempty"`
	EnabledAt                string `json:"enabled_at,omitempty"`
	RecoveryCodesRemaining   int    `json:"recovery_codes_remaining"`
	RecoveryCodesTotal       int    `json:"recovery_codes_total"`
	RecoveryCodesGeneratedAt string `json:"recovery_codes_generated_at,omitempty"`
	PasswordChangedAt        string `json:"password_changed_at,omitempty"`
	// RecentAuthenticationMinutes is the configured window in whole minutes,
	// rounded down so clients never promise more time than the server allows.
	RecentAuthenticationMinutes int    `json:"recent_authentication_minutes"`
	RecentlyAuthenticatedUntil  string `json:"recently_authenticated_until,omitempty"`
}

type mfaResourceModule struct {
	mfa MFA
}

func mfaResource(mfa MFA) apiResource {
	module := mfaResourceModule{mfa: mfa}
	base := path("/users/me/mfa")
	return newResource(
		"multi-factor-authentication",
		mfaRecoverySessionRoute(http.MethodGet, base, []string{"authentication.mfa.unavailable", "authentication.reauthentication_required", "authentication.session_required"}, module.status),
		recentMFARecoverySessionRoute(http.MethodPost, path("/users/me/mfa/setup"), []string{"authentication.mfa.disabled", "authentication.mfa.not_found", "authentication.mfa.conflict", "authentication.mfa.unavailable", "authentication.internal", "audit.unavailable", "authentication.session_required"}, module.setup),
		recentMFARecoverySessionRoute(http.MethodPost, path("/users/me/mfa/activate"), []string{"request.invalid", "authentication.mfa.code_invalid", "authentication.rate_limited", "authentication.rate_limit_unavailable", "authentication.mfa.disabled", "authentication.mfa.not_found", "authentication.mfa.conflict", "authentication.mfa.unavailable", "authentication.internal", "audit.unavailable", "authentication.session_required"}, module.activate),
		mfaEnrollmentSessionRoute(http.MethodPost, path("/users/me/mfa/challenge"), []string{"request.invalid", "authentication.mfa.code_invalid", "authentication.rate_limited", "authentication.rate_limit_unavailable", "authentication.mfa.disabled", "authentication.mfa.not_found", "authentication.mfa.conflict", "authentication.mfa.unavailable", "authentication.internal", "audit.unavailable", "authentication.reauthentication_required", "authentication.session_required"}, module.challenge),
		strongRecentSessionRoute(http.MethodPost, path("/users/me/mfa/recovery-codes/regenerate"), []string{"authentication.mfa.disabled", "authentication.mfa.not_found", "authentication.mfa.conflict", "authentication.mfa.unavailable", "authentication.internal", "audit.unavailable", "authentication.session_required"}, module.regenerateRecoveryCodes),
		strongRecentSessionRoute(http.MethodPost, path("/users/me/mfa/disable"), []string{"mfa.required_by_policy", "authentication.mfa.disabled", "authentication.mfa.not_found", "authentication.mfa.conflict", "authentication.mfa.unavailable", "audit.unavailable", "authentication.session_required"}, module.disable),
	)
}

func (m mfaResourceModule) status(request operationRequest) (operationResult, error) {
	status, err := m.mfa.GetMFAStatus(
		request.context,
		request.invocation(),
		identity.GetMFAStatusQuery{},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, mfaStatusResponse{
		ServiceEnabled: status.ServiceEnabled, MFARecoveryRequired: status.MFARecoveryRequired,
		MFAEnrollmentRequired: status.MFAEnrollmentRequired,
		AuthenticationMethod:  status.AuthenticationMethod, AuthenticationProviderID: status.AuthenticationProviderID,
		AuthenticationStrength: string(status.AuthenticationStrength), RecentlyAuthenticated: status.RecentlyAuthenticated,
		Enabled:                     status.Enabled,
		Pending:                     status.Pending,
		PendingExpiresAt:            status.PendingExpiresAt.FormatRFC3339(),
		Method:                      status.Method,
		EnabledAt:                   status.EnabledAt.FormatRFC3339(),
		RecoveryCodesRemaining:      status.RecoveryCodesRemaining,
		RecoveryCodesTotal:          status.RecoveryCodesTotal,
		RecoveryCodesGeneratedAt:    status.RecoveryCodesGeneratedAt.FormatRFC3339(),
		PasswordChangedAt:           status.PasswordChangedAt.FormatRFC3339(),
		RecentAuthenticationMinutes: max(1, int(status.RecentAuthenticationTTL/time.Minute)),
		RecentlyAuthenticatedUntil:  status.RecentlyAuthenticatedUntil.FormatRFC3339(),
	}).withHeaders(noStoreHeaders()), nil
}

func (m mfaResourceModule) setup(request operationRequest) (operationResult, error) {
	setup, err := m.mfa.SetupMFA(
		request.context,
		request.invocation(),
		identity.SetupMFACommand{},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusCreated, mfaSetupResponse{
		Secret:          setup.Secret,
		ProvisioningURI: setup.ProvisioningURI,
		ExpiresAt:       model.FormatRFC3339(setup.ExpiresAt),
	}).withHeaders(noStoreHeaders()), nil
}

func (m mfaResourceModule) activate(request operationRequest) (operationResult, error) {
	code, err := mfaCode(request)
	if err != nil {
		return operationResult{}, err
	}
	activation, err := m.mfa.ActivateMFA(
		request.context,
		request.invocation(),
		identity.ActivateMFACommand{Code: code},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, mfaActivationResponse{
		RecoveryCodes: activation.RecoveryCodes,
	}).withHeaders(noStoreHeaders()), nil
}

func (m mfaResourceModule) challenge(request operationRequest) (operationResult, error) {
	code, err := mfaCode(request)
	if err != nil {
		return operationResult{}, err
	}
	session, err := m.mfa.ChallengeMFA(
		request.context,
		request.invocation(),
		identity.ChallengeMFACommand{Code: code},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, sessionResponseFromModel(request.request, session)).withHeaders(noStoreHeaders()), nil
}

func (m mfaResourceModule) regenerateRecoveryCodes(request operationRequest) (operationResult, error) {
	codes, err := m.mfa.RegenerateMFARecoveryCodes(
		request.context,
		request.invocation(),
		identity.RegenerateMFARecoveryCodesCommand{},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, mfaRecoveryCodesResponse{
		RecoveryCodes: codes,
	}).withHeaders(noStoreHeaders()), nil
}

func (m mfaResourceModule) disable(request operationRequest) (operationResult, error) {
	if err := m.mfa.DisableMFA(
		request.context,
		request.invocation(),
		identity.DisableMFACommand{},
	); err != nil {
		return operationResult{}, err
	}
	return noContentResult(), nil
}

func mfaCode(request operationRequest) (string, error) {
	var input mfaCodeRequest
	if err := request.decodeJSON(&input); err != nil {
		return "", err
	}
	return input.Code, nil
}

type MFA interface {
	GetMFAStatus(context.Context, invocation.Invocation, identity.GetMFAStatusQuery) (*identity.MFAStatus, error)
	SetupMFA(context.Context, invocation.Invocation, identity.SetupMFACommand) (*identity.MFASetup, error)
	ActivateMFA(context.Context, invocation.Invocation, identity.ActivateMFACommand) (*identity.MFAActivation, error)
	ChallengeMFA(context.Context, invocation.Invocation, identity.ChallengeMFACommand) (*model.Session, error)
	RegenerateMFARecoveryCodes(context.Context, invocation.Invocation, identity.RegenerateMFARecoveryCodesCommand) ([]string, error)
	DisableMFA(context.Context, invocation.Invocation, identity.DisableMFACommand) error
}
