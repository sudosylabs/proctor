// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/internal/asyncapidoc"
	"github.com/sudosylabs/proctor/server/model"
)

// conformanceConstructors builds every declared message through its real
// constructor. A new message fails TestEveryDeclaredMessageHasItsAsyncAPIMessage
// until it has an entry here, a schema, and an AsyncAPI message.
func conformanceConstructors() map[string]func() (Event, error) {
	at := time.Date(2026, time.August, 1, 9, 30, 0, 123456000, time.UTC)
	examID := model.NewExamID()
	sittingID := model.NewExamSittingID()
	attemptID := model.NewExamAttemptID()
	candidateID := model.NewUserID()
	connectionID := model.NewAttemptConnectionID()
	flagID := model.NewIntegrityFlagID()
	suspensionID := model.NewAttemptSuspensionID()
	submissionID := model.NewSubmissionID()
	revisionID := model.NewExamRevisionID()
	digest := strings.Repeat("ab", 32)
	review := ExamIntegrityReviewEventFact{
		SubmissionID: submissionID,
		AttemptID:    attemptID,
		CandidateID:  candidateID,
		ReviewID:     model.NewSubmissionReviewID(),
		State:        model.SubmissionReviewDraft,
		Revision:     2,
		ReleaseState: model.SubmissionReviewWithheld,
		ChangedAt:    at,
	}
	finalized := review
	finalized.State = model.SubmissionReviewFinalized
	released := finalized
	released.ReleaseState = model.SubmissionReviewReleased

	return map[string]func() (Event, error){
		"access_policy_changed": func() (Event, error) {
			return NewAccessPolicyChangedEvent(model.NewInstitutionID(), 7)
		},
		"plugin_settings_changed": func() (Event, error) {
			return NewPluginSettingsChangedEvent(model.NewInstitutionID(), "display-topology", 3)
		},
		"academic_unit_created":  func() (Event, error) { return NewAcademicUnitCreatedEvent(model.NewAcademicUnitID()) },
		"academic_unit_updated":  func() (Event, error) { return NewAcademicUnitUpdatedEvent(model.NewAcademicUnitID()) },
		"academic_unit_archived": func() (Event, error) { return NewAcademicUnitArchivedEvent(model.NewAcademicUnitID()) },
		"candidate.exam_activity.changed": func() (Event, error) {
			return NewCandidateExamActivityChangedEvent(candidateID)
		},
		"current_user.context.changed":  func() (Event, error) { return NewCurrentUserContextChangedEvent(candidateID) },
		"current_user.sessions.changed": func() (Event, error) { return NewCurrentUserSessionsChangedEvent(candidateID) },
		"invitation.changed": func() (Event, error) {
			return NewInvitationChangedEvent(model.NewUserID(), model.NewInvitationID(), model.InvitationAccepted)
		},
		// The status-only form is checked by
		// TestJobAndOnboardingImportEventsCoverEveryStatusAndScope.
		"job.changed": func() (Event, error) {
			return NewJobChangedEvent(model.NewInstitutionID(), model.NewJobID(), model.JobTypeOnboardingImportExecute,
				model.JobStatusRunning, &model.JobProgress{Current: 3, Total: 9, Stage: "executing"})
		},
		"onboarding_import.changed": func() (Event, error) {
			return NewOnboardingImportChangedEvent(model.NewOnboardingImportID(), model.OnboardingImportPreviewReady,
				model.Resource{Type: model.ResourceClass, ID: model.NewClassID().String()})
		},
		"onboarding_import.changed_for_creator": func() (Event, error) {
			return NewCreatorOnboardingImportChangedEvent(model.NewUserID(), model.NewOnboardingImportID(),
				model.OnboardingImportPreviewReady)
		},
		"manager.sitting_board.changed": func() (Event, error) {
			return NewManagerSittingBoardChangedEvent(examID, sittingID)
		},
		"user_settings_changed": func() (Event, error) {
			return NewUserSettingsChangedEvent(candidateID, model.NewUserSettingsRevision(), 1, at)
		},
		"user_profile_picture_changed": func() (Event, error) {
			return NewUserProfilePictureChangedEvent(candidateID, model.NewFileEntryID(), 4, at)
		},
		"exam_created":       func() (Event, error) { return NewExamCreatedEvent(examID) },
		"exam_draft_updated": func() (Event, error) { return NewExamDraftUpdatedEvent(examID, 12) },
		"exam_starter_workspace_changed": func() (Event, error) {
			return NewExamStarterWorkspaceChangedEvent(examID, model.NewStarterWorkspaceEntryID(), 13, "file_replaced", at)
		},
		"exam_archived": func() (Event, error) { return NewExamArchivedEvent(examID, 20, at) },
		"exam_manager_changed": func() (Event, error) {
			return NewExamManagerChangedEvent(examID, model.NewUserID(), false, 21, at)
		},
		"exam_owner_transferred": func() (Event, error) {
			return NewExamOwnerTransferredEvent(examID, model.NewUserID(), 22, at)
		},
		"exam_revision_published": func() (Event, error) {
			return NewExamRevisionPublishedEvent(revisionID, examID, 3, digest, model.ExamRevisionPublicationLiveCorrection, at)
		},
		"exam_sitting_scheduled": func() (Event, error) {
			return NewExamSittingScheduledEvent(examID, sittingID, model.ExamSittingScheduled, 1, at)
		},
		"exam_sitting_schedule_updated": func() (Event, error) {
			return NewExamSittingScheduleUpdatedEvent(examID, sittingID, model.ExamSittingScheduled, 2, at)
		},
		"exam_sitting_canceled": func() (Event, error) {
			return NewExamSittingCanceledEvent(examID, sittingID, model.ExamSittingCanceled, 3, at)
		},
		"exam_sitting_lifecycle_changed": func() (Event, error) {
			return NewExamSittingLifecycleChangedEvent(examID, sittingID, model.ExamSittingOpen, 4, "manager_resumed", at.Add(time.Hour), at)
		},
		"exam_sitting_lifecycle_changed_for_candidate": func() (Event, error) {
			return NewCandidateExamSittingLifecycleChangedEvent(examID, sittingID, model.ExamSittingClosing, 5, "scheduled_end_reached", at, at)
		},
		"exam_sitting_content_corrected": func() (Event, error) {
			return NewExamSittingContentCorrectedEvent(examID, sittingID, model.NewExamRevisionID(), revisionID, 6, at)
		},
		"exam_sitting_content_corrected_for_candidate": func() (Event, error) {
			return NewCandidateExamSittingContentCorrectedEvent(examID, sittingID, model.NewExamRevisionID(), revisionID, 6, at)
		},
		"exam_attempt_connection_opened": func() (Event, error) {
			return NewExamAttemptConnectionOpenedEvent(sittingID, attemptID, candidateID, connectionID, at)
		},
		"exam_attempt_connection_closed": func() (Event, error) {
			return NewExamAttemptConnectionClosedEvent(sittingID, attemptID, candidateID, connectionID, model.AttemptConnectionClosePolicySuspended, at)
		},
		// The kick leaves out the Connection and the Flag; the policy form
		// is checked by TestOptionalMembersOfSharedMessagesStayValid.
		"exam_attempt_suspended": func() (Event, error) {
			return NewExamAttemptKickedEvent(sittingID, attemptID, candidateID, "", suspensionID, 3, at)
		},
		"exam_attempt_reallowed": func() (Event, error) {
			return NewExamAttemptReallowedEvent(sittingID, attemptID, candidateID, suspensionID, 4, at)
		},
		"exam_attempt_access_suspended": func() (Event, error) {
			return NewCandidateExamAttemptSuspendedEvent(sittingID, attemptID, candidateID,
				model.AttemptSuspensionSourcePolicy, model.AttemptSuspensionCandidateReasonFocusLossPolicy, at)
		},
		"exam_attempt_access_reallowed": func() (Event, error) {
			return NewCandidateExamAttemptReallowedEvent(sittingID, attemptID, candidateID, at)
		},
		"exam_attempt_workspace_changed": func() (Event, error) {
			return NewCandidateExamAttemptWorkspaceChangedEvent(sittingID, attemptID, candidateID,
				model.NewAttemptWorkspaceEntryID(), model.AttemptWorkspaceMutationMoveEntry, 9, at)
		},
		"exam_attempt_focus_warning": func() (Event, error) {
			return NewCandidateExamAttemptFocusLossWarningEvent(sittingID, attemptID, candidateID, at)
		},
		"exam_attempt_integrity_flagged": func() (Event, error) {
			return NewExamAttemptIntegrityFlaggedEvent(sittingID, attemptID, candidateID, flagID, model.IntegrityPolicyFocusLoss,
				model.IntegrityOutcomeFlagAndSuspend, model.FocusLossMaximumEvidenceEpisodes, 3, at)
		},
		"exam_attempt_submitted": func() (Event, error) {
			return NewExamAttemptSubmittedEvent(sittingID, attemptID, candidateID, submissionID, 0, digest,
				model.ExamSubmissionManagerEndedAttempt, at)
		},
		"exam_attempt_submitted_for_candidate": func() (Event, error) {
			return NewCandidateExamAttemptSubmittedEvent(sittingID, attemptID, candidateID, submissionID, 9, digest,
				model.ExamSubmissionCandidateSubmitted, at)
		},
		"exam_integrity_discrepancy_recorded": func() (Event, error) {
			return NewExamIntegrityDiscrepancyRecordedEvent(submissionID, sittingID, attemptID, candidateID,
				model.NewIntegrityDiscrepancyID(), at)
		},
		"exam_integrity_review_changed":              func() (Event, error) { return NewExamIntegrityReviewChangedEvent(review) },
		"exam_integrity_review_finalized":            func() (Event, error) { return NewExamIntegrityReviewFinalizedEvent(finalized) },
		"exam_student_result_released":               func() (Event, error) { return NewExamStudentResultReleasedEvent(released) },
		"exam_student_result_released_for_candidate": func() (Event, error) { return NewCandidateStudentResultReleasedEvent(released) },
	}
}

