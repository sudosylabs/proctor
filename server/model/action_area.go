// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"sort"
	"strings"
)

// ActionArea groups the actions a role editor shows together.
type ActionArea string

const (
	ActionAreaExams       ActionArea = "exams"
	ActionAreaSittings    ActionArea = "sittings"
	ActionAreaSubmissions ActionArea = "submissions"
	ActionAreaRecords     ActionArea = "records"
	ActionAreaPeople      ActionArea = "people"
	ActionAreaStructure   ActionArea = "structure"
	ActionAreaAccess      ActionArea = "access"
	ActionAreaOperations  ActionArea = "operations"
)

// ActionAreas lists the areas in the order a role editor shows them.
func ActionAreas() []ActionArea {
	return []ActionArea{
		ActionAreaExams, ActionAreaSittings, ActionAreaSubmissions, ActionAreaRecords,
		ActionAreaPeople, ActionAreaStructure, ActionAreaAccess, ActionAreaOperations,
	}
}

// actionAreas holds every role-grantable action. An override shares the area
// of the action it overrides, so it is not listed.
var actionAreas = map[Action]ActionArea{
	ActionExamCreate:                     ActionAreaExams,
	ActionExamView:                       ActionAreaExams,
	ActionExamManage:                     ActionAreaExams,
	ActionExamPublish:                    ActionAreaExams,
	ActionExamSittingCreate:              ActionAreaSittings,
	ActionExamSittingView:                ActionAreaSittings,
	ActionExamSittingManage:              ActionAreaSittings,
	ActionExamAttemptBrowserActivityView: ActionAreaSittings,
	ActionSubmissionView:                 ActionAreaSubmissions,
	ActionSubmissionReview:               ActionAreaSubmissions,
	ActionSubmissionRelease:              ActionAreaSubmissions,
	ActionExamRecordsComplete:            ActionAreaRecords,
	ActionExamRecordsExport:              ActionAreaRecords,
	ActionExamRecordsHold:                ActionAreaRecords,
	ActionRetentionPolicyView:            ActionAreaRecords,
	ActionAuditView:                      ActionAreaRecords,
	ActionAcademicAuditView:              ActionAreaRecords,
	ActionUserView:                       ActionAreaPeople,
	ActionUserManage:                     ActionAreaPeople,
	ActionUserProfilePictureManage:       ActionAreaPeople,
	ActionSessionView:                    ActionAreaPeople,
	ActionSessionManage:                  ActionAreaPeople,
	ActionExternalIdentityManage:         ActionAreaPeople,
	ActionInvitationCreate:               ActionAreaPeople,
	ActionInvitationView:                 ActionAreaPeople,
	ActionInvitationManage:               ActionAreaPeople,
	ActionOnboardingBatchView:            ActionAreaPeople,
	ActionOnboardingBatchManage:          ActionAreaPeople,
	ActionInstitutionManage:              ActionAreaStructure,
	ActionAcademicUnitView:               ActionAreaStructure,
	ActionAcademicUnitManage:             ActionAreaStructure,
	ActionAcademicUnitMembersView:        ActionAreaStructure,
	ActionAcademicUnitMembersManage:      ActionAreaStructure,
	ActionAcademicPeriodView:             ActionAreaStructure,
	ActionAcademicPeriodManage:           ActionAreaStructure,
	ActionProgrammeView:                  ActionAreaStructure,
	ActionProgrammeManage:                ActionAreaStructure,
	ActionProgrammeLevelView:             ActionAreaStructure,
	ActionProgrammeLevelManage:           ActionAreaStructure,
	ActionClassView:                      ActionAreaStructure,
	ActionClassManage:                    ActionAreaStructure,
	ActionClassMembersView:               ActionAreaStructure,
	ActionClassMembersManage:             ActionAreaStructure,
	ActionAcademicProgressionManage:      ActionAreaStructure,
	ActionRoleView:                       ActionAreaAccess,
	ActionRoleManage:                     ActionAreaAccess,
	ActionRoleBindingView:                ActionAreaAccess,
	ActionRoleBindingManage:              ActionAreaAccess,
	ActionAccessPolicyView:               ActionAreaAccess,
	ActionAccessPolicyManage:             ActionAreaAccess,
	ActionJobView:                        ActionAreaOperations,
	ActionJobManage:                      ActionAreaOperations,
	ActionMailView:                       ActionAreaOperations,
	ActionMailManage:                     ActionAreaOperations,
	ActionMailRekey:                      ActionAreaOperations,
	ActionMFARekey:                       ActionAreaOperations,
}

const overrideSuffix = ".override"

// OverriddenAction returns the action an override action widens, and false
// for an action that overrides nothing.
func OverriddenAction(action Action) (Action, bool) {
	base, ok := strings.CutSuffix(string(action), overrideSuffix)
	if !ok {
		return "", false
	}
	return Action(base), true
}

// AreaForAction returns the area of a role-grantable action.
func AreaForAction(action Action) (ActionArea, bool) {
	if base, ok := OverriddenAction(action); ok {
		action = base
	}
	area, ok := actionAreas[action]
	return area, ok
}

// GrantableActions returns, in stable order, every action a custom role may
// hold.
func GrantableActions() []Action {
	actions := make([]Action, 0, len(actionDefinitions))
	for action := range actionDefinitions {
		if IsGrantableAction(string(action)) {
			actions = append(actions, action)
		}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })
	return actions
}
