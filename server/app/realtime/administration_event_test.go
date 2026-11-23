// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// TestAdministrationEventsCarryOnlyIDsToTheirReaders pins each administration
// event's subscription target and that its data names records by ID only.
func TestAdministrationEventsCarryOnlyIDsToTheirReaders(t *testing.T) {
	t.Parallel()
	institutionID, unitID, userID := model.NewInstitutionID(), model.NewAcademicUnitID(), model.NewUserID()
	classID, periodID, roleID := model.NewClassID(), model.NewAcademicPeriodID(), model.NewRoleID()
	holdID, examID, deliveryID := model.NewRetentionHoldID(), model.NewExamID(), model.NewMailDeliveryID()
	binding := &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: userID, RoleID: roleID,
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}
	institution := model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()}
	build := func(event Event, err error) Event {
		t.Helper()
		require.NoError(t, err)
		require.NoError(t, event.ValidateForPublish())
		require.Empty(t, event.UserID, "an administration event goes to its readers, not one User")
		return event
	}
	for name, test := range map[string]struct {
		event    Event
		action   model.Action
		resource model.Resource
		data     string
	}{
		"user": {build(NewUserChangedEvent(userID)), model.ActionUserView,
			model.Resource{Type: model.ResourceUser, ID: userID.String()},
			`{"schema_version":1,"user_id":"` + userID.String() + `"}`},
		"class": {build(NewClassChangedEvent(classID, unitID)), model.ActionClassView,
			model.Resource{Type: model.ResourceClass, ID: classID.String()},
			`{"schema_version":1,"class_id":"` + classID.String() + `","academic_unit_id":"` + unitID.String() + `"}`},
		"unit period": {build(NewAcademicPeriodChangedEvent(periodID, model.NewAcademicUnitAcademicPeriodOwner(unitID))),
			model.ActionAcademicPeriodView, model.Resource{Type: model.ResourceAcademicPeriod, ID: periodID.String()},
			`{"schema_version":1,"academic_period_id":"` + periodID.String() + `","academic_unit_id":"` + unitID.String() + `"}`},
		"institution period": {build(NewAcademicPeriodChangedEvent(periodID, model.NewInstitutionAcademicPeriodOwner(institutionID))),
			model.ActionAcademicPeriodView, model.Resource{Type: model.ResourceAcademicPeriod, ID: periodID.String()},
			`{"schema_version":1,"academic_period_id":"` + periodID.String() + `"}`},
		"role": {build(NewRoleChangedEvent(institutionID, roleID)), model.ActionRoleView, institution,
			`{"schema_version":1,"role_id":"` + roleID.String() + `"}`},
		"role binding": {build(NewRoleBindingChangedEvent(binding)), model.ActionRoleBindingView,
			model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()},
			`{"schema_version":1,"role_binding_id":"` + binding.ID.String() + `","user_id":"` + userID.String() +
				`","role_id":"` + roleID.String() + `","scope_type":"academic_unit","scope_id":"` + unitID.String() + `"}`},
		"retention policy": {build(NewRetentionPolicyChangedEvent(institutionID)), model.ActionRetentionPolicyView, institution,
			`{"schema_version":1,"kind":"policy"}`},
		"retention hold": {build(NewRetentionHoldChangedEvent(institutionID, holdID, examID)), model.ActionRetentionPolicyView, institution,
			`{"schema_version":1,"kind":"hold","retention_hold_id":"` + holdID.String() + `","exam_id":"` + examID.String() + `"}`},
		"retention records": {build(NewRetentionRecordsChangedEvent(institutionID, examID)), model.ActionRetentionPolicyView, institution,
			`{"schema_version":1,"kind":"records","exam_id":"` + examID.String() + `"}`},
		"mail delivery": {build(NewMailDeliveryChangedEvent(institutionID, deliveryID)), model.ActionMailView, institution,
			`{"schema_version":1,"mail_delivery_id":"` + deliveryID.String() + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.action, test.event.Action)
			require.Equal(t, test.resource, test.event.Resource)
			require.JSONEq(t, test.data, string(test.event.Data))
		})
	}
}

func TestAdministrationEventsRefuseInvalidIdentities(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func() (Event, error){
		"user":       func() (Event, error) { return NewUserChangedEvent("") },
		"class unit": func() (Event, error) { return NewClassChangedEvent(model.NewClassID(), "") },
		"period owner": func() (Event, error) {
			return NewAcademicPeriodChangedEvent(model.NewAcademicPeriodID(), model.AcademicPeriodOwner{})
		},
		"role": func() (Event, error) { return NewRoleChangedEvent(model.NewInstitutionID(), "") },
		"role binding": func() (Event, error) {
			return NewRoleBindingChangedEvent(&model.RoleBinding{ID: model.NewRoleBindingID()})
		},
		"hold exam": func() (Event, error) {
			return NewRetentionHoldChangedEvent(model.NewInstitutionID(), model.NewRetentionHoldID(), "")
		},
		"mail institution": func() (Event, error) { return NewMailDeliveryChangedEvent("", model.NewMailDeliveryID()) },
	} {
		_, err := build()
		require.Error(t, err, name)
	}
}
