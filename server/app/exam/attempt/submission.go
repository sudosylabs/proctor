// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"io"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type SubmitCommand struct {
	Access                    WorkspaceMutationAccess
	ExpectedCurrentRevisionID model.ExamRevisionID
	ExpectedWorkspaceCursor   int64
	FinalFocusLossSequence    int64
	IdempotencyKey            string
}

type SubmissionResult struct {
	Receipt         store.ExamSubmissionReceipt
	Provenance      model.ExamSubmissionProvenance
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	ClassID         model.ClassID
	CandidateUserID model.UserID
	ParticipationID model.AttemptParticipationID
	Generation      int64
	ConnectionID    model.AttemptConnectionID
	Replayed        bool
}

type GetSubmissionQuery struct {
	ExamID       model.ExamID
	SittingID    model.ExamSittingID
	AttemptID    model.ExamAttemptID
	SubmissionID model.SubmissionID
}

type ManagedSubmission struct {
	Authorization store.ExamSubmissionAuthorization
	Submission    model.ExamSubmission
}

type ListSubmissionManifestQuery struct {
	GetSubmissionQuery
	AfterEntryID model.AttemptWorkspaceEntryID
	Limit        int
}

type SubmissionManifestPage struct {
	SubmissionID    model.SubmissionID
	WorkspaceCursor int64
	ManifestDigest  string
	Items           []store.ExamSubmissionManifestItem
	HasMore         bool
}

type OpenSubmissionFileQuery struct {
	GetSubmissionQuery
	EntryID model.AttemptWorkspaceEntryID
}

