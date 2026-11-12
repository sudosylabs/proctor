// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	candidateAttemptCredentialHeader = "X-Proctor-Attempt-Credential"
	candidateAttemptConnectionHeader = "X-Proctor-Attempt-Connection-ID"
	sittingCandidateStatusCursorKind = "sitting_candidate_status"
)

type candidateAttemptHeaderAccess struct {
	ConnectionID         model.AttemptConnectionID
	ContinuityCredential string
}

func candidateAttemptAccessHeaders(request *http.Request) (candidateAttemptHeaderAccess, error) {
	if request == nil {
		return candidateAttemptHeaderAccess{}, errors.New("candidate Attempt headers are required")
	}
	credentials := request.Header.Values(candidateAttemptCredentialHeader)
	connections := request.Header.Values(candidateAttemptConnectionHeader)
	if len(credentials) != 1 || len(connections) != 1 || !model.IsValidCredentialToken(credentials[0]) {
		return candidateAttemptHeaderAccess{}, errors.New("one canonical candidate Attempt credential and Connection ID are required")
	}
	connectionID, err := model.ParseAttemptConnectionID(connections[0])
	if err != nil {
		return candidateAttemptHeaderAccess{}, errors.New("one canonical candidate Attempt credential and Connection ID are required")
	}
	return candidateAttemptHeaderAccess{ConnectionID: connectionID, ContinuityCredential: credentials[0]}, nil
}

