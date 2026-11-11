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

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// MFAKeyApplication is the operator surface of the MFA key ring.
type MFAKeyApplication interface {
	GetMFAKeyState(context.Context, invocation.Invocation) (identity.MFAKeyStateView, error)
	StartMFARekey(context.Context, invocation.Invocation, identity.StartMFARekeyCommand) (identity.MFARekeyView, error)
	GetMFARekeyStatus(context.Context, invocation.Invocation, model.JobID) (identity.MFARekeyStatusView, error)
}

type mfaRekeyRequest struct {
	RetiringKeyID string `json:"retiring_key_id"`
}

type mfaRekeyResponse struct {
	JobID         string `json:"job_id"`
	PrimaryKeyID  string `json:"primary_key_id"`
	RetiringKeyID string `json:"retiring_key_id"`
	CreatedAt     string `json:"created_at"`
}

type mfaRekeyProgressResponse struct {
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Stage   string `json:"stage"`
}

type mfaRekeyProofResponse struct {
	NonPrimaryReferences int64 `json:"non_primary_references"`
	RetiringReferences   int64 `json:"retiring_references"`
	RetirementSafe       bool  `json:"retirement_safe"`
}

type mfaRekeyStatusResponse struct {
	JobID           string                    `json:"job_id"`
	Status          model.JobStatus           `json:"status"`
	PrimaryKeyID    string                    `json:"primary_key_id"`
	RetiringKeyID   string                    `json:"retiring_key_id"`
	CreatedAt       string                    `json:"created_at"`
	UpdatedAt       string                    `json:"updated_at"`
	CompletedAt     string                    `json:"completed_at,omitempty"`
	PublicErrorCode string                    `json:"public_error_code,omitempty"`
	AttemptCount    int                       `json:"attempt_count"`
	MaximumAttempts int                       `json:"maximum_attempts"`
	Processed       int64                     `json:"processed"`
	Resealed        int64                     `json:"resealed"`
	Progress        *mfaRekeyProgressResponse `json:"progress,omitempty"`
	Proof           *mfaRekeyProofResponse    `json:"proof,omitempty"`
}

type mfaSecretKeyUsageResponse struct {
	KeyID       string `json:"key_id"`
	Credentials int64  `json:"credentials"`
	Configured  bool   `json:"configured"`
}

type mfaKeyStateResponse struct {
	PrimaryKeyID         string                      `json:"primary_key_id"`
	RequiredPrimaryKeyID string                      `json:"required_primary_key_id,omitempty"`
	ActiveRekeyJobID     string                      `json:"active_rekey_job_id,omitempty"`
	Active               []mfaSecretKeyUsageResponse `json:"active"`
}

type mfaKeyHandlers struct{ app MFAKeyApplication }

func mfaKeyResource(app MFAKeyApplication) route.Resource {
	h := mfaKeyHandlers{app: app}
	return route.NewResource("mfa-keys",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/mfa/keys",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"authentication.mfa.unavailable",
				"resource.not_found",
			},
			Handle: h.getKeyState,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/mfa/rekey",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"authentication.mfa.rekey.invalid",
				"authentication.mfa.rekey.conflict",
				"authentication.mfa.unavailable",
				"resource.not_found",
			},
			Handle: h.startRekey,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/mfa/rekey/{job_id}",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: operatorReadErrorCodes("request.invalid", "resource.not_found", "authentication.mfa.unavailable"),
			Handle: h.getRekeyStatus,
		},
	)
}

func (h mfaKeyHandlers) getKeyState(request route.Request) (route.Result, error) {
	view, err := h.app.GetMFAKeyState(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}

	response := mfaKeyStateResponse{
		PrimaryKeyID:         view.PrimaryKeyID,
		RequiredPrimaryKeyID: view.RequiredPrimaryKeyID,
		ActiveRekeyJobID:     view.ActiveRekeyJobID.String(),
		Active:               make([]mfaSecretKeyUsageResponse, 0, len(view.Active)),
	}
	for _, usage := range view.Active {
		response.Active = append(response.Active, mfaSecretKeyUsageResponse{
			KeyID:       usage.KeyID,
			Credentials: usage.Credentials,
			Configured:  usage.Configured,
		})
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h mfaKeyHandlers) startRekey(request route.Request) (route.Result, error) {
	var body mfaRekeyRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	view, err := h.app.StartMFARekey(
		request.Context,
		request.Invocation(),
		identity.StartMFARekeyCommand{RetiringKeyID: body.RetiringKeyID},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusAccepted, mfaRekeyResponse{
		JobID:         view.JobID.String(),
		PrimaryKeyID:  view.PrimaryKeyID,
		RetiringKeyID: view.RetiringKeyID,
		CreatedAt:     model.FormatRFC3339(view.CreatedAt),
	}), nil
}

func (h mfaKeyHandlers) getRekeyStatus(request route.Request) (route.Result, error) {
	jobID, err := route.PathID[model.JobID](request, "job_id")
	if err != nil {
		return route.Result{}, err
	}

	view, err := h.app.GetMFARekeyStatus(request.Context, request.Invocation(), jobID)
	if err != nil {
		return route.Result{}, err
	}

	response := mfaRekeyStatusResponse{
		JobID:           view.JobID.String(),
		Status:          view.Status,
		PrimaryKeyID:    view.PrimaryKeyID,
		RetiringKeyID:   view.RetiringKeyID,
		CreatedAt:       model.FormatRFC3339(view.CreatedAt),
		UpdatedAt:       model.FormatRFC3339(view.UpdatedAt),
		PublicErrorCode: view.PublicErrorCode,
		AttemptCount:    view.AttemptCount,
		MaximumAttempts: view.MaximumAttempts,
		Processed:       view.Processed,
		Resealed:        view.Resealed,
	}
	if view.CompletedAt.Valid {
		response.CompletedAt = view.CompletedAt.FormatRFC3339()
	}
	if view.Progress != nil {
		response.Progress = &mfaRekeyProgressResponse{Current: view.Progress.Current, Total: view.Progress.Total, Stage: view.Progress.Stage}
	}

	if view.Proof != nil {
		response.Proof = &mfaRekeyProofResponse{
			NonPrimaryReferences: view.Proof.NonPrimaryReferences,
			RetiringReferences:   view.Proof.RetiringReferences,
			RetirementSafe:       view.Proof.RetirementSafe,
		}
	}

	return route.JSON(http.StatusOK, response), nil
}
