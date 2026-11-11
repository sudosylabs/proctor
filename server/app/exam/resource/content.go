// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CreateCommand struct {
	ExamID                           model.ExamID
	ExpectedDraftRevision            int64
	DisplayName, DescriptionMarkdown string
	MediaType                        model.ExamResourceMediaType
	Body                             io.Reader
	Size                             int64
	ExpectedSHA256                   string
	IdempotencyKey                   string
}

type ReplaceContentCommand struct {
	ExamID                model.ExamID
	ResourceID            model.ExamResourceID
	ExpectedDraftRevision int64
	MediaType             model.ExamResourceMediaType
	Body                  io.Reader
	Size                  int64
	ExpectedSHA256        string
	IdempotencyKey        string
}

// CreateExamResource idempotently uploads, verifies, and adds an Exam
// Resource to the Exam Draft, fenced by ExpectedDraftRevision.
func (s *Service) CreateExamResource(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateCommand,
) (_ store.ExamResourceRecord, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.ExamID.IsValid() ||
		command.ExpectedDraftRevision < 1 ||
		command.Body == nil ||
		command.Size < 0 ||
		command.Size > model.ExamResourceMaximumBytes ||
		!command.MediaType.IsValid() ||
		!validSHA256(command.ExpectedSHA256) {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "upload")
	}

	command.DisplayName = strings.TrimSpace(command.DisplayName)
	commandIdempotency, err := prepareResourceIdempotency(
		inv,
		idempotencyOperationAddResource,
		command.IdempotencyKey,
		command.ExamID,
		command.ExpectedDraftRevision,
		"",
		command.DisplayName,
		command.DescriptionMarkdown,
		command.MediaType,
		command.Size,
		command.ExpectedSHA256,
		nil,
	)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	authorization, err := s.authorizeExam(ctx, inv, command.ExamID, true)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	items, err := s.resources.List(ctx, command.ExamID)
	if err != nil {
		return store.ExamResourceRecord{}, storeErrors.Translate(err)
	}

	position := min(len(items), model.ExamResourceMaximumCount-1)
	at := model.TimeUTC(s.now())
	principal := inv.Principal()
	resourceID, entryID, revisionID, leaseID := s.newResourceID(), s.newEntryID(), s.newRevisionID(), s.newLeaseID()
	entry, err := model.NewFileEntryForPurpose(entryID, model.FilePurposeExamResource, model.FileIndexingNone, at)
	if err != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "file_entry").Wrap(err)
	}

	revision, err := model.NewFileRevision(revisionID, entryID, model.FileAvailabilityPending, model.FileIndexingNotRequired, at)
	if err != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "file_revision").Wrap(err)
	}

	lease, err := model.NewUploadLease(leaseID, revisionID, principal.UserID, at, at.Add(time.Hour))
	if err != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "upload_lease").Wrap(err)
	}

	resource, err := model.NewExamResource(
		resourceID,
		command.ExamID,
		entryID,
		revisionID,
		command.DisplayName,
		command.DescriptionMarkdown,
		position,
		at,
	)
	if err != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "metadata").Wrap(err)
	}

	if _, err = s.resources.ReserveUpload(ctx, &store.ExamResourceUploadReservation{
		ExamID:                command.ExamID,
		ActorUserID:           principal.UserID,
		ManagerOverride:       authorization.override,
		ExpectedDraftRevision: command.ExpectedDraftRevision,
		ResourceID:            resourceID,
		Entry:                 entry,
		Revision:              revision,
		Lease:                 lease,
	}); err != nil {
		return store.ExamResourceRecord{}, storeErrors.Translate(err)
	}

	rendition, err := s.content.StoreExamResource(ctx, revisionID, command.MediaType, command.Body, command.Size, at)
	if err != nil {
		return store.ExamResourceRecord{}, mapContentError(err)
	}

	if subtle.ConstantTimeCompare([]byte(rendition.SHA256), []byte(command.ExpectedSHA256)) != 1 {
		_ = s.content.RemoveExamResource(ctx, rendition.RevisionID, rendition.ID)
		return store.ExamResourceRecord{}, apperror.New("exam.resource.content_invalid").Wrap(errors.New("content checksum mismatch"))
	}

	return s.finalizeUpload(ctx, inv, authorization, resource, leaseID, &rendition, command.ExpectedDraftRevision, commandIdempotency, "add")
}

