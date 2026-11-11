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
	"github.com/sudosylabs/proctor/server/httpapi/route"
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

type mfaHandlers struct {
	app MFA
}

func mfaResource(app MFA) route.Resource {
	h := mfaHandlers{app: app}
	base := "/users/me/mfa"
	return route.NewResource(
		"multi-factor-authentication",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   base,
			Auth:   route.AuthMFARecoverySessionRequired,
			Errors: []string{"authentication.mfa.unavailable", "authentication.reauthentication_required", "authentication.session_required"},
			Handle: h.status,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/mfa/setup",
			Auth:   route.AuthRecentMFARecoverySessionRequired,
			Errors: []string{
				"authentication.mfa.disabled",
				"authentication.mfa.not_found",
				"authentication.mfa.conflict",
				"authentication.mfa.unavailable",
				"authentication.internal",
				"audit.unavailable",
				"authentication.session_required",
			},
			Handle: h.setup,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/mfa/activate",
			Auth:   route.AuthRecentMFARecoverySessionRequired,
			Errors: []string{
				"request.invalid",
				"authentication.mfa.code_invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.mfa.disabled",
				"authentication.mfa.not_found",
				"authentication.mfa.conflict",
				"authentication.mfa.unavailable",
				"authentication.internal",
				"audit.unavailable",
				"authentication.session_required",
			},
			Handle: h.activate,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/mfa/challenge",
			Auth:   route.AuthMFAEnrollmentSessionRequired,
			Errors: []string{
				"request.invalid",
				"authentication.mfa.code_invalid",
				"authentication.rate_limited",
				"authentication.rate_limit_unavailable",
				"authentication.mfa.disabled",
				"authentication.mfa.not_found",
				"authentication.mfa.conflict",
				"authentication.mfa.unavailable",
				"authentication.internal",
				"audit.unavailable",
				"authentication.reauthentication_required",
				"authentication.session_required",
			},
			Handle: h.challenge,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/mfa/recovery-codes/regenerate",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authentication.mfa.disabled",
				"authentication.mfa.not_found",
				"authentication.mfa.conflict",
				"authentication.mfa.unavailable",
				"authentication.internal",
				"audit.unavailable",
				"authentication.session_required",
			},
			Handle: h.regenerateRecoveryCodes,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/mfa/disable",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"mfa.required_by_policy",
				"authentication.mfa.disabled",
				"authentication.mfa.not_found",
				"authentication.mfa.conflict",
				"authentication.mfa.unavailable",
				"audit.unavailable",
				"authentication.session_required",
			},
			Handle: h.disable,
		},
	)
}

func (h mfaHandlers) status(request route.Request) (route.Result, error) {
	status, err := h.app.GetMFAStatus(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, mfaStatusResponse{
		ServiceEnabled:              status.ServiceEnabled,
		MFARecoveryRequired:         status.MFARecoveryRequired,
		MFAEnrollmentRequired:       status.MFAEnrollmentRequired,
		AuthenticationMethod:        status.AuthenticationMethod,
		AuthenticationProviderID:    status.AuthenticationProviderID,
		AuthenticationStrength:      string(status.AuthenticationStrength),
		RecentlyAuthenticated:       status.RecentlyAuthenticated,
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
	}).WithHeaders(noStoreHeaders()), nil
}

func (h mfaHandlers) setup(request route.Request) (route.Result, error) {
	setup, err := h.app.SetupMFA(
		request.Context,
		request.Invocation(),
		identity.SetupMFACommand{},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusCreated, mfaSetupResponse{
		Secret:          setup.Secret,
		ProvisioningURI: setup.ProvisioningURI,
		ExpiresAt:       model.FormatRFC3339(setup.ExpiresAt),
	}).WithHeaders(noStoreHeaders()), nil
}

func (h mfaHandlers) activate(request route.Request) (route.Result, error) {
	code, err := mfaCode(request)
	if err != nil {
		return route.Result{}, err
	}
	activation, err := h.app.ActivateMFA(
		request.Context,
		request.Invocation(),
		identity.ActivateMFACommand{Code: code},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, mfaActivationResponse{
		RecoveryCodes: activation.RecoveryCodes,
	}).WithHeaders(noStoreHeaders()), nil
}

func (h mfaHandlers) challenge(request route.Request) (route.Result, error) {
	code, err := mfaCode(request)
	if err != nil {
		return route.Result{}, err
	}
	session, err := h.app.ChallengeMFA(
		request.Context,
		request.Invocation(),
		identity.ChallengeMFACommand{Code: code},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, sessionResponseFromModel(request.HTTP, session)).WithHeaders(noStoreHeaders()), nil
}

func (h mfaHandlers) regenerateRecoveryCodes(request route.Request) (route.Result, error) {
	codes, err := h.app.RegenerateMFARecoveryCodes(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, mfaRecoveryCodesResponse{
		RecoveryCodes: codes,
	}).WithHeaders(noStoreHeaders()), nil
}

func (h mfaHandlers) disable(request route.Request) (route.Result, error) {
	if err := h.app.DisableMFA(
		request.Context,
		request.Invocation(),
	); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func mfaCode(request route.Request) (string, error) {
	var input mfaCodeRequest
	if err := request.DecodeJSON(&input); err != nil {
		return "", err
	}
	return input.Code, nil
}

// MFA is the application port its handlers call; it manages the caller's multi-factor authentication.
type MFA interface {
	GetMFAStatus(context.Context, invocation.Invocation) (*identity.MFAStatus, error)
	SetupMFA(context.Context, invocation.Invocation, identity.SetupMFACommand) (*identity.MFASetup, error)
	ActivateMFA(context.Context, invocation.Invocation, identity.ActivateMFACommand) (*identity.MFAActivation, error)
	ChallengeMFA(context.Context, invocation.Invocation, identity.ChallengeMFACommand) (*model.Session, error)
	RegenerateMFARecoveryCodes(context.Context, invocation.Invocation) ([]string, error)
	DisableMFA(context.Context, invocation.Invocation) error
}