// ExamAttemptApplication is the application port the Attempt handlers call:
// the candidate's Workspace and Submission, browser and native evidence
// delivery, security preflight, and manager actions on Attempts.
type ExamAttemptApplication interface {
	BrowserSourceStatus(context.Context, invocation.Invocation, attempt.BrowserSourceQuery) (*model.BrowserSourceStatus, error)
	BrowserDeliveryReceipts(context.Context, invocation.Invocation, attempt.BrowserSourceQuery, int64, int) (*model.BrowserReceiptPage, error)
	DeclareBrowserDeliveryGaps(
		context.Context,
		invocation.Invocation,
		attempt.BrowserDeliveryGapsCommand,
	) (*model.BrowserDeliveryGapResult, error)
	SealBrowserDelivery(context.Context, invocation.Invocation, attempt.BrowserDeliveryFinalCommand) (*model.BrowserSourceStatus, error)
	UpdateBrowserDeliverySummary(
		context.Context,
		invocation.Invocation,
		attempt.BrowserDeliverySummaryCommand,
	) (*model.BrowserDeliverySummaryResult, error)
	AppendHistoricalBrowserDelivery(
		context.Context,
		invocation.Invocation,
		attempt.BrowserDeliveryAppendCommand,
	) (*model.BrowserActivityAcknowledgement, error)

	BrowserSourceList(context.Context, invocation.Invocation, attempt.BrowserSourceQuery) ([]model.BrowserSourceStatus, error)
	AppendNativeDelivery(
		context.Context,
		invocation.Invocation,
		attempt.NativeDeliveryAppendCommand,
	) (*model.NativeSecurityAcknowledgement, error)
	DeliveryBudget(context.Context, invocation.Invocation, attempt.DeliveryBudgetQuery) (*model.DeliveryBudgetSnapshot, error)
	StopDeliveryDetails(context.Context, invocation.Invocation, attempt.StopDeliveryDetailsCommand) (*model.StopDeliveryDetailsResult, error)
	NativeDeliveryStatus(context.Context, invocation.Invocation, attempt.NativeDeliveryQuery) (*model.NativeSecurityStreamStatus, error)
	NativeDeliveryReceipt(context.Context, invocation.Invocation, attempt.NativeDeliveryQuery, int64) (*model.NativeBatchReceipt, error)
	DeclareNativeDeliveryGaps(context.Context, invocation.Invocation, attempt.NativeDeliveryGapsCommand) (*model.DeliveryGapReceipt, error)
	SealNativeDelivery(context.Context, invocation.Invocation, attempt.NativeDeliveryFinalCommand) (*model.NativeSecurityStreamStatus, error)
	UpdateNativeDeliverySummary(
		context.Context,
		invocation.Invocation,
		attempt.NativeDeliverySummaryCommand,
	) (*model.NativeSecurityStreamStatus, error)
	RecoverExamSecurityPolicy(context.Context, invocation.Invocation, model.ExamAttemptID) (*store.SecurityPolicyRecovery, error)
	PrepareExamSecurityPreflight(
		context.Context,
		invocation.Invocation,
		attempt.PrepareSecurityPreflightCommand,
	) (*store.SecurityPreflightPrepared, error)
	ReportExamSecurityPreflight(
		context.Context,
		invocation.Invocation,
		attempt.ReportSecurityPreflightCommand,
	) (*model.SecurityPreflightResult, error)
	GetExamAttempt(
		context.Context,
		invocation.Invocation,
		model.ExamID,
		model.ExamSittingID,
		model.ExamAttemptID,
	) (store.ExamAttemptManagerSnapshot, error)
	ListExamAttempts(context.Context, invocation.Invocation, attempt.ListManagedAttemptsQuery) (attempt.ManagedAttemptPage, error)
	ListSittingCandidateStatuses(
		context.Context,
		invocation.Invocation,
		attempt.SittingCandidateStatusesQuery,
	) (attempt.SittingCandidateStatusesPage, error)
	ListExamAttemptBrowserActivity(
		context.Context,
		invocation.Invocation,
		attempt.BrowserActivityPageQuery,
	) (attempt.BrowserActivityPage, error)
	AcknowledgeExamAttemptCorrection(
		context.Context,
		invocation.Invocation,
		attempt.AcknowledgeCorrectionCommand,
	) (attempt.CorrectionAcknowledgementResult, error)
	EndExamAttemptByManager(context.Context, invocation.Invocation, attempt.ManagerEndCommand) (store.ExamSubmissionReceipt, error)
	GetCandidateExamPresentation(context.Context, invocation.Invocation, attempt.CandidateAccess) (attempt.Presentation, error)
	ListCandidateExamWorkspace(context.Context, invocation.Invocation, attempt.WorkspaceQuery) (attempt.WorkspacePage, error)
	ListCandidateExamWorkspaceJournal(
		context.Context,
		invocation.Invocation,
		attempt.WorkspaceJournalQuery,
	) (attempt.WorkspaceJournalPage, error)
	CreateCandidateExamWorkspaceDirectory(
		context.Context,
		invocation.Invocation,
		attempt.CreateWorkspaceDirectoryCommand,
	) (attempt.WorkspaceMutationResult, error)
	CreateCandidateExamWorkspaceFile(
		context.Context,
		invocation.Invocation,
		attempt.CreateWorkspaceFileCommand,
	) (attempt.WorkspaceMutationResult, error)
	ReplaceCandidateExamWorkspaceFile(
		context.Context,
		invocation.Invocation,
		attempt.ReplaceWorkspaceFileCommand,
	) (attempt.WorkspaceMutationResult, error)
	MoveCandidateExamWorkspaceEntry(
		context.Context,
		invocation.Invocation,
		attempt.MoveWorkspaceEntryCommand,
	) (attempt.WorkspaceMutationResult, error)
	DeleteCandidateExamWorkspaceEntry(
		context.Context,
		invocation.Invocation,
		attempt.DeleteWorkspaceEntryCommand,
	) (attempt.WorkspaceMutationResult, error)
	OpenCandidateExamResource(context.Context, invocation.Invocation, attempt.OpenResourceQuery) (attempt.OpenedContent, error)
	OpenCandidateExamWorkspaceFile(context.Context, invocation.Invocation, attempt.OpenWorkspaceFileQuery) (attempt.OpenedContent, error)
	ReallowExamAttempt(context.Context, invocation.Invocation, attempt.ReallowCommand) (attempt.ReallowResult, error)
	SuspendExamAttempt(context.Context, invocation.Invocation, attempt.ManualSuspensionCommand) (attempt.ManualSuspensionResult, error)
	RequestScreenClip(context.Context, invocation.Invocation, attempt.ScreenClipRequestCommand) (attempt.ScreenClipRequestResult, error)
	PendingScreenClips(context.Context, invocation.Invocation, attempt.CandidateAccess) ([]store.ExamAttemptScreenClipView, error)
	TakeScreenClip(
		context.Context,
		invocation.Invocation,
		attempt.CandidateAccess,
		model.ScreenClipID,
	) (store.ExamAttemptScreenClipView, error)
	BeginScreenClipUpload(
		context.Context,
		invocation.Invocation,
		attempt.CandidateAccess,
		model.ScreenClipID,
		model.ScreenClipUpload,
	) (store.ExamAttemptScreenClipView, error)
	ScreenClipUploadStatus(
		context.Context,
		invocation.Invocation,
		attempt.CandidateAccess,
		model.ScreenClipID,
	) (store.ExamAttemptScreenClipUploadStatus, error)
	UploadScreenClipChunk(context.Context, invocation.Invocation, attempt.ScreenClipChunkCommand) error
	CompleteScreenClipUpload(
		context.Context,
		invocation.Invocation,
		attempt.CandidateAccess,
		model.ScreenClipID,
	) (store.ExamAttemptScreenClipView, error)
	SubmitExamAttempt(context.Context, invocation.Invocation, attempt.SubmitCommand) (store.ExamSubmissionReceipt, error)
	GetExamSubmission(context.Context, invocation.Invocation, attempt.GetSubmissionQuery) (attempt.ManagedSubmission, error)
	ListExamSubmissionManifest(
		context.Context,
		invocation.Invocation,
		attempt.ListSubmissionManifestQuery,
	) (attempt.SubmissionManifestPage, error)
	OpenExamSubmissionFile(context.Context, invocation.Invocation, attempt.OpenSubmissionFileQuery) (attempt.OpenedContent, error)
}

