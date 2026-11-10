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
	GetExamAttempt(context.Context, invocation.Invocation, attempt.GetManagedAttemptQuery) (store.ExamAttemptManagerSnapshot, error)
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
	SubmitExamAttempt(context.Context, invocation.Invocation, attempt.SubmitCommand) (store.ExamSubmissionReceipt, error)
	GetExamSubmission(context.Context, invocation.Invocation, attempt.GetSubmissionQuery) (attempt.ManagedSubmission, error)
	ListExamSubmissionManifest(
		context.Context,
		invocation.Invocation,
		attempt.ListSubmissionManifestQuery,
	) (attempt.SubmissionManifestPage, error)
	OpenExamSubmissionFile(context.Context, invocation.Invocation, attempt.OpenSubmissionFileQuery) (attempt.OpenedContent, error)
}

type examAttemptHTTPModule struct{ application ExamAttemptApplication }

func examAttemptResource(application ExamAttemptApplication) apiResource {
	module := examAttemptHTTPModule{application: application}
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
	return newResource("exam-attempts",
		endpoint{
			Method: http.MethodGet,
			Path:   managerCollection,
			Auth:   AuthPrincipalRequired,
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
			Handle: module.listManaged,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   candidateStatuses,
			Auth:   AuthPrincipalRequired,
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
			Handle: module.listCandidateStatuses,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   managerMember,
			Auth:   AuthPrincipalRequired,
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
			Handle: module.getManaged,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   managerBrowserActivity,
			Auth:   AuthPrincipalRequired,
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
			Handle: module.listBrowserActivity,
		},
		endpoint{
			Method:      http.MethodPost,
			Path:        managerEnd,
			Auth:        AuthPrincipalRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.endByManager,
		},
		endpoint{
			Method:      http.MethodPost,
			Path:        reallow,
			Auth:        AuthPrincipalRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.reallow,
		},
		endpoint{
			Method:      http.MethodPost,
			Path:        suspend,
			Auth:        AuthPrincipalRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.suspend,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   presentation,
			Auth:   AuthSessionRequired,
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
			Handle: module.presentation,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   workspace,
			Auth:   AuthSessionRequired,
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
			Handle: module.workspace,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   workspaceChanges,
			Auth:   AuthSessionRequired,
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
			Handle: module.workspaceChanges,
		},
		endpoint{
			Method:      http.MethodPost,
			Path:        workspaceDirectories,
			Auth:        AuthSessionRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.createWorkspaceDirectory,
		},
		endpoint{
			Method:       http.MethodPost,
			Path:         workspaceFiles,
			Auth:         AuthSessionRequired,
			Idempotency:  IdempotencyRequired,
			MaxBodyBytes: model.AttemptWorkspaceMaximumRequestBytes,
			ProtocolName: "candidate-exam-workspace-file-upload",
			ProtocolKind: RouteProtocolStreamingUpload,
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
			Protocol: module.createWorkspaceFile,
		},
		endpoint{
			Method:      http.MethodPatch,
			Path:        workspaceEntry,
			Auth:        AuthSessionRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.moveWorkspaceEntry,
		},
		endpoint{
			Method:       http.MethodPut,
			Path:         workspaceContent,
			Auth:         AuthSessionRequired,
			Idempotency:  IdempotencyRequired,
			MaxBodyBytes: model.AttemptWorkspaceMaximumRequestBytes,
			ProtocolName: "candidate-exam-workspace-file-replacement",
			ProtocolKind: RouteProtocolStreamingUpload,
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
			Protocol: module.replaceWorkspaceFile,
		},
		endpoint{
			Method:      http.MethodDelete,
			Path:        workspaceEntry,
			Auth:        AuthSessionRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.deleteWorkspaceEntry,
		},
		endpoint{
			Method:       http.MethodGet,
			Path:         resourceContent,
			Auth:         AuthSessionRequired,
			ProtocolName: "candidate-exam-resource-content",
			ProtocolKind: RouteProtocolBinaryDownload,
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
			Protocol: module.openResource,
		},
		endpoint{
			Method:       http.MethodGet,
			Path:         workspaceContent,
			Auth:         AuthSessionRequired,
			ProtocolName: "candidate-exam-workspace-content",
			ProtocolKind: RouteProtocolBinaryDownload,
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
			Protocol: module.openWorkspaceFile,
		},
		endpoint{
			Method:      http.MethodPost,
			Path:        candidateSubmissions,
			Auth:        AuthSessionRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.submit,
		},
		endpoint{
			Method:      http.MethodPut,
			Path:        correctionAcknowledgement,
			Auth:        AuthSessionRequired,
			Idempotency: IdempotencyRequired,
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
			Handle: module.acknowledgeCorrection,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   managerSubmission,
			Auth:   AuthPrincipalRequired,
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
			Handle: module.getSubmission,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   managerSubmissionManifest,
			Auth:   AuthPrincipalRequired,
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
			Handle: module.listSubmissionManifest,
		},
		endpoint{
			Method:       http.MethodGet,
			Path:         managerSubmissionContent,
			Auth:         AuthPrincipalRequired,
			ProtocolName: "exam-submission-file-content",
			ProtocolKind: RouteProtocolBinaryDownload,
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
			Protocol: module.openSubmissionFile,
		},
	)
}

func candidateAttemptContentResult(
	request operationRequest,
	opened attempt.OpenedContent,
	etag string,
	untrusted func(http.Header),
) (protocolResult, error) {
	if opened.Body == nil || opened.SizeBytes < 0 || strings.TrimSpace(opened.MediaType) == "" || etag == `""` {
		return protocolResult{}, errors.New("Exam Attempt application returned incomplete content")
	}

	headers := privateNoStoreHeaders()
	headers.Set("Content-Type", opened.MediaType)
	if untrusted != nil {
		untrusted(headers)
	}

	headers.Set("ETag", etag)
	if etagMatches(request.request.Header.Get("If-None-Match"), etag) {
		_ = opened.Body.Close()
		return notModifiedProtocolResult(opened.SizeBytes).withHeaders(headers), nil
	}

	return binaryDownloadProtocolResult(opened.Body, opened.SizeBytes).withHeaders(headers), nil
}

func candidateAccess(request operationRequest) (attempt.CandidateAccess, error) {
	attemptID, err := pathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return attempt.CandidateAccess{}, err
	}

	headers, err := candidateAttemptAccessHeaders(request.request)
	if err != nil {
		return attempt.CandidateAccess{}, invalidRequestError(err)
	}

	return attempt.CandidateAccess{
		AttemptID:            attemptID,
		ConnectionID:         headers.ConnectionID,
		ContinuityCredential: headers.ContinuityCredential,
	}, nil
}

func privateNoStoreHeaders() http.Header { return http.Header{"Cache-Control": {"private, no-store"}} }