// OpenExamSubmissionFile opens one file sealed in a Submission for a caller
// with Submission view.
func (s *Service) OpenExamSubmissionFile(
	ctx context.Context,
	inv invocation.Invocation,
	query OpenSubmissionFileQuery,
) (_ OpenedContent, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.EntryID.IsValid() {
		return OpenedContent{}, apperror.Invalid("exam.attempt.invalid", "submission_entry_id")
	}
	if _, err := s.resolveManagedSubmission(ctx, inv, query.GetSubmissionQuery); err != nil {
		return OpenedContent{}, err
	}

	selector, err := s.submissions.ResolveFile(ctx, query.SubmissionID, query.EntryID)
	if err != nil {
		return OpenedContent{}, storeError(storeErrors, err)
	}
	if !validSubmissionFileSelector(selector, query.EntryID) {
		return OpenedContent{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Submission file selector"))
	}

	var body io.ReadCloser
	switch selector.StorageOrigin {
	case model.AttemptWorkspaceStorageStarter:
		body, err = s.content.OpenStarterWorkspaceObject(ctx, selector.StarterObjectID)
	case model.AttemptWorkspaceStorageAttempt:
		body, err = s.content.OpenAttemptWorkspaceObject(ctx, selector.AttemptObjectID)
	default:
		return OpenedContent{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("invalid Submission file storage origin"))
	}

	if err != nil {
		return OpenedContent{}, apperror.New("exam.attempt.unavailable").Wrap(err)
	}

	return OpenedContent{
		Body:           body,
		MediaType:      selector.Entry.MediaType,
		SizeBytes:      selector.Entry.SizeBytes,
		SHA256:         selector.Entry.SHA256,
		ContentVersion: selector.ContentVersion,
	}, nil
}

func validSubmissionFileSelector(selector *store.ExamSubmissionFileSelector,
	entryID model.AttemptWorkspaceEntryID,
) bool {
	if selector == nil ||
		selector.Entry.EntryID != entryID ||
		selector.Entry.Kind != model.StarterWorkspaceEntryFile ||
		!validSubmissionManifestItem(selector.Entry) ||
		selector.ContentVersion != selector.Entry.ContentVersion {
		return false
	}

	switch selector.StorageOrigin {
	case model.AttemptWorkspaceStorageStarter:
		return selector.StarterObjectID.IsValid() && selector.AttemptObjectID.IsZero()
	case model.AttemptWorkspaceStorageAttempt:
		return selector.AttemptObjectID.IsValid() && selector.StarterObjectID.IsZero()
	default:
		return false
	}
}

// ListExamSubmissionManifest returns one page of a Submission's sealed
// manifest for a caller with Submission view.
func (s *Service) ListExamSubmissionManifest(
	ctx context.Context,
	inv invocation.Invocation,
	query ListSubmissionManifestQuery,
) (_ SubmissionManifestPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if query.Limit < 1 || query.Limit > model.ExamSubmissionManifestReadMaximum {
		return SubmissionManifestPage{}, apperror.Invalid("exam.attempt.invalid", "submission_manifest")
	}
	if _, err := s.resolveManagedSubmission(ctx, inv, query.GetSubmissionQuery); err != nil {
		return SubmissionManifestPage{}, err
	}

	stored, err := s.submissions.ListManifest(ctx, store.ExamSubmissionManifestListOptions{
		SubmissionID: query.SubmissionID,
		AfterEntryID: query.AfterEntryID,
		Limit:        query.Limit,
	})
	if err != nil {
		return SubmissionManifestPage{}, storeError(storeErrors, err)
	}
	if !validSubmissionManifestPage(stored, query) {
		return SubmissionManifestPage{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Submission manifest page"))
	}

	items := append([]store.ExamSubmissionManifestItem(nil), stored.Items...)
	return SubmissionManifestPage{
		SubmissionID:    stored.SubmissionID,
		WorkspaceCursor: stored.WorkspaceCursor,
		ManifestDigest:  stored.ManifestDigest,
		Items:           items,
		HasMore:         stored.HasMore,
	}, nil
}

func validSubmissionManifestPage(page *store.ExamSubmissionManifestPage, query ListSubmissionManifestQuery) bool {
	if page == nil ||
		page.SubmissionID != query.SubmissionID ||
		page.WorkspaceCursor < 0 ||
		!store.IsValidWorkspaceSHA256(page.ManifestDigest) ||
		len(page.Items) > query.Limit ||
		(page.HasMore && len(page.Items) != query.Limit) {
		return false
	}

	previous := query.AfterEntryID
	for _, item := range page.Items {
		if !validSubmissionManifestItem(item) || (!previous.IsZero() && item.EntryID.String() <= previous.String()) {
			return false
		}

		previous = item.EntryID
	}

	return true
}

func validSubmissionManifestItem(item store.ExamSubmissionManifestItem) bool {
	if !item.EntryID.IsValid() {
		return false
	}

	path, err := model.NormalizeAttemptWorkspacePath(item.Path)
	if err != nil || path != item.Path {
		return false
	}

	switch item.Kind {
	case model.StarterWorkspaceEntryDirectory:
		return item.ContentVersion.IsZero() && item.MediaType == "" && item.SizeBytes == 0 && item.SHA256 == ""
	case model.StarterWorkspaceEntryFile:
		return item.ContentVersion.IsValid() &&
			(model.AttemptWorkspaceContent{
				MediaType: item.MediaType,
				SizeBytes: item.SizeBytes,
				SHA256:    item.SHA256,
			}).Validate() == nil
	default:
		return false
	}
}

// GetExamSubmission returns a Submission and its authorization projection for
// a caller with Submission view. A Submission that does not belong to the
// named Exam, Sitting, and Attempt is reported as not found.
func (s *Service) GetExamSubmission(
	ctx context.Context,
	inv invocation.Invocation,
	query GetSubmissionQuery,
) (_ ManagedSubmission, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	authorization, err := s.resolveManagedSubmission(ctx, inv, query)
	if err != nil {
		return ManagedSubmission{}, err
	}

	submission, err := s.submissions.Get(ctx, query.SubmissionID)
	if err != nil {
		return ManagedSubmission{}, storeError(storeErrors, err)
	}

	if submission == nil ||
		submission.Validate() != nil ||
		submission.ID != query.SubmissionID ||
		submission.AttemptID != query.AttemptID {
		return ManagedSubmission{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent managed Submission"))
	}

	return ManagedSubmission{Authorization: *authorization, Submission: *submission}, nil
}

func (s *Service) resolveManagedSubmission(
	ctx context.Context,
	inv invocation.Invocation,
	query GetSubmissionQuery,
) (*store.ExamSubmissionAuthorization, error) {
	if !query.ExamID.IsValid() || !query.SittingID.IsValid() || !query.AttemptID.IsValid() || !query.SubmissionID.IsValid() {
		return nil, apperror.Invalid("exam.attempt.invalid", "submission")
	}
	if err := s.managers.AuthorizeSubmissionView(ctx, inv, query.SubmissionID); err != nil {
		return nil, err
	}

	authorization, err := s.submissions.Resolve(ctx, query.SubmissionID)
	if err != nil {
		return nil, storeError(storeErrors, err)
	}

	if authorization.ExamID != query.ExamID ||
		authorization.SittingID != query.SittingID ||
		authorization.AttemptID != query.AttemptID {
		return nil, apperror.New("exam.attempt.not_found")
	}

	return authorization, nil
}

// SubmitExamAttempt idempotently seals the candidate's Submission from the
// acknowledged Attempt Workspace state, fenced by participation generation,
// the expected current Exam Revision, the expected workspace cursor, and the
// final Focus Loss sequence.
func (s *Service) SubmitExamAttempt(
	ctx context.Context,
	inv invocation.Invocation,
	command SubmitCommand,
) (_ store.ExamSubmissionReceipt, resultErr error) {
	defer func() { s.recordOperational("submit", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	workspaceAccess, err := workspaceMutationSelector(inv, command.Access)
	if err != nil {
		return store.ExamSubmissionReceipt{}, err
	}
	if !command.ExpectedCurrentRevisionID.IsValid() || command.ExpectedWorkspaceCursor < 0 || command.FinalFocusLossSequence < 0 {
		return store.ExamSubmissionReceipt{}, apperror.Invalid("exam.attempt.invalid", "submission")
	}

	commandIdempotency, err := prepareSubmissionIdempotency(
		inv,
		command.IdempotencyKey,
		workspaceAccess.AttemptID,
		command.ExpectedCurrentRevisionID,
		command.ExpectedWorkspaceCursor,
		command.FinalFocusLossSequence,
	)
	if err != nil {
		return store.ExamSubmissionReceipt{}, err
	}

	sealAccess := store.ExamSubmissionSealAccess{
		AttemptID:                 workspaceAccess.AttemptID,
		ParticipationID:           workspaceAccess.ParticipationID,
		Generation:                workspaceAccess.Generation,
		ConnectionID:              workspaceAccess.ConnectionID,
		CandidateUserID:           workspaceAccess.CandidateUserID,
		SessionID:                 workspaceAccess.SessionID,
		ContinuityCredentialHash:  workspaceAccess.ContinuityCredentialHash,
		ExpectedCurrentRevisionID: command.ExpectedCurrentRevisionID,
		ExpectedWorkspaceCursor:   command.ExpectedWorkspaceCursor,
		FinalFocusLossSequence:    command.FinalFocusLossSequence,
	}
	target, err := s.submissions.ResolveSealTarget(ctx, sealAccess)
	if err != nil {
		return store.ExamSubmissionReceipt{}, storeError(storeErrors, err)
	}
	if !validSubmissionTarget(target, sealAccess) {
		return store.ExamSubmissionReceipt{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Submission seal target"))
	}

	submissionID, receiptCode := s.newSubmissionID(), s.newReceiptCode()
	if !submissionID.IsValid() {
		return store.ExamSubmissionReceipt{}, apperror.Invalid("exam.attempt.invalid", "submission_id")
	}
	if !receiptCode.IsValid() {
		return store.ExamSubmissionReceipt{}, apperror.Invalid("exam.attempt.invalid", "receipt_code")
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    target.ClassID.String(),
		Operation:  store.ExamSubmissionSealOperation,
		Value:      map[string]any{"exam_attempt_id": sealAccess.AttemptID.String()},
	})
	if err != nil {
		return store.ExamSubmissionReceipt{}, err
	}

	at := target.SealAt
	var notice *store.PreparedMail
	var expectedRecipientRevision int64
	if !target.Replayed {
		preparedMail, prepareErr := s.mail.PrepareSubmissionReceipt(ctx, SubmissionMailPreparation{
			CandidateUserID: target.CandidateUserID,
			ExamID:          target.ExamID,
			SittingID:       target.SittingID,
			SubmissionID:    submissionID,
			ReceiptCode:     receiptCode,
			SealedAt:        at,
			Provenance:      model.ExamSubmissionCandidateSubmitted,
		})
		if prepareErr != nil || preparedMail == nil || preparedMail.Notice == nil || preparedMail.ExpectedRecipientRevision < 1 {
			if prepareErr == nil {
				prepareErr = errors.New("invalid Submission receipt mail preparation")
			}
			return store.ExamSubmissionReceipt{}, s.failAudit(ctx, auditID, apperror.New("exam.attempt.unavailable").Wrap(prepareErr))
		}

		notice, expectedRecipientRevision = preparedMail.Notice, preparedMail.ExpectedRecipientRevision
	}

	stored, err := s.submissions.Seal(ctx, &store.ExamSubmissionSeal{
		SubmissionID:              submissionID,
		ReceiptCode:               receiptCode,
		Access:                    sealAccess,
		AuditEventID:              auditID,
		AuditAt:                   at,
		Notice:                    notice,
		ExpectedRecipientRevision: expectedRecipientRevision,
	}, commandIdempotency)
	if err != nil {
		return store.ExamSubmissionReceipt{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectSubmissionResult(stored)
	if err != nil {
		return store.ExamSubmissionReceipt{}, err
	}

	if !result.Replayed {
		if effectErr := s.publishAttemptSubmitted(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_submitted", effectErr)
		}
	}

	return result.Receipt, nil
}

func validSubmissionTarget(target *store.ExamSubmissionSealTarget, sealAccess store.ExamSubmissionSealAccess) bool {
	return target != nil &&
		target.ExamID.IsValid() &&
		target.SittingID.IsValid() &&
		target.ClassID.IsValid() &&
		target.CandidateUserID == sealAccess.CandidateUserID &&
		target.WorkspaceID.IsValid() &&
		target.CurrentRevisionID == sealAccess.ExpectedCurrentRevisionID &&
		!target.SealAt.IsZero()
}

func projectSubmissionResult(stored *store.ExamSubmissionSealResult) (SubmissionResult, error) {
	if !stored.Sealed() {
		return SubmissionResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Submission seal result"))
	}

	return SubmissionResult{
		Receipt:         stored.Receipt,
		Provenance:      model.ExamSubmissionCandidateSubmitted,
		ExamID:          stored.ExamID,
		SittingID:       stored.SittingID,
		ClassID:         stored.ClassID,
		CandidateUserID: stored.CandidateUserID,
		ParticipationID: stored.ParticipationID,
		Generation:      stored.Generation,
		ConnectionID:    stored.ConnectionID,
		Replayed:        stored.Replayed,
	}, nil
}