// examAttemptHandlers serves every route backed by the Attempt service: the
// Exam Attempt routes here and the security preflight, native delivery,
// browser delivery and delivery budget resources in their own files.
type examAttemptHandlers struct{ app ExamAttemptApplication }

func examAttemptResource(app ExamAttemptApplication) route.Resource {
	h := examAttemptHandlers{app: app}
	managerCollection := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts"
	managerMember := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}"
	managerBrowserActivity := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}/browser-activity"
	managerEnd := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}/end"
	candidateStatuses := "/exams/{exam_id}/sittings/{exam_sitting_id}/candidate-statuses"
	presentation := "/exam-attempts/{exam_attempt_id}/presentation"
	workspace := "/exam-attempts/{exam_attempt_id}/workspace"
	workspaceChanges := "/exam-attempts/{exam_attempt_id}/workspace/changes"
	workspaceDirectories := "/exam-attempts/{exam_attempt_id}/workspace/directories"
	workspaceFiles := "/exam-attempts/{exam_attempt_id}/workspace/files"
	workspaceEntry := "/exam-attempts/{exam_attempt_id}/workspace/entries/{attempt_workspace_entry_id}"
	resourceContent := "/exam-attempts/{exam_attempt_id}/resources/{exam_resource_id}/content"
	workspaceContent := "/exam-attempts/{exam_attempt_id}/workspace/files/{attempt_workspace_entry_id}/content"
	candidateSubmissions := "/exam-attempts/{exam_attempt_id}/submissions"
	correctionAcknowledgement := "/exam-attempts/{exam_attempt_id}/corrections/{exam_revision_id}/acknowledgement"
	managerSubmission := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}/submissions/{submission_id}"
	managerSubmissionManifest := managerSubmission + "/manifest"
	managerSubmissionContent := managerSubmission + "/files/{attempt_workspace_entry_id}/content"
	reallow := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}/reallow"
	suspend := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}/suspend"
	screenClips := "/exams/{exam_id}/sittings/{exam_sitting_id}/attempts/{exam_attempt_id}/screen-clips"
	screenClipRequests := "/exam-attempts/{exam_attempt_id}/screen-clip-requests"
	return route.NewResource("exam-attempts",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   managerCollection,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listManaged,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   candidateStatuses,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listCandidateStatuses,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   managerMember,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"audit.unavailable",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.getManaged,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   managerBrowserActivity,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"audit.unavailable",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.not_found",
				"exam.sitting.unavailable",
			},
			Handle: h.listBrowserActivity,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        managerEnd,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.revision_conflict",
				"exam.attempt.state_conflict",
				"exam.attempt.sitting_not_open",
				"exam.attempt.management_authority_conflict",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.endByManager,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        reallow,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.revision_conflict",
				"exam.attempt.suspension_conflict",
				"exam.attempt.state_conflict",
				"exam.attempt.sitting_not_open",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.reallow,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        suspend,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.revision_conflict",
				"exam.attempt.state_conflict",
				"exam.attempt.sitting_not_open",
				"exam.attempt.connection_lost",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.suspend,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        screenClips,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.state_conflict",
				"exam.attempt.screen_recording_off",
				"exam.attempt.screen_clip_limit_reached",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.requestScreenClip,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   screenClipRequests,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.pendingScreenClips,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   screenClipRequests + "/{screen_clip_id}/take",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.takeScreenClip,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   screenClipRequests + "/{screen_clip_id}/upload",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
				"exam.attempt.screen_clip_too_large",
			},
			Handle: h.beginScreenClipUpload,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   screenClipRequests + "/{screen_clip_id}/upload",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.screenClipUploadStatus,
		},
		route.Endpoint{
			Method:       http.MethodPut,
			Path:         screenClipRequests + "/{screen_clip_id}/upload/chunks/{chunk:sequence}",
			Auth:         route.AuthSessionRequired,
			MaxBodyBytes: model.ScreenClipChunkBytes,
			ProtocolName: "candidate-screen-clip-chunk-upload",
			ProtocolKind: route.ProtocolStreamingUpload,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Protocol: h.uploadScreenClipChunk,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   screenClipRequests + "/{screen_clip_id}/upload/complete",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
				"exam.attempt.screen_clip_incomplete",
			},
			Handle: h.completeScreenClipUpload,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   presentation,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.presentation,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   workspace,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.workspace,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   workspaceChanges,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.workspaceChanges,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        workspaceDirectories,
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.connection_closed",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.attempt.workspace.path_conflict",
				"exam.attempt.workspace.entry_conflict",
				"exam.attempt.workspace.entry_limit",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.createWorkspaceDirectory,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         workspaceFiles,
			Auth:         route.AuthSessionRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: model.AttemptWorkspaceMaximumRequestBytes,
			ProtocolName: "candidate-exam-workspace-file-upload",
			ProtocolKind: route.ProtocolStreamingUpload,
			Errors: []string{
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.connection_closed",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.attempt.workspace.path_conflict",
				"exam.attempt.workspace.entry_conflict",
				"exam.attempt.workspace.entry_limit",
				"exam.attempt.workspace.size_limit",
				"exam.attempt.workspace.object_conflict",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Protocol: h.createWorkspaceFile,
		},
		route.Endpoint{
			Method:      http.MethodPatch,
			Path:        workspaceEntry,
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.connection_closed",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.attempt.workspace.path_conflict",
				"exam.attempt.workspace.entry_conflict",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.moveWorkspaceEntry,
		},
		route.Endpoint{
			Method:       http.MethodPut,
			Path:         workspaceContent,
			Auth:         route.AuthSessionRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: model.AttemptWorkspaceMaximumRequestBytes,
			ProtocolName: "candidate-exam-workspace-file-replacement",
			ProtocolKind: route.ProtocolStreamingUpload,
			Errors: []string{
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.connection_closed",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.attempt.workspace.path_conflict",
				"exam.attempt.workspace.entry_conflict",
				"exam.attempt.workspace.content_conflict",
				"exam.attempt.workspace.size_limit",
				"exam.attempt.workspace.object_conflict",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Protocol: h.replaceWorkspaceFile,
		},
		route.Endpoint{
			Method:      http.MethodDelete,
			Path:        workspaceEntry,
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.connection_closed",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.attempt.workspace.path_conflict",
				"exam.attempt.workspace.entry_conflict",
				"exam.attempt.workspace.content_conflict",
				"exam.attempt.workspace.directory_not_empty",
				"exam.attempt.workspace.cursor_conflict",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.deleteWorkspaceEntry,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         resourceContent,
			Auth:         route.AuthSessionRequired,
			ProtocolName: "candidate-exam-resource-content",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Protocol: h.openResource,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         workspaceContent,
			Auth:         route.AuthSessionRequired,
			ProtocolName: "candidate-exam-workspace-content",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Protocol: h.openWorkspaceFile,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        candidateSubmissions,
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.sitting_not_open",
				"exam.attempt.state_conflict",
				"exam.attempt.connection_closed",
				"exam.attempt.conflict",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.attempt.workspace.cursor_conflict",
				"exam.attempt.focus_loss_conflict",
				"exam.attempt.revision_conflict",
				"exam.attempt.correction_conflict",
				"exam.attempt.browser_activity_conflict",
				"exam.attempt.connection_lost",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.submit,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        correctionAcknowledgement,
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.revision_conflict",
				"exam.attempt.correction_conflict",
				"exam.attempt.state_conflict",
				"exam.attempt.sitting_not_open",
				"exam.attempt.connection_closed",
				"exam.attempt.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"authorization.unavailable",
				"exam.attempt.registered_desktop_required",
				"exam.delivery.metadata_capacity",
			},
			Handle: h.acknowledgeCorrection,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   managerSubmission,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.getSubmission,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   managerSubmissionManifest,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listSubmissionManifest,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         managerSubmissionContent,
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "exam-submission-file-content",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.unavailable",
				"exam.delivery.metadata_capacity",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Protocol: h.openSubmissionFile,
		},
	)
}