// realtimeContract is the compiled AsyncAPI artifact and a JSON Schema
// compiler over it.
type realtimeContract struct {
	messages map[string]any
	compiler *jsonschema.Compiler
}

func loadRealtimeContract(t *testing.T) realtimeContract {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "asyncapi.json"))
	require.NoError(t, err)
	compiler, err := asyncapidoc.NewCompiler(encoded)
	require.NoError(t, err)
	var document struct {
		Components struct {
			Messages map[string]any `json:"messages"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(encoded, &document))
	return realtimeContract{messages: document.Components.Messages, compiler: compiler}
}

// eventMessageKeys are the AsyncAPI messages tagged as events, which are the
// ones app/realtime owns; protocol frames belong to the websocket package.
func (c realtimeContract) eventMessageKeys() []string {
	var keys []string
	for key, raw := range c.messages {
		for _, tag := range raw.(map[string]any)["tags"].([]any) {
			if tag.(map[string]any)["name"] == "event" {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// narrowing is the second member of an event message's payload allOf: the
// constraints that make the envelope this one event.
func (c realtimeContract) narrowing(t *testing.T, key string) map[string]any {
	t.Helper()
	payload := c.messages[key].(map[string]any)["payload"].(map[string]any)
	allOf := payload["allOf"].([]any)
	require.Len(t, allOf, 2, "%s payload is the event frame and its narrowing", key)
	return allOf[1].(map[string]any)
}

func (c realtimeContract) validateData(t *testing.T, key string, data json.RawMessage) {
	t.Helper()
	schema, err := c.compiler.Compile(asyncapidoc.DocumentURL + "#/components/messages/" + key + "/payload/allOf/1/properties/data")
	require.NoError(t, err, key)
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	require.NoError(t, err, key)
	require.NoError(t, schema.Validate(decoded), "%s data %s", key, data)
}

func sortedMessageKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestEveryDeclaredMessageHasItsAsyncAPIMessage(t *testing.T) {
	t.Parallel()

	contract := loadRealtimeContract(t)
	declared := sortedMessageKeys(declaredMessages)
	require.Equal(t, declared, sortedMessageKeys(conformanceConstructors()),
		"every declared message needs a constructor entry here, and nothing else")
	require.Equal(t, declared, contract.eventMessageKeys(),
		"every declared message needs an AsyncAPI event message of the same key, and nothing else")
}

func TestEveryConstructorEmitsWhatItsAsyncAPIMessageDescribes(t *testing.T) {
	t.Parallel()

	// The subtests share one JSON Schema compiler, which is not safe for
	// concurrent use, so they run in sequence.
	contract := loadRealtimeContract(t)
	for key, construct := range conformanceConstructors() {
		t.Run(key, func(t *testing.T) {
			declared, exists := declaredMessages[key]
			require.True(t, exists, "constructor entry %s has no declared message", key)
			event, err := construct()
			require.NoError(t, err)
			require.NoError(t, event.ValidateForPublish())
			require.Equal(t, declared.name, event.Name)
			message := contract.messages[key].(map[string]any)
			require.Equal(t, declared.name, message["name"])

			narrowing := contract.narrowing(t, key)
			properties := narrowing["properties"].(map[string]any)
			required := narrowing["required"].([]any)
			require.Equal(t, map[string]any{"type": "string", "const": declared.name}, properties["event"])
			require.Equal(t, event.UserID != "", containsMember(required, "user_id"), "user_id addressing")
			if action, scoped := properties["action"].(map[string]any); scoped {
				require.Equal(t, string(event.Action), action["const"])
			} else {
				require.Empty(t, event.Action)
			}
			if _, carriesData := properties["data"]; carriesData {
				contract.validateData(t, key, event.Data)
			} else {
				require.Empty(t, event.Data, "the AsyncAPI message declares no data")
			}

			// A peer node forwards exactly the Data its origin built.
			encoded, err := json.Marshal(realtimeEventMessageFromModel(event))
			require.NoError(t, err)
			var forwarded realtimeEventMessage
			require.NoError(t, json.Unmarshal(encoded, &forwarded))
			require.Equal(t, event, forwarded.toModel())
		})
	}
}

// TestOptionalMembersOfSharedMessagesStayValid covers the constructors that
// share a message with another constructor and fill different members.
func TestOptionalMembersOfSharedMessagesStayValid(t *testing.T) {
	t.Parallel()

	contract := loadRealtimeContract(t)
	at := time.Date(2026, time.August, 1, 9, 30, 0, 0, time.UTC)
	sittingID, attemptID, candidateID := model.NewExamSittingID(), model.NewExamAttemptID(), model.NewUserID()

	policy, err := NewExamAttemptPolicySuspendedEvent(sittingID, attemptID, candidateID,
		model.NewAttemptConnectionID(), model.NewIntegrityFlagID(), model.NewAttemptSuspensionID(), 3, at)
	require.NoError(t, err)
	require.Equal(t, examAttemptSuspendedMessage.name, policy.Name)
	contract.validateData(t, examAttemptSuspendedMessage.key, policy.Data)

	kicked, err := NewExamAttemptKickedEvent(sittingID, attemptID, candidateID,
		model.NewAttemptConnectionID(), model.NewAttemptSuspensionID(), 3, at)
	require.NoError(t, err)
	contract.validateData(t, examAttemptSuspendedMessage.key, kicked.Data)

	manual, err := NewCandidateExamAttemptSuspendedEvent(sittingID, attemptID, candidateID,
		model.AttemptSuspensionSourceManual, model.AttemptSuspensionCandidateReasonManagerSuspended, at)
	require.NoError(t, err)
	contract.validateData(t, examAttemptAccessSuspendedMessage.key, manual.Data)

	opened, err := NewExamAttemptConnectionOpenedEvent(sittingID, attemptID, candidateID, model.NewAttemptConnectionID(), at)
	require.NoError(t, err)
	contract.validateData(t, examAttemptConnectionOpenedMessage.key, opened.Data)
}

// TestJobAndOnboardingImportEventsCoverEveryStatusAndScope covers every Job
// type and status without progress, and every onboarding import scope and
// state.
func TestJobAndOnboardingImportEventsCoverEveryStatusAndScope(t *testing.T) {
	t.Parallel()

	contract := loadRealtimeContract(t)
	institutionID := model.NewInstitutionID()
	for _, jobType := range model.JobTypes() {
		for _, status := range []model.JobStatus{
			model.JobStatusQueued, model.JobStatusRunning, model.JobStatusCancelRequested,
			model.JobStatusSucceeded, model.JobStatusFailed, model.JobStatusCanceled,
		} {
			event, err := NewJobChangedEvent(institutionID, model.NewJobID(), jobType, status, nil)
			require.NoError(t, err)
			contract.validateData(t, jobChangedMessage.key, event.Data)
		}
	}

	scopes := []model.Resource{
		{Type: model.ResourceInstitution, ID: institutionID.String()},
		{Type: model.ResourceAcademicUnit, ID: model.NewAcademicUnitID().String()},
		{Type: model.ResourceClass, ID: model.NewClassID().String()},
	}
	for _, scope := range scopes {
		for _, state := range []model.OnboardingImportState{
			model.OnboardingImportUploading, model.OnboardingImportParsing, model.OnboardingImportPreviewReady,
			model.OnboardingImportExecuting, model.OnboardingImportCompleted, model.OnboardingImportCompletedWithErrors,
			model.OnboardingImportCanceled, model.OnboardingImportFailed,
		} {
			event, err := NewOnboardingImportChangedEvent(model.NewOnboardingImportID(), state, scope)
			require.NoError(t, err)
			require.NoError(t, event.ValidateForPublish())
			require.Equal(t, scope, event.Resource)
			contract.validateData(t, onboardingImportChangedMessage.key, event.Data)
		}
	}
}

func containsMember(list []any, member string) bool {
	for _, item := range list {
		if item == member {
			return true
		}
	}
	return false
}
