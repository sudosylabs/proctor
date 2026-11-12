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
}

// NewOnboardingImportChangedEvent tells the connections subscribed to
// onboarding_batch.view on scope that an onboarding import entered state.
// scope is the Institution, Academic Unit or Class the import is scoped to,
// or one that encloses it: the area publishes one event for each, so a
// subscriber at any scope that can view the import hears of it. Rows, counts
// and file names never travel.
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

// NewInvitationChangedEvent tells the User who issued an Invitation that it
// reached state, such as accepted. Only the inviter's own connections receive
// it; the accepting User and their account never travel.
func NewInvitationChangedEvent(
	inviterID model.UserID,
	invitationID model.InvitationID,
	state model.InvitationState,
) (Event, error) {
	if !inviterID.IsValid() || !invitationID.IsValid() || !state.IsValid() {
		return Event{}, errors.New("Invitation change event requires a valid inviter, Invitation and state")
	}

	data, err := json.Marshal(invitationChangedData{SchemaVersion: 1, InvitationID: invitationID.String(), State: state})
	if err != nil {
		return Event{}, fmt.Errorf("encode Invitation change event: %w", err)
	}

	return Event{Name: invitationChangedMessage.name, UserID: inviterID, Data: data}, nil
}