func candidateAttemptContentResult(
	request route.Request,
	opened attempt.OpenedContent,
	etag string,
	untrusted func(http.Header),
) (route.ProtocolResult, error) {
	if opened.Body == nil || opened.SizeBytes < 0 || strings.TrimSpace(opened.MediaType) == "" || etag == `""` {
		return route.ProtocolResult{}, errors.New("Exam Attempt application returned incomplete content")
	}

	headers := privateNoStoreHeaders()
	headers.Set("Content-Type", opened.MediaType)
	if untrusted != nil {
		untrusted(headers)
	}

	headers.Set("ETag", etag)
	if etagMatches(request.HTTP.Header.Get("If-None-Match"), etag) {
		_ = opened.Body.Close()
		return route.NotModified(opened.SizeBytes).WithHeaders(headers), nil
	}

	return route.BinaryDownload(opened.Body, opened.SizeBytes).WithHeaders(headers), nil
}

func candidateAccess(request route.Request) (attempt.CandidateAccess, error) {
	attemptID, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return attempt.CandidateAccess{}, err
	}

	headers, err := candidateAttemptAccessHeaders(request.HTTP)
	if err != nil {
		return attempt.CandidateAccess{}, route.InvalidRequest(err)
	}

	return attempt.CandidateAccess{
		AttemptID:            attemptID,
		ConnectionID:         headers.ConnectionID,
		ContinuityCredential: headers.ContinuityCredential,
	}, nil
}

func privateNoStoreHeaders() http.Header { return http.Header{"Cache-Control": {"private, no-store"}} }
