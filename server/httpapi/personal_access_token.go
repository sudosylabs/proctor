// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/api4/user.go access-token handlers.
// The raw credential is returned exactly once and every management route
// remains interactive-session-only.

package httpapi

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type createPersonalAccessTokenRequest struct {
	Description    string   `json:"description"`
	Scopes         []string `json:"scopes"`
	AcademicUnitID string   `json:"academic_unit_id,omitempty"`
	ExpiresAt      string   `json:"expires_at"`
}

type personalAccessTokenResponse struct {
	ID             string   `json:"id"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	ArchivedAt     string   `json:"archived_at,omitempty"`
	UserID         string   `json:"user_id"`
	Description    string   `json:"description"`
	Scopes         []string `json:"scopes"`
	AcademicUnitID string   `json:"academic_unit_id,omitempty"`
	ExpiresAt      string   `json:"expires_at"`
	LastUsedAt     string   `json:"last_used_at,omitempty"`
	DisabledAt     string   `json:"disabled_at,omitempty"`
	RevokedAt      string   `json:"revoked_at,omitempty"`
}

type personalAccessTokenCreationResponse struct {
	Token      personalAccessTokenResponse `json:"token"`
	Credential string                      `json:"credential"`
}

func personalAccessTokenResponseFromModel(token *model.PersonalAccessToken) personalAccessTokenResponse {
	if token == nil {
		return personalAccessTokenResponse{}
	}
	scopes := token.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return personalAccessTokenResponse{
		ID:             token.ID.String(),
		CreatedAt:      model.FormatRFC3339(token.CreatedAt),
		UpdatedAt:      model.FormatRFC3339(token.UpdatedAt),
		ArchivedAt:     token.ArchivedAt.FormatRFC3339(),
		UserID:         token.UserID.String(),
		Description:    token.Description,
		Scopes:         append([]string(nil), scopes...),
		AcademicUnitID: token.AcademicUnitID.String(),
		ExpiresAt:      model.FormatRFC3339(token.ExpiresAt),
		LastUsedAt:     token.LastUsedAt.FormatRFC3339(),
		DisabledAt:     token.DisabledAt.FormatRFC3339(),
		RevokedAt:      token.RevokedAt.FormatRFC3339(),
	}
}

type personalAccessTokenGrantableScopesResponse struct {
	Scopes               []personalAccessTokenGrantableScopeResponse `json:"scopes"`
	AcademicUnits        []personalAccessTokenAcademicUnitResponse   `json:"academic_units"`
	AcademicUnitsHasMore bool                                        `json:"academic_units_has_more"`
}

type personalAccessTokenGrantableScopeResponse struct {
	Name            string   `json:"name"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	ResourceTypes   []string `json:"resource_types"`
	InstitutionWide bool     `json:"institution_wide"`
	AcademicUnitIDs []string `json:"academic_unit_ids"`
}

type personalAccessTokenAcademicUnitResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func personalAccessTokenGrantableScopesResponseFromApp(
	request *http.Request,
	catalogue *identity.PersonalAccessTokenGrantableScopes,
) personalAccessTokenGrantableScopesResponse {
	response := personalAccessTokenGrantableScopesResponse{
		Scopes:        []personalAccessTokenGrantableScopeResponse{},
		AcademicUnits: []personalAccessTokenAcademicUnitResponse{},
	}
	if catalogue == nil {
		return response
	}

	response.AcademicUnitsHasMore = catalogue.AcademicUnitsHasMore
	for _, unit := range catalogue.AcademicUnits {
		response.AcademicUnits = append(response.AcademicUnits, personalAccessTokenAcademicUnitResponse{
			ID:          unit.ID.String(),
			DisplayName: unit.DisplayName,
		})
	}

	for _, scope := range catalogue.Scopes {
		name := string(scope.Action)
		item := personalAccessTokenGrantableScopeResponse{
			Name:            name,
			Title:           route.Translate(request, accessActionTitleLocalizationID(scope.Action), name),
			Description:     route.Translate(request, accessActionDescriptionLocalizationID(scope.Action), ""),
			ResourceTypes:   make([]string, 0, len(scope.ResourceTypes)),
			InstitutionWide: scope.InstitutionWide,
			AcademicUnitIDs: make([]string, 0, len(scope.AcademicUnitIDs)),
		}
		for _, resourceType := range scope.ResourceTypes {
			item.ResourceTypes = append(item.ResourceTypes, string(resourceType))
		}

		for _, id := range scope.AcademicUnitIDs {
			item.AcademicUnitIDs = append(item.AcademicUnitIDs, id.String())
		}

		response.Scopes = append(response.Scopes, item)
	}

	return response
}

type personalAccessTokenHandlers struct {
	app PersonalAccessTokens
}

