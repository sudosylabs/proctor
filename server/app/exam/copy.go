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
	"io"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// copyReservationLease is how long a copy may take to write its bytes before
// its reservation lapses and the purge job removes it.
const copyReservationLease = time.Hour

const idempotencyOperationCopy = "exam.copy"

// CopyContent reads a source exam's bytes and writes the copy's under new
// keys. It checks no access itself.
type CopyContent interface {
	OpenExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error)
	StoreExamResource(context.Context, model.FileRevisionID, model.ExamResourceMediaType, io.Reader, int64, time.Time) (model.FileRendition, error)
	OpenStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID) (io.ReadCloser, error)
	StageStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID, io.Reader, int64, string) (*model.StarterWorkspaceContent, error)
}

// Copier copies an exam's current draft into a new exam.
type Copier struct {
	exams               store.ExamAuthoringStore
	resources           store.ExamResourceStore
	starterWorkspaces   store.ExamStarterWorkspaceStore
	academicUnitMembers store.AcademicUnitMemberStore
	content             CopyContent
	authorizer          *access.Authorizer
	auditor             access.MutationAuditAdapter
	publisher           realtime.Publisher
	now                 func() time.Time
	newID               func() model.ExamID
}

// CopierDependencies are the collaborators of NewCopier.
type CopierDependencies struct {
	Persistence       store.ExamAuthoringStore
	Resources         store.ExamResourceStore
	StarterWorkspaces store.ExamStarterWorkspaceStore
	Memberships       store.AcademicUnitMemberStore
	Content           CopyContent
	Authorizer        *access.Authorizer
	Auditor           *access.Auditor
	Publisher         realtime.Publisher
	Now               func() time.Time
	NewID             func() model.ExamID
}

// NewCopier returns a Copier, or an error when a dependency is missing.
func NewCopier(deps CopierDependencies) (*Copier, error) {
	if deps.Persistence == nil || deps.Resources == nil || deps.StarterWorkspaces == nil || deps.Memberships == nil ||
		deps.Content == nil || deps.Authorizer == nil || deps.Auditor == nil || deps.Publisher == nil ||
		deps.Now == nil || deps.NewID == nil {
		return nil, errors.New("exam copy dependencies are required")
	}
	return &Copier{
		exams: deps.Persistence, resources: deps.Resources, starterWorkspaces: deps.StarterWorkspaces,
		academicUnitMembers: deps.Memberships, content: deps.Content, authorizer: deps.Authorizer,
		auditor: deps.Auditor.MutationAudit(), publisher: deps.Publisher, now: deps.Now, newID: deps.NewID,
	}, nil
}

// CopyCommand names the exam to copy, the copy's title and the Academic Unit
// that will own it.
type CopyCommand struct {
	SourceExamID   model.ExamID
	Title          string
	AcademicUnitID model.AcademicUnitID
	IdempotencyKey string
}

