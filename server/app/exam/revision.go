// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type PublishRevisionCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	IdempotencyKey        string
}

type ListRevisionsQuery struct {
	ExamID           model.ExamID
	BeforeNumber     int64
	BeforeRevisionID model.ExamRevisionID
	Limit            int
}

// RevisionPage is one page of Exam Revisions, newest first; HasMore reports
// that an older page exists.
type RevisionPage struct {
	Items   []store.ExamRevisionSummary
	HasMore bool
}

// PublishExamRevision idempotently publishes the current Exam Draft as a new
// immutable Exam Revision, fenced by ExpectedDraftRevision.
func (p *Publication) PublishExamRevision(
	ctx context.Context,
	inv invocation.Invocation,
	command PublishRevisionCommand,
) (_ store.ExamRevisionSummary, resultErr error) {
	defer func() { p.recordOperational("publish_revision", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedDraftRevision < 1 {
		return store.ExamRevisionSummary{}, apperror.Invalid("exam.invalid", "publication")
	}

	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationPublishRevision, command.IdempotencyKey, struct {
		ExamID                string `json:"exam_id"`
		ExpectedDraftRevision int64  `json:"expected_draft_revision"`
	}{command.ExamID.String(), command.ExpectedDraftRevision})
	if err != nil {
		return store.ExamRevisionSummary{}, err
	}

	at := model.TimeUTC(p.now())
	revisionID := p.newID()
	if !revisionID.IsValid() {
		return store.ExamRevisionSummary{}, apperror.Invalid("exam.invalid", "revision_id")
	}

	examAccess, err := p.exams.Access(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return store.ExamRevisionSummary{}, examStoreErrors.Translate(err)
	}
	if !examAccess.Consistent() {
		return store.ExamRevisionSummary{}, apperror.New("exam.unavailable").Wrap(errors.New("Exam access projection is incomplete"))
	}

	action, err := manageraccess.SelectAction(
		ctx,
		p.academicUnitMembers,
		principal.UserID,
		examAccess,
		at,
		model.ActionExamPublish,
		model.ActionExamPublishOverride,
	)
	if err != nil {
		return store.ExamRevisionSummary{}, apperror.New("exam.unavailable").Wrap(err)
	}

	resource := model.Resource{Type: model.ResourceExam, ID: command.ExamID.String()}
	if err = p.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return store.ExamRevisionSummary{}, err
	}

	auditID, err := p.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    examAccess.Exam.AcademicUnitID.String(),
		Operation:  "publish",
		Value: map[string]any{
			"exam_id":                 command.ExamID.String(),
			"expected_draft_revision": command.ExpectedDraftRevision,
		},
	})
	if err != nil {
		return store.ExamRevisionSummary{}, err
	}

	result, err := p.revisions.Publish(
		ctx,
		&store.ExamRevisionPublication{
			InstitutionOrigin:     p.institutionOrigin,
			RevisionID:            revisionID,
			ExamID:                command.ExamID,
			ActorUserID:           principal.UserID,
			ManagerOverride:       action == model.ActionExamPublishOverride,
			ExpectedDraftRevision: command.ExpectedDraftRevision,
			Kind:                  model.ExamRevisionPublicationStandard,
			PublishedAt:           at,
			AuditEventID:          auditID,
			AuditAt:               at,
		},
		commandIdempotency,
	)
	if err != nil {
		mapped := examStoreErrors.Translate(err)
		if auditErr := p.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return store.ExamRevisionSummary{}, errors.Join(mapped, auditErr)
		}

		return store.ExamRevisionSummary{}, mapped
	}

	if result == nil || result.Revision == nil {
		return store.ExamRevisionSummary{}, apperror.New("exam.unavailable").Wrap(errors.New("Exam Revision store returned no result"))
	}

	if !result.Replayed {
		if effectErr := p.publishRevisionPublished(ctx, *result.Revision); effectErr != nil {
			p.publisher.Report(ctx, "exam_revision_published", effectErr)
		}
	}

	return *result.Revision, nil
}

// GetExamRevision returns the summary of one Exam Revision to a caller
// authorized to view the Exam.
func (p *Publication) GetExamRevision(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	revisionID model.ExamRevisionID,
) (_ store.ExamRevisionSummary, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := p.authorizeView(ctx, inv, examID); err != nil {
		return store.ExamRevisionSummary{}, err
	}

	revision, err := p.revisions.GetSummary(ctx, examID, revisionID)
	if err != nil {
		return store.ExamRevisionSummary{}, examStoreErrors.Translate(err)
	}
	if revision == nil {
		return store.ExamRevisionSummary{}, apperror.New("exam.unavailable").Wrap(errors.New("Exam Revision store returned no summary"))
	}

	return *revision, nil
}

// ListExamRevisions returns one page of an Exam's Exam Revisions, newest
// first, to a caller authorized to view the Exam.
func (p *Publication) ListExamRevisions(
	ctx context.Context,
	inv invocation.Invocation,
	query ListRevisionsQuery,
) (_ RevisionPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := p.authorizeView(ctx, inv, query.ExamID); err != nil {
		return RevisionPage{}, err
	}
	if query.Limit < 1 || query.Limit > 200 {
		return RevisionPage{}, apperror.Invalid("exam.invalid", "limit")
	}

	items, err := p.revisions.List(ctx, store.ExamRevisionListOptions{
		ExamID:           query.ExamID,
		BeforeNumber:     query.BeforeNumber,
		BeforeRevisionID: query.BeforeRevisionID,
		Limit:            query.Limit + 1,
	})
	if err != nil {
		return RevisionPage{}, examStoreErrors.Translate(err)
	}

	page := RevisionPage{Items: items, HasMore: len(items) > query.Limit}
	if page.HasMore {
		page.Items = page.Items[:query.Limit]
	}

	return page, nil
}

func (p *Publication) authorizeView(ctx context.Context, inv invocation.Invocation, examID model.ExamID) error {
	principal := inv.Principal()
	if principal.Validate() != nil || !examID.IsValid() {
		return apperror.Invalid("exam.invalid", "exam_id")
	}

	examAccess, err := p.exams.Access(ctx, examID, principal.UserID)
	if err != nil {
		return examStoreErrors.Translate(err)
	}
	if !examAccess.Consistent() {
		return apperror.New("exam.unavailable").Wrap(errors.New("Exam access projection is incomplete"))
	}

	action, err := manageraccess.SelectAction(
		ctx,
		p.academicUnitMembers,
		principal.UserID,
		examAccess,
		model.TimeUTC(p.now()),
		model.ActionExamView,
		model.ActionExamViewOverride,
	)
	if err != nil {
		return apperror.New("exam.unavailable").Wrap(err)
	}

	return p.authorizer.Authorize(ctx, inv, action, model.Resource{Type: model.ResourceExam, ID: examID.String()})
}
