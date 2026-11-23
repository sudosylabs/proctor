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

// The administration events are refetch hints for the people and structure
// boards. Each carries the changed record's ID and the ID of what holds it,
// both of which its subscribers already read; names, reasons and counts stay
// behind the authorized HTTP reads.

type userChangedData struct {
	SchemaVersion int    `json:"schema_version"`
	UserID        string `json:"user_id"`
}

type classChangedData struct {
	SchemaVersion  int    `json:"schema_version"`
	ClassID        string `json:"class_id"`
	AcademicUnitID string `json:"academic_unit_id"`
}

type academicPeriodChangedData struct {
	SchemaVersion    int    `json:"schema_version"`
	AcademicPeriodID string `json:"academic_period_id"`
	// AcademicUnitID is the owning unit; an Institution period has none.
	AcademicUnitID string `json:"academic_unit_id,omitempty"`
}

type roleChangedData struct {
	SchemaVersion int    `json:"schema_version"`
	RoleID        string `json:"role_id"`
}

type roleBindingChangedData struct {
	SchemaVersion int                 `json:"schema_version"`
	RoleBindingID string              `json:"role_binding_id"`
	UserID        string              `json:"user_id"`
	RoleID        string              `json:"role_id"`
	ScopeType     model.RoleScopeType `json:"scope_type"`
	ScopeID       string              `json:"scope_id"`
}

type retentionChangedData struct {
	SchemaVersion   int                 `json:"schema_version"`
	Kind            RetentionChangeKind `json:"kind"`
	RetentionHoldID string              `json:"retention_hold_id,omitempty"`
	ExamID          string              `json:"exam_id,omitempty"`
}

type mailDeliveryChangedData struct {
	SchemaVersion  int    `json:"schema_version"`
	MailDeliveryID string `json:"mail_delivery_id"`
}

// NewUserChangedEvent tells the User's viewers to refetch them: their
// profile, account state, affiliations, two-step state or memberships moved.
func NewUserChangedEvent(userID model.UserID) (Event, error) {
	if !userID.IsValid() {
		return Event{}, errors.New("User change event requires a valid User")
	}
	return scopedEvent(userChangedMessage, model.ActionUserView,
		model.Resource{Type: model.ResourceUser, ID: userID.String()},
		userChangedData{SchemaVersion: 1, UserID: userID.String()})
}

// NewClassChangedEvent tells the Class's viewers to refetch it: the Class, its
// members or its teachers changed. unitID is the unit that owns the Class
// through its Programme.
func NewClassChangedEvent(classID model.ClassID, unitID model.AcademicUnitID) (Event, error) {
	if !classID.IsValid() || !unitID.IsValid() {
		return Event{}, errors.New("Class change event requires a valid Class and Academic Unit")
	}
	return scopedEvent(classChangedMessage, model.ActionClassView,
		model.Resource{Type: model.ResourceClass, ID: classID.String()},
		classChangedData{SchemaVersion: 1, ClassID: classID.String(), AcademicUnitID: unitID.String()})
}

// NewAcademicPeriodChangedEvent tells the period's viewers, who view it
// through its owner, that it was created, changed or archived.
func NewAcademicPeriodChangedEvent(periodID model.AcademicPeriodID, owner model.AcademicPeriodOwner) (Event, error) {
	if !periodID.IsValid() || owner.Validate() != nil {
		return Event{}, errors.New("Academic Period change event requires a valid period and owner")
	}
	return scopedEvent(academicPeriodChangedMessage, model.ActionAcademicPeriodView,
		model.Resource{Type: model.ResourceAcademicPeriod, ID: periodID.String()},
		academicPeriodChangedData{SchemaVersion: 1, AcademicPeriodID: periodID.String(), AcademicUnitID: owner.AcademicUnitID.String()})
}

// NewRoleChangedEvent tells role.view holders that a Role or its permissions
// changed.
func NewRoleChangedEvent(institutionID model.InstitutionID, roleID model.RoleID) (Event, error) {
	if !institutionID.IsValid() || !roleID.IsValid() {
		return Event{}, errors.New("Role change event requires a valid Institution and Role")
	}
	return scopedEvent(roleChangedMessage, model.ActionRoleView,
		model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		roleChangedData{SchemaVersion: 1, RoleID: roleID.String()})
}

