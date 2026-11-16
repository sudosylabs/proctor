// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"errors"

	"github.com/sudosylabs/proctor/server/model"
)

// NewAcademicUnitCreatedEvent tells everyone who may view the Academic Unit
// that it was created. Like its update and archive siblings it carries no
// payload: subscribers refetch the Academic Unit.
func NewAcademicUnitCreatedEvent(unitID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitCreatedMessage, unitID)
}

// NewAcademicUnitUpdatedEvent tells everyone who may view the Academic Unit
// that it changed.
func NewAcademicUnitUpdatedEvent(unitID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitUpdatedMessage, unitID)
}

// NewAcademicUnitArchivedEvent tells everyone who may view the Academic Unit
// that it was archived.
func NewAcademicUnitArchivedEvent(unitID model.AcademicUnitID) (Event, error) {
	return newAcademicUnitEvent(academicUnitArchivedMessage, unitID)
}

func newAcademicUnitEvent(declared message, unitID model.AcademicUnitID) (Event, error) {
	if !unitID.IsValid() {
		return Event{}, errors.New("Academic Unit event requires a valid Academic Unit identity")
	}
	return Event{
		Name:     declared.name,
		Action:   model.ActionAcademicUnitView,
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()},
	}, nil
}