func personalAccessTokenResource(app PersonalAccessTokens) route.Resource {
	h := personalAccessTokenHandlers{app: app}
	collection := "/users/me/tokens"
	item := "/users/me/tokens/{personal_access_token_id}"
	grantableScopes := "/users/me/tokens/grantable-scopes"
	return route.NewResource(
		"personal-access-tokens",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   collection,
			Auth:   route.AuthRecentSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"personal_access_token.invalid",
				"personal_access_token.maximum_reached",
				"personal_access_token.unavailable",
				"audit.unavailable",
				"authentication.session_required",
			},
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"personal_access_token.unavailable",
				"authentication.reauthentication_required",
				"authentication.session_required",
				"request.invalid",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   grantableScopes,
			Auth:   route.AuthSessionRequired,
			Errors: []string{"personal_access_token.unavailable", "authentication.reauthentication_required", "authentication.session_required"},
			Handle: h.grantableScopes,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/tokens/{personal_access_token_id}/disable",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"personal_access_token.unavailable",
				"audit.unavailable",
				"authentication.reauthentication_required",
				"authentication.session_required",
				"personal_access_token.invalid",
			},
			Handle: h.disable,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/users/me/tokens/{personal_access_token_id}/enable",
			Auth:   route.AuthRecentSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"personal_access_token.maximum_reached",
				"personal_access_token.unavailable",
				"audit.unavailable",
				"authentication.session_required",
				"personal_access_token.invalid",
			},
			Handle: h.enable,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   item,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"personal_access_token.unavailable",
				"audit.unavailable",
				"authentication.reauthentication_required",
				"authentication.session_required",
				"personal_access_token.invalid",
			},
			Handle: h.revoke,
		},
	)
}

func personalAccessTokenSessionCodes(extra ...string) []string {
	return append([]string(nil), extra...)
}

func personalAccessTokenSessionMutationCodes(extra ...string) []string {
	return personalAccessTokenSessionCodes(extra...)
}

func (h personalAccessTokenHandlers) disable(request route.Request) (route.Result, error) {
	return h.setDisabled(request, true)
}

func (h personalAccessTokenHandlers) enable(request route.Request) (route.Result, error) {
	return h.setDisabled(request, false)
}

func (h personalAccessTokenHandlers) setDisabled(request route.Request, disabled bool) (route.Result, error) {
	tokenID, err := route.PathID[model.PersonalAccessTokenID](request, "personal_access_token_id")
	if err != nil {
		return route.Result{}, err
	}
	updated, err := h.app.SetPersonalAccessTokenDisabled(
		request.Context,
		request.Invocation(),
		identity.SetPersonalAccessTokenDisabledCommand{TokenID: tokenID, Disabled: disabled},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, personalAccessTokenResponseFromModel(updated)).WithHeaders(noStoreHeaders()), nil
}

func (h personalAccessTokenHandlers) create(request route.Request) (route.Result, error) {
	var input createPersonalAccessTokenRequest
	if err := request.DecodeJSON(&input); err != nil {
		return route.Result{}, err
	}

	expiresAt, err := route.WireTimeMillis("expires_at", input.ExpiresAt)
	if err != nil {
		return route.Result{}, err
	}

	created, err := h.app.CreatePersonalAccessToken(
		request.Context,
		request.Invocation(),
		identity.CreatePersonalAccessTokenCommand{
			Description:    input.Description,
			Scopes:         input.Scopes,
			AcademicUnitID: model.AcademicUnitID(input.AcademicUnitID),
			ExpiresAt:      expiresAt,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, personalAccessTokenCreationResponse{
		Token:      personalAccessTokenResponseFromModel(created.Token),
		Credential: created.Credential,
	}).WithHeaders(noStoreHeaders()), nil
}

func (h personalAccessTokenHandlers) list(request route.Request) (route.Result, error) {
	tokens, err := h.app.ListPersonalAccessTokens(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.BoundedList(
		request,
		"personal access token",
		tokens,
		func(token *model.PersonalAccessToken) route.BoundedKey {
			return route.BoundedKey{At: token.CreatedAt, ID: token.ID.String()}
		},
		personalAccessTokenResponseFromModel,
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h personalAccessTokenHandlers) grantableScopes(request route.Request) (route.Result, error) {
	catalogue, err := h.app.ListGrantablePersonalAccessTokenScopes(
		request.Context,
		request.Invocation(),
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, personalAccessTokenGrantableScopesResponseFromApp(request.HTTP, catalogue)).
		WithHeaders(noStoreHeaders()), nil
}

func (h personalAccessTokenHandlers) revoke(request route.Request) (route.Result, error) {
	tokenID, err := route.PathID[model.PersonalAccessTokenID](request, "personal_access_token_id")
	if err != nil {
		return route.Result{}, err
	}
	if _, err := h.app.RevokePersonalAccessToken(
		request.Context,
		request.Invocation(),
		identity.RevokePersonalAccessTokenCommand{TokenID: tokenID},
	); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func noStoreHeaders() http.Header {
	return http.Header{"Cache-Control": []string{"no-store"}}
}

// PersonalAccessTokens is the application port its handlers call; it creates, lists, revokes and disables personal access tokens.
type PersonalAccessTokens interface {
	CreatePersonalAccessToken(
		context.Context,
		invocation.Invocation,
		identity.CreatePersonalAccessTokenCommand,
	) (*model.PersonalAccessTokenCreation, error)
	ListPersonalAccessTokens(
		context.Context,
		invocation.Invocation,
	) ([]*model.PersonalAccessToken, error)
	ListGrantablePersonalAccessTokenScopes(
		context.Context,
		invocation.Invocation,
	) (*identity.PersonalAccessTokenGrantableScopes, error)
	RevokePersonalAccessToken(
		context.Context,
		invocation.Invocation,
		identity.RevokePersonalAccessTokenCommand,
	) (*model.PersonalAccessToken, error)
	SetPersonalAccessTokenDisabled(
		context.Context,
		invocation.Invocation,
		identity.SetPersonalAccessTokenDisabledCommand,
	) (*model.PersonalAccessToken, error)
}