// NewRoleBindingChangedEvent tells role_binding.view holders at the binding's
// scope that it was granted, ended or archived. The holder hears of it
// through current_user.context.changed instead.
func NewRoleBindingChangedEvent(binding *model.RoleBinding) (Event, error) {
	if binding == nil || !binding.ID.IsValid() || !binding.UserID.IsValid() || !binding.RoleID.IsValid() ||
		!binding.ScopeType.IsValid() || !model.IsValidOpaqueID(binding.ScopeID) {
		return Event{}, errors.New("Role Binding change event requires a valid Role Binding")
	}
	var resourceType model.ResourceType
	switch binding.ScopeType {
	case model.RoleScopeInstitution:
		resourceType = model.ResourceInstitution
	case model.RoleScopeAcademicUnit:
		resourceType = model.ResourceAcademicUnit
	case model.RoleScopeClass:
		resourceType = model.ResourceClass
	}
	return scopedEvent(roleBindingChangedMessage, model.ActionRoleBindingView,
		model.Resource{Type: resourceType, ID: binding.ScopeID},
		roleBindingChangedData{
			SchemaVersion: 1, RoleBindingID: binding.ID.String(), UserID: binding.UserID.String(),
			RoleID: binding.RoleID.String(), ScopeType: binding.ScopeType, ScopeID: binding.ScopeID,
		})
}

// RetentionChangeKind says which retention read to refetch.
type RetentionChangeKind string

// Retention change kinds identify the policy, hold, or records that changed.
const (
	RetentionChangePolicy  RetentionChangeKind = "policy"
	RetentionChangeHold    RetentionChangeKind = "hold"
	RetentionChangeRecords RetentionChangeKind = "records"
)

// NewRetentionPolicyChangedEvent tells retention viewers the policy changed.
func NewRetentionPolicyChangedEvent(institutionID model.InstitutionID) (Event, error) {
	return newRetentionChangedEvent(institutionID, retentionChangedData{Kind: RetentionChangePolicy})
}

// NewRetentionHoldChangedEvent tells retention viewers a hold on an Exam was
// placed or released.
func NewRetentionHoldChangedEvent(institutionID model.InstitutionID, holdID model.RetentionHoldID, examID model.ExamID) (Event, error) {
	if !holdID.IsValid() || !examID.IsValid() {
		return Event{}, errors.New("retention hold change event requires a valid hold and Exam")
	}
	return newRetentionChangedEvent(institutionID, retentionChangedData{
		Kind: RetentionChangeHold, RetentionHoldID: holdID.String(), ExamID: examID.String(),
	})
}

// NewRetentionRecordsChangedEvent tells retention viewers an Exam's records
// due for removal changed.
func NewRetentionRecordsChangedEvent(institutionID model.InstitutionID, examID model.ExamID) (Event, error) {
	if !examID.IsValid() {
		return Event{}, errors.New("retention records change event requires a valid Exam")
	}
	return newRetentionChangedEvent(institutionID, retentionChangedData{Kind: RetentionChangeRecords, ExamID: examID.String()})
}

func newRetentionChangedEvent(institutionID model.InstitutionID, data retentionChangedData) (Event, error) {
	if !institutionID.IsValid() {
		return Event{}, errors.New("retention change event requires a valid Institution")
	}
	data.SchemaVersion = 1
	return scopedEvent(retentionChangedMessage, model.ActionRetentionPolicyView,
		model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()}, data)
}

// NewMailDeliveryChangedEvent tells Institution mail.view holders a
// delivery's state changed. The recipient and the message never travel.
func NewMailDeliveryChangedEvent(institutionID model.InstitutionID, deliveryID model.MailDeliveryID) (Event, error) {
	if !institutionID.IsValid() || !deliveryID.IsValid() {
		return Event{}, errors.New("mail delivery change event requires a valid Institution and delivery")
	}
	return scopedEvent(mailDeliveryChangedMessage, model.ActionMailView,
		model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		mailDeliveryChangedData{SchemaVersion: 1, MailDeliveryID: deliveryID.String()})
}

func scopedEvent(declared message, action model.Action, resource model.Resource, data any) (Event, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("encode %s event: %w", declared.name, err)
	}
	return Event{Name: declared.name, Action: action, Resource: resource, Data: encoded}, nil
}
