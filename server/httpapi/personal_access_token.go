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
			ID: unit.ID.String(), DisplayName: unit.DisplayName,
		})
	}
	for _, scope := range catalogue.Scopes {
		name := string(scope.Action)
		item := personalAccessTokenGrantableScopeResponse{
			Name:            name,
			Title:           translatedRequestText(request, accessActionTitleLocalizationID(scope.Action), name),
			Description:     translatedRequestText(request, accessActionDescriptionLocalizationID(scope.Action), ""),
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

type personalAccessTokenResourceModule struct {
	tokens PersonalAccessTokens
}

func personalAccessTokenResource(tokens PersonalAccessTokens) apiResource {
	module := personalAccessTokenResourceModule{tokens: tokens}
	collection := path("/users/me/tokens")
	item := path("/users/me/tokens/{personal_access_token_id}")
	grantableScopes := path("/users/me/tokens/grantable-scopes")
	return newResource(
		"personal-access-tokens",
		recentSessionRoute(http.MethodPost, collection, []string{"request.invalid", "resource.not_found", "personal_access_token.invalid", "personal_access_token.maximum_reached", "personal_access_token.unavailable", "audit.unavailable", "authentication.session_required"}, module.create),
		sessionRoute(http.MethodGet, collection, []string{"personal_access_token.unavailable", "authentication.reauthentication_required", "authentication.session_required", "request.invalid"}, module.list),
		sessionRoute(http.MethodGet, grantableScopes, []string{"personal_access_token.unavailable", "authentication.reauthentication_required", "authentication.session_required"}, module.grantableScopes),
		sessionRoute(http.MethodPost, path("/users/me/tokens/{personal_access_token_id}/disable"), []string{"request.invalid", "resource.not_found", "personal_access_token.unavailable", "audit.unavailable", "authentication.reauthentication_required", "authentication.session_required", "personal_access_token.invalid"}, module.disable),
		recentSessionRoute(http.MethodPost, path("/users/me/tokens/{personal_access_token_id}/enable"), []string{"request.invalid", "resource.not_found", "personal_access_token.maximum_reached", "personal_access_token.unavailable", "audit.unavailable", "authentication.session_required", "personal_access_token.invalid"}, module.enable),
		sessionRoute(http.MethodDelete, item, []string{"request.invalid", "resource.not_found", "personal_access_token.unavailable", "audit.unavailable", "authentication.reauthentication_required", "authentication.session_required", "personal_access_token.invalid"}, module.revoke),
	)
}

func personalAccessTokenSessionCodes(extra ...string) []string {
	return append([]string(nil), extra...)
}

func personalAccessTokenSessionMutationCodes(extra ...string) []string {
	return personalAccessTokenSessionCodes(extra...)
}

func (m personalAccessTokenResourceModule) disable(request operationRequest) (operationResult, error) {
	return m.setDisabled(request, true)
}

func (m personalAccessTokenResourceModule) enable(request operationRequest) (operationResult, error) {
	return m.setDisabled(request, false)
}

func (m personalAccessTokenResourceModule) setDisabled(request operationRequest, disabled bool) (operationResult, error) {
	tokenID, err := request.params.RequirePersonalAccessTokenID()
	if err != nil {
		return operationResult{}, err
	}
	updated, err := m.tokens.SetPersonalAccessTokenDisabled(
		request.context,
		request.invocation(),
		identity.SetPersonalAccessTokenDisabledCommand{TokenID: tokenID, Disabled: disabled},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, personalAccessTokenResponseFromModel(updated)).withHeaders(noStoreHeaders()), nil
}

func (m personalAccessTokenResourceModule) create(request operationRequest) (operationResult, error) {
	var input createPersonalAccessTokenRequest
	if err := request.decodeJSON(&input); err != nil {
		return operationResult{}, err
	}
	expiresAt, err := wireTimeMillis("expires_at", input.ExpiresAt)
	if err != nil {
		return operationResult{}, err
	}
	created, err := m.tokens.CreatePersonalAccessToken(
		request.context,
		request.invocation(),
		identity.CreatePersonalAccessTokenCommand{
			Description: input.Description, Scopes: input.Scopes,
			AcademicUnitID: model.AcademicUnitID(input.AcademicUnitID), ExpiresAt: expiresAt,
		},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusCreated, personalAccessTokenCreationResponse{
		Token: personalAccessTokenResponseFromModel(created.Token), Credential: created.Credential,
	}).withHeaders(noStoreHeaders()), nil
}

func (m personalAccessTokenResourceModule) list(request operationRequest) (operationResult, error) {
	tokens, err := m.tokens.ListPersonalAccessTokens(
		request.context,
		request.invocation(),
		identity.ListPersonalAccessTokensQuery{},
	)
	if err != nil {
		return operationResult{}, err
	}
	response, err := boundedListResult(request, "personal access token", tokens,
		func(token *model.PersonalAccessToken) boundedPageKey {
			return boundedPageKey{At: token.CreatedAt, ID: token.ID.String()}
		}, personalAccessTokenResponseFromModel)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, response).withHeaders(noStoreHeaders()), nil
}

func (m personalAccessTokenResourceModule) grantableScopes(request operationRequest) (operationResult, error) {
	catalogue, err := m.tokens.ListGrantablePersonalAccessTokenScopes(
		request.context,
		request.invocation(),
		identity.ListGrantablePersonalAccessTokenScopesQuery{},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, personalAccessTokenGrantableScopesResponseFromApp(request.request, catalogue)).
		withHeaders(noStoreHeaders()), nil
}

func (m personalAccessTokenResourceModule) revoke(request operationRequest) (operationResult, error) {
	tokenID, err := request.params.RequirePersonalAccessTokenID()
	if err != nil {
		return operationResult{}, err
	}
	if _, err := m.tokens.RevokePersonalAccessToken(
		request.context,
		request.invocation(),
		identity.RevokePersonalAccessTokenCommand{TokenID: tokenID},
	); err != nil {
		return operationResult{}, err
	}
	return noContentResult(), nil
}

func noStoreHeaders() http.Header {
	return http.Header{"Cache-Control": []string{"no-store"}}
}

type PersonalAccessTokens interface {
	CreatePersonalAccessToken(context.Context, invocation.Invocation, identity.CreatePersonalAccessTokenCommand) (*model.PersonalAccessTokenCreation, error)
	ListPersonalAccessTokens(context.Context, invocation.Invocation, identity.ListPersonalAccessTokensQuery) ([]*model.PersonalAccessToken, error)
	ListGrantablePersonalAccessTokenScopes(context.Context, invocation.Invocation, identity.ListGrantablePersonalAccessTokenScopesQuery) (*identity.PersonalAccessTokenGrantableScopes, error)
	RevokePersonalAccessToken(context.Context, invocation.Invocation, identity.RevokePersonalAccessTokenCommand) (*model.PersonalAccessToken, error)
	SetPersonalAccessTokenDisabled(context.Context, invocation.Invocation, identity.SetPersonalAccessTokenDisabledCommand) (*model.PersonalAccessToken, error)
}