// CopyExam copies the source's current draft, with its instructions, every
// policy, its Resources and its Starter Workspace, into a new exam the caller
// owns. An archived source copies too. Revisions, sittings, submissions,
// managers and holds are never copied. It needs manager access on the source
// and exam.create in the target unit.
//
// The bytes are copied under new keys between a reservation and the commit:
// a copy interrupted midway lapses, and the file purge job removes what it
// had written.
func (c *Copier) CopyExam(ctx context.Context, inv invocation.Invocation, command CopyCommand) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !command.SourceExamID.IsValid() || !command.AcademicUnitID.IsValid() {
		return View{}, apperror.Invalid("exam.invalid", "copy")
	}

	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationCopy, command.IdempotencyKey, struct {
		SourceExamID   string `json:"source_exam_id"`
		Title          string `json:"title"`
		AcademicUnitID string `json:"academic_unit_id"`
	}{command.SourceExamID.String(), command.Title, command.AcademicUnitID.String()})
	if err != nil {
		return View{}, err
	}

	at := model.TimeUTC(c.now())
	sourceAction, sourceUnitID, err := c.authorizeSource(ctx, inv, command.SourceExamID, at)
	if err != nil {
		return View{}, err
	}
	createAction, err := c.authorizeTarget(ctx, inv, command.AcademicUnitID, at)
	if err != nil {
		return View{}, err
	}

	source, err := c.exams.Get(ctx, command.SourceExamID, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}
	resources, err := c.resources.List(ctx, command.SourceExamID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}
	starter, err := c.starterWorkspaces.List(ctx, command.SourceExamID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}
	if !source.Consistent() || source.Exam.ID != command.SourceExamID {
		return View{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned a mismatched copy source"))
	}

	plan, err := c.planCopy(principal.UserID, command, source.Draft, resources, starter, at)
	if err != nil {
		return View{}, err
	}
	if err = c.reserve(ctx, principal.UserID, plan, at); err != nil {
		return View{}, err
	}

	createAudit, err := c.auditor.Begin(ctx, access.Mutation{
		Invocation: inv, Action: createAction,
		Resource:  model.Resource{Type: model.ResourceAcademicUnit, ID: command.AcademicUnitID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: command.AcademicUnitID.String(), Operation: "copy",
		Value: map[string]any{"exam_id": plan.exam.ID.String(), "academic_unit_id": command.AcademicUnitID.String(),
			"source_exam_id": command.SourceExamID.String(), "creator_user_id": principal.UserID.String()},
	})
	if err != nil {
		return View{}, err
	}
	sourceAudit, err := c.auditor.Begin(ctx, access.Mutation{
		Invocation: inv, Action: sourceAction,
		Resource:  model.Resource{Type: model.ResourceExam, ID: command.SourceExamID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: sourceUnitID.String(), Operation: "copy_source",
		Value: map[string]any{"exam_id": command.SourceExamID.String(), "copy_exam_id": plan.exam.ID.String()},
	})
	if err != nil {
		return View{}, c.failAudits(ctx, err, createAudit)
	}

	if err = c.copyBytes(ctx, plan, at); err != nil {
		return View{}, c.failAudits(ctx, err, createAudit, sourceAudit)
	}

	result, err := c.exams.CommitCopy(ctx, &store.ExamCopyCommit{
		SourceExamID: command.SourceExamID, ExamID: plan.exam.ID, ActorUserID: principal.UserID,
		SourceManagerOverride: sourceAction == model.ActionExamManageOverride,
		Resources:             plan.committedResources(), StarterEntries: plan.committedEntries(),
		ChangedAt: at, CreateAuditEventID: createAudit, SourceAuditEventID: sourceAudit, AuditAt: at,
	}, commandIdempotency)
	if err != nil {
		return View{}, c.failAudits(ctx, examStoreErrors.Translate(err), createAudit, sourceAudit)
	}
	if result == nil || result.Value == nil || !result.Value.Consistent() {
		return View{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned no copy"))
	}

	if !result.Replayed {
		event, eventErr := realtime.NewExamCreatedEvent(result.Value.Exam.ID, result.Value.Exam.AcademicUnitID)
		if eventErr == nil {
			eventErr = c.publisher.Publish(ctx, event)
		}
		if eventErr != nil {
			c.publisher.Report(ctx, "exam_created", eventErr)
		}
	}

	return projectView(result.Value), nil
}

func (c *Copier) authorizeSource(
	ctx context.Context,
	inv invocation.Invocation,
	sourceID model.ExamID,
	at time.Time,
) (model.Action, model.AcademicUnitID, error) {
	principal := inv.Principal()
	sourceAccess, err := c.exams.Access(ctx, sourceID, principal.UserID)
	if err != nil {
		return "", "", examStoreErrors.Translate(err)
	}
	action, err := manageraccess.SelectAction(ctx, c.academicUnitMembers, principal.UserID, sourceAccess, at,
		model.ActionExamManage, model.ActionExamManageOverride)
	if err != nil {
		return "", "", apperror.New("exam.unavailable").Wrap(err)
	}
	if err = c.authorizer.Authorize(ctx, inv, action, model.Resource{Type: model.ResourceExam, ID: sourceID.String()}); err != nil {
		return "", "", err
	}
	return action, sourceAccess.Exam.AcademicUnitID, nil
}

// authorizeTarget applies exam creation's rule: exam.create for a member of
// the unit, the override for anyone else.
func (c *Copier) authorizeTarget(ctx context.Context, inv invocation.Invocation, unitID model.AcademicUnitID, at time.Time) (model.Action, error) {
	ordinary, err := manageraccess.HasCurrentMembership(ctx, c.academicUnitMembers, inv.Principal().UserID, unitID, at)
	if err != nil {
		return "", apperror.New("exam.unavailable").Wrap(err)
	}
	action := model.ActionExamCreateOverride
	if ordinary {
		action = model.ActionExamCreate
	}
	if err = c.authorizer.Authorize(ctx, inv, action, model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}); err != nil {
		return "", err
	}
	return action, nil
}

func (c *Copier) failAudits(ctx context.Context, err error, auditIDs ...model.AuditEventID) error {
	code := apperror.CodeOf(err, "exam.unavailable")
	errs := []error{err}
	for _, auditID := range auditIDs {
		if auditErr := c.auditor.Fail(ctx, auditID, code); auditErr != nil {
			errs = append(errs, auditErr)
		}
	}
	return errors.Join(errs...)
}
