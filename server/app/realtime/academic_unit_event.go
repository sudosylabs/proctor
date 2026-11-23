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

// academicUnitChangedData names the unit and its parent, which a viewer of
// the unit already reads; a root unit has no parent.
type academicUnitChangedData struct {
	SchemaVersion        int    `json:"schema_version"`
	AcademicUnitID       string `json:"academic_unit_id"`
	ParentAcademicUnitID string `json:"parent_academic_unit_id,omitempty"`
}

// NewAcademicUnitCreatedEvent tells subscribers to refetch a newly created
// academic unit. The event carries the unit and parent IDs.
func NewAcademicUnitCreatedEvent(unitID, parentID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitCreatedMessage, unitID, parentID)
}

// NewAcademicUnitUpdatedEvent tells academic unit viewers that it changed.
func NewAcademicUnitUpdatedEvent(unitID, parentID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitUpdatedMessage, unitID, parentID)
}

// NewAcademicUnitArchivedEvent tells academic unit viewers that it was
// archived.
func NewAcademicUnitArchivedEvent(unitID, parentID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitArchivedMessage, unitID, parentID)
}

func newAcademicUnitEvent(declared message, unitID, parentID model.AcademicUnitID) (Event, error) {
	if !unitID.IsValid() || (!parentID.IsZero() && !parentID.IsValid()) || parentID == unitID {
		return Event{}, errors.New("Academic Unit event requires a valid Academic Unit and parent identity")
	}
	data, err := json.Marshal(academicUnitChangedData{SchemaVersion: 1, AcademicUnitID: unitID.String(), ParentAcademicUnitID: parentID.String()})
	if err != nil {
		return Event{}, fmt.Errorf("encode Academic Unit event: %w", err)
	}
	return Event{
		Name:     declared.name,
		Action:   model.ActionAcademicUnitView,
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()},
		Data:     data,
	}, nil
}