// ReplaceExamResourceContent idempotently uploads and selects new content for
// an existing Exam Resource, fenced by ExpectedDraftRevision.
func (s *Service) ReplaceExamResourceContent(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplaceContentCommand,
) (_ store.ExamResourceRecord, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.ExamID.IsValid() ||
		!command.ResourceID.IsValid() ||
		command.ExpectedDraftRevision < 1 ||
		command.Body == nil ||
		command.Size < 0 ||
		command.Size > model.ExamResourceMaximumBytes ||
		!command.MediaType.IsValid() ||
		!validSHA256(command.ExpectedSHA256) {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "upload")
	}

	commandIdempotency, err := prepareResourceIdempotency(
		inv,
		idempotencyOperationReplaceResourceContent,
		command.IdempotencyKey,
		command.ExamID,
		command.ExpectedDraftRevision,
		command.ResourceID.String(),
		"",
		"",
		command.MediaType,
		command.Size,
		command.ExpectedSHA256,
		nil,
	)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	authorization, err := s.authorizeExam(ctx, inv, command.ExamID, true)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	current, err := s.resources.Get(ctx, command.ExamID, command.ResourceID)
	if err != nil {
		return store.ExamResourceRecord{}, storeErrors.Translate(err)
	}

	at := model.TimeUTC(s.now())
	principal := inv.Principal()
	revisionID, leaseID := s.newRevisionID(), s.newLeaseID()
	revision, err := model.NewFileRevision(
		revisionID,
		current.Resource.FileEntryID,
		model.FileAvailabilityPending,
		model.FileIndexingNotRequired,
		at,
	)
	if err != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "file_revision").Wrap(err)
	}

	lease, err := model.NewUploadLease(leaseID, revisionID, principal.UserID, at, at.Add(time.Hour))
	if err != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "upload_lease").Wrap(err)
	}

	if _, err = s.resources.ReserveUpload(ctx, &store.ExamResourceUploadReservation{
		ExamID:                command.ExamID,
		ActorUserID:           principal.UserID,
		ManagerOverride:       authorization.override,
		ExpectedDraftRevision: command.ExpectedDraftRevision,
		ResourceID:            command.ResourceID,
		EntryID:               current.Resource.FileEntryID,
		Revision:              revision,
		Lease:                 lease,
		Replacement:           true,
	}); err != nil {
		return store.ExamResourceRecord{}, storeErrors.Translate(err)
	}

	rendition, err := s.content.StoreExamResource(ctx, revisionID, command.MediaType, command.Body, command.Size, at)
	if err != nil {
		return store.ExamResourceRecord{}, mapContentError(err)
	}

	if subtle.ConstantTimeCompare([]byte(rendition.SHA256), []byte(command.ExpectedSHA256)) != 1 {
		_ = s.content.RemoveExamResource(ctx, rendition.RevisionID, rendition.ID)
		return store.ExamResourceRecord{}, apperror.New("exam.resource.content_invalid").Wrap(errors.New("content checksum mismatch"))
	}

	updated := *current.Resource
	if _, err = updated.ReplaceContent(revisionID, at); err != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "content").Wrap(err)
	}

	return s.finalizeUpload(
		ctx,
		inv,
		authorization,
		&updated,
		leaseID,
		&rendition,
		command.ExpectedDraftRevision,
		commandIdempotency,
		"replace_content",
	)
}

func (s *Service) finalizeUpload(
	ctx context.Context,
	inv invocation.Invocation,
	authorization authorizationDecision,
	resource *model.ExamResource,
	leaseID model.UploadLeaseID,
	rendition *model.FileRendition,
	expected int64,
	commandIdempotency *store.CommandIdempotency,
	operation string,
) (store.ExamResourceRecord, error) {
	action := model.ActionExamManage
	if authorization.override {
		action = model.ActionExamManageOverride
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   model.Resource{Type: model.ResourceExam, ID: resource.ExamID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.academicUnitID.String(),
		Operation:  operation,
		Value: map[string]any{
			"exam_id":          resource.ExamID.String(),
			"exam_resource_id": resource.ID.String(),
			"file_revision_id": resource.SelectedFileRevisionID.String(),
		},
	})
	if err != nil {
		_ = s.content.RemoveExamResource(ctx, rendition.RevisionID, rendition.ID)
		return store.ExamResourceRecord{}, err
	}

	at := model.TimeUTC(s.now())
	result, err := s.resources.FinalizeUpload(
		ctx,
		&store.ExamResourceUploadFinalization{
			ExamID:                resource.ExamID,
			ActorUserID:           inv.Principal().UserID,
			ManagerOverride:       authorization.override,
			ExpectedDraftRevision: expected,
			Resource:              resource,
			LeaseID:               leaseID,
			Rendition:             rendition,
			ChangedAt:             at,
			AuditEventID:          auditID,
			AuditAt:               at,
		},
		commandIdempotency,
	)
	if err != nil {
		return store.ExamResourceRecord{}, s.failAudit(ctx, auditID, err)
	}
	if result == nil || result.Value == nil {
		return store.ExamResourceRecord{}, apperror.New("exam.resource.unavailable").Wrap(errors.New("missing resource outcome"))
	}

	s.publishChanged(ctx, result)
	return *result.Value, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
