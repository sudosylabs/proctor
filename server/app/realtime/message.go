// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import "fmt"

// message is one kind of event the server sends. The list is closed: every
// event a constructor builds is declared here, Publish refuses a name nothing
// declared, and each key is the AsyncAPI message in server/asyncapi that holds
// the payload schema. Clients stay open and ignore a name they do not know.
//
// Two messages share a wire name when two audiences receive different
// payloads or authorization for the same fact, as the candidate's own copy of
// a submission does; their keys tell them apart.
type message struct {
	key  string
	name string
}

var (
	declaredMessages = map[string]message{}
	declaredNames    = map[string]struct{}{}
)

// declare records a message while the package initializes. A repeated key is
// a programming error in this package, so it panics before the server starts.
func declare(key, name string) message {
	if _, exists := declaredMessages[key]; exists {
		panic(fmt.Sprintf("realtime message %q is declared twice", key))
	}
	declared := message{key: key, name: name}
	declaredMessages[key] = declared
	declaredNames[name] = struct{}{}
	return declared
}

func isDeclaredName(name string) bool {
	_, declared := declaredNames[name]
	return declared
}

var (
	accessPolicyChangedMessage                = declare("access_policy_changed", "access_policy_changed")
	pluginSettingsChangedMessage              = declare("plugin_settings_changed", "plugin_settings_changed")
	academicUnitCreatedMessage                = declare("academic_unit_created", "academic_unit_created")
	academicUnitUpdatedMessage                = declare("academic_unit_updated", "academic_unit_updated")
	academicUnitArchivedMessage               = declare("academic_unit_archived", "academic_unit_archived")
	candidateExamActivityChangedMessage       = declare("candidate.exam_activity.changed", "candidate.exam_activity.changed")
	currentUserContextChangedMessage          = declare("current_user.context.changed", "current_user.context.changed")
	currentUserSessionsChangedMessage         = declare("current_user.sessions.changed", "current_user.sessions.changed")
	invitationChangedMessage                  = declare("invitation.changed", "invitation.changed")
	jobChangedMessage                         = declare("job.changed", "job.changed")
	onboardingImportChangedMessage            = declare("onboarding_import.changed", "onboarding_import.changed")
	creatorOnboardingImportChangedMessage     = declare("onboarding_import.changed_for_creator", "onboarding_import.changed")
	managerSittingBoardChangedMessage         = declare("manager.sitting_board.changed", "manager.sitting_board.changed")
	userSettingsChangedMessage                = declare("user_settings_changed", "user_settings_changed")
	userProfilePictureChangedMessage          = declare("user_profile_picture_changed", "user_profile_picture_changed")
	examCreatedMessage                        = declare("exam_created", "exam_created")
	examDraftUpdatedMessage                   = declare("exam_draft_updated", "exam_draft_updated")
	examStarterWorkspaceChangedMessage        = declare("exam_starter_workspace_changed", "exam_starter_workspace_changed")
	examArchivedMessage                       = declare("exam_archived", "exam_archived")
	examManagerChangedMessage                 = declare("exam_manager_changed", "exam_manager_changed")
	examOwnerTransferredMessage               = declare("exam_owner_transferred", "exam_owner_transferred")
	examRevisionPublishedMessage              = declare("exam_revision_published", "exam_revision_published")
	examSittingScheduledMessage               = declare("exam_sitting_scheduled", "exam_sitting_scheduled")
	examSittingScheduleUpdatedMessage         = declare("exam_sitting_schedule_updated", "exam_sitting_schedule_updated")
	examSittingCanceledMessage                = declare("exam_sitting_canceled", "exam_sitting_canceled")
	examSittingLifecycleChangedMessage        = declare("exam_sitting_lifecycle_changed", "exam_sitting_lifecycle_changed")
	candidateExamSittingLifecycleMessage      = declare("exam_sitting_lifecycle_changed_for_candidate", "exam_sitting_lifecycle_changed")
	examSittingContentCorrectedMessage        = declare("exam_sitting_content_corrected", "exam_sitting_content_corrected")
	candidateExamSittingCorrectionMessage     = declare("exam_sitting_content_corrected_for_candidate", "exam_sitting_content_corrected")
	examAttemptConnectionOpenedMessage        = declare("exam_attempt_connection_opened", "exam_attempt_connection_opened")
	examAttemptConnectionClosedMessage        = declare("exam_attempt_connection_closed", "exam_attempt_connection_closed")
	examAttemptSuspendedMessage               = declare("exam_attempt_suspended", "exam_attempt_suspended")
	examAttemptReallowedMessage               = declare("exam_attempt_reallowed", "exam_attempt_reallowed")
	examAttemptAccessSuspendedMessage         = declare("exam_attempt_access_suspended", "exam_attempt_access_suspended")
	examAttemptAccessReallowedMessage         = declare("exam_attempt_access_reallowed", "exam_attempt_access_reallowed")
	examAttemptWorkspaceChangedMessage        = declare("exam_attempt_workspace_changed", "exam_attempt_workspace_changed")
	examAttemptFocusWarningMessage            = declare("exam_attempt_focus_warning", "exam_attempt_focus_warning")
	examAttemptIntegrityFlaggedMessage        = declare("exam_attempt_integrity_flagged", "exam_attempt_integrity_flagged")
	examAttemptSubmittedMessage               = declare("exam_attempt_submitted", "exam_attempt_submitted")
	candidateExamAttemptSubmittedMessage      = declare("exam_attempt_submitted_for_candidate", "exam_attempt_submitted")
	examIntegrityDiscrepancyRecordedMessage   = declare("exam_integrity_discrepancy_recorded", "exam_integrity_discrepancy_recorded")
	examIntegrityReviewChangedMessage         = declare("exam_integrity_review_changed", "exam_integrity_review_changed")
	examIntegrityReviewFinalizedMessage       = declare("exam_integrity_review_finalized", "exam_integrity_review_finalized")
	examStudentResultReleasedMessage          = declare("exam_student_result_released", "exam_student_result_released")
	candidateExamStudentResultReleasedMessage = declare("exam_student_result_released_for_candidate", "exam_student_result_released")
)
