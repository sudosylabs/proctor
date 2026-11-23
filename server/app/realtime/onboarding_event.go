// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
)

type onboardingImportChangedData struct {
	SchemaVersion int                         `json:"schema_version"`
	ImportID      string                      `json:"import_id"`
	State         model.OnboardingImportState `json:"state"`
}

type invitationChangedData struct {
	SchemaVersion int                   `json:"schema_version"`
	InvitationID  string                `json:"invitation_id"`
	State         model.InvitationState `json:"state"`
	ScopeType     model.RoleScopeType   `json:"scope_type"`
	ScopeID       string                `json:"scope_id"`
}

// NewOnboardingImportChangedEvent is published once for the import's scope
// and once for each enclosing scope, so a viewer at any of them hears of it.
// Rows, counts and file names never travel.
func NewOnboardingImportChangedEvent(
	importID model.OnboardingImportID,
	state model.OnboardingImportState,
	scope model.Resource,
) (Event, error) {
	if !importID.IsValid() || !state.IsValid() || !model.IsValidOpaqueID(scope.ID) {
		return Event{}, errors.New("onboarding import change event requires a valid import, state and scope")
	}
	switch scope.Type {
	case model.ResourceInstitution, model.ResourceAcademicUnit, model.ResourceClass:
	default:
		return Event{}, errors.New("onboarding import change event scope must be an Institution, Academic Unit or Class")
	}

	data, err := json.Marshal(onboardingImportChangedData{SchemaVersion: 1, ImportID: importID.String(), State: state})
	if err != nil {
		return Event{}, fmt.Errorf("encode onboarding import change event: %w", err)
	}

	return Event{
		Name:     onboardingImportChangedMessage.name,
		Action:   model.ActionOnboardingBatchView,
		Resource: scope,
		Data:     data,
	}, nil
}

// NewCreatorOnboardingImportChangedEvent goes to the creator alone, for
// imports only they may read, so other viewers never learn they exist.
func NewCreatorOnboardingImportChangedEvent(
	creatorID model.UserID,
	importID model.OnboardingImportID,
	state model.OnboardingImportState,
) (Event, error) {
	if !creatorID.IsValid() || !importID.IsValid() || !state.IsValid() {
		return Event{}, errors.New("onboarding import change event requires a valid creator, import and state")
	}

	data, err := json.Marshal(onboardingImportChangedData{SchemaVersion: 1, ImportID: importID.String(), State: state})
	if err != nil {
		return Event{}, fmt.Errorf("encode onboarding import change event: %w", err)
	}

	return Event{Name: creatorOnboardingImportChangedMessage.name, UserID: creatorID, Data: data}, nil
}

// NewInvitationChangedEvent tells subscribers to refetch invitations visible
// in audience. It names the grant's scope without disclosing the accepting
// user or account.
func NewInvitationChangedEvent(
	invitationID model.InvitationID,
	state model.InvitationState,
	scopeType model.RoleScopeType,
	scopeID string,
	audience model.Resource,
) (Event, error) {
	if !invitationID.IsValid() || !state.IsValid() ||
		!scopeType.IsValid() || !model.IsValidOpaqueID(scopeID) {
		return Event{}, errors.New("Invitation change event requires a valid Invitation, state and scope")
	}
	definition, _ := model.DefinitionForAction(model.ActionInvitationView)
	if audience.Validate() != nil || !definition.AcceptsResource(audience.Type) {
		return Event{}, errors.New("Invitation change event requires a valid audience")
	}

	return scopedEvent(invitationChangedMessage, model.ActionInvitationView, audience, invitationChangedData{
		SchemaVersion: 1, InvitationID: invitationID.String(), State: state, ScopeType: scopeType, ScopeID: scopeID,
	})
}
