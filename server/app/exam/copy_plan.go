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
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// copyPlan is the new exam and, for each source Resource and Starter
// Workspace entry, where its copy will live.
type copyPlan struct {
	exam      *model.Exam
	draft     *model.ExamDraft
	manager   *model.ExamManager
	resources []copiedResource
	entries   []copiedEntry
}

type copiedResource struct {
	source    store.ExamResourceRecord
	resource  *model.ExamResource
	entry     *model.FileEntry
	revision  *model.FileRevision
	lease     *model.UploadLease
	rendition *model.FileRendition
}

type copiedEntry struct {
	source  store.ExamStarterWorkspaceItem
	entry   *model.StarterWorkspaceEntry
	object  *model.StarterWorkspaceObject
	version model.WorkspaceContentVersion
	content *model.StarterWorkspaceContent
}

func (c *Copier) planCopy(
	actorID model.UserID,
	command CopyCommand,
	source *model.ExamDraft,
	resources []store.ExamResourceRecord,
	starter []store.ExamStarterWorkspaceItem,
	at time.Time,
) (*copyPlan, error) {
	examID := c.newID()
	if !examID.IsValid() {
		return nil, apperror.Invalid("exam.invalid", "exam_id")
	}
	exam, err := model.NewExam(examID, command.AcademicUnitID, actorID, at)
	if err != nil {
		return nil, apperror.Invalid("exam.invalid", "exam").Wrap(err)
	}
	draft, err := model.NewExamDraft(examID, command.Title, source.InstructionsMarkdown, source.Policy, actorID, at)
	if err != nil {
		return nil, apperror.Invalid("exam.invalid", "draft").Wrap(err)
	}
	draft.BrowserPolicy = source.BrowserPolicy.Clone()
	manager, err := model.NewExamManager(examID, actorID, actorID, at)
	if err != nil {
		return nil, apperror.Invalid("exam.invalid", "manager").Wrap(err)
	}

	plan := &copyPlan{exam: exam, draft: draft, manager: manager}
	expires := at.Add(copyReservationLease)
	for position, record := range resources {
		if !record.Consistent() {
			return nil, apperror.New("exam.unavailable").Wrap(errors.New("inconsistent source Resource"))
		}
		entryID, revisionID := model.NewFileEntryID(), model.NewFileRevisionID()
		entry, entryErr := model.NewFileEntryForPurpose(entryID, model.FilePurposeExamResource, model.FileIndexingNone, at)
		revision, revisionErr := model.NewFileRevision(revisionID, entryID, model.FileAvailabilityPending, model.FileIndexingNotRequired, at)
		lease, leaseErr := model.NewUploadLease(model.NewUploadLeaseID(), revisionID, actorID, at, expires)
		resource, resourceErr := model.NewExamResource(model.NewExamResourceID(), examID, entryID, revisionID,
			record.Resource.DisplayName, record.Resource.DescriptionMarkdown, position, at)
		if err = errors.Join(entryErr, revisionErr, leaseErr, resourceErr); err != nil {
			return nil, apperror.Invalid("exam.invalid", "resource").Wrap(err)
		}
		plan.resources = append(plan.resources, copiedResource{source: record, resource: resource, entry: entry,
			revision: revision, lease: lease})
	}
	for _, item := range starter {
		copied := copiedEntry{source: item}
		if item.Entry.Kind == model.StarterWorkspaceEntryFile {
			if item.Object == nil {
				return nil, apperror.New("exam.unavailable").Wrap(errors.New("source Starter Workspace file has no object"))
			}
			copied.object, err = model.NewStagedStarterWorkspaceObject(model.NewStarterWorkspaceObjectID(), examID, actorID, at, expires)
			if err != nil {
				return nil, apperror.Invalid("exam.invalid", "starter_workspace").Wrap(err)
			}
			copied.version = model.NewWorkspaceContentVersion()
			copied.entry, err = model.NewStarterWorkspaceFile(model.NewStarterWorkspaceEntryID(), examID, item.Entry.Path, copied.object.ID, at)
		} else {
			copied.entry, err = model.NewStarterWorkspaceDirectory(model.NewStarterWorkspaceEntryID(), examID, item.Entry.Path, at)
		}
		if err != nil {
			return nil, apperror.Invalid("exam.invalid", "starter_workspace").Wrap(err)
		}
		plan.entries = append(plan.entries, copied)
	}
	return plan, nil
}

// reserve records the invisible copy and its staged objects, then each
// Resource upload, so the purge job can find whatever bytes follow.
func (c *Copier) reserve(ctx context.Context, actorID model.UserID, plan *copyPlan, at time.Time) error {
	objects := make([]*model.StarterWorkspaceObject, 0, len(plan.entries))
	for _, copied := range plan.entries {
		if copied.object != nil {
			objects = append(objects, copied.object)
		}
	}
	if err := c.exams.ReserveCopy(ctx, &store.ExamCopyReservation{
		Exam: plan.exam, Draft: plan.draft, Manager: plan.manager,
		CopyingUntil: at.Add(copyReservationLease), StarterObjects: objects,
	}); err != nil {
		return examStoreErrors.Translate(err)
	}
	for _, copied := range plan.resources {
		if _, err := c.resources.ReserveUpload(ctx, &store.ExamResourceUploadReservation{
			ExamID: plan.exam.ID, ActorUserID: actorID, ExpectedDraftRevision: plan.draft.Revision,
			ResourceID: copied.resource.ID, Entry: copied.entry, Revision: copied.revision, Lease: copied.lease,
		}); err != nil {
			return examStoreErrors.Translate(err)
		}
	}
	return nil
}

// copyBytes writes each copy under its new key. Resources go through
// StoreExamResource, which validates them as an upload would. A copy whose
// bytes differ from the source's digest is refused.
func (c *Copier) copyBytes(ctx context.Context, plan *copyPlan, at time.Time) error {
	for index := range plan.resources {
		copied := &plan.resources[index]
		source := copied.source.Rendition
		body, err := c.content.OpenExamResource(ctx, copied.source.Resource.SelectedFileRevisionID, source.ID)
		if err != nil {
			return apperror.New("exam.unavailable").Wrap(err)
		}
		rendition, err := c.content.StoreExamResource(ctx, copied.revision.ID, model.ExamResourceMediaType(source.MediaType),
			body, source.Size, at)
		_ = body.Close()
		if err != nil {
			return apperror.New("exam.unavailable").Wrap(err)
		}
		if rendition.SHA256 != source.SHA256 {
			return apperror.New("exam.unavailable").Wrap(errors.New("copied Resource differs from its source"))
		}
		copied.rendition = &rendition
	}
	for index := range plan.entries {
		copied := &plan.entries[index]
		if copied.object == nil {
			continue
		}
		source := copied.source.Object
		body, err := c.content.OpenStarterWorkspaceObject(ctx, source.ID)
		if err != nil {
			return apperror.New("exam.unavailable").Wrap(err)
		}
		content, err := c.content.StageStarterWorkspaceObject(ctx, copied.object.ID, body, source.SizeBytes, source.MediaType)
		_ = body.Close()
		if err != nil {
			return apperror.New("exam.unavailable").Wrap(err)
		}
		if content == nil || content.SHA256 != source.SHA256 {
			return apperror.New("exam.unavailable").Wrap(errors.New("copied Starter Workspace file differs from its source"))
		}
		copied.content = content
	}
	return nil
}

func (p *copyPlan) committedResources() []store.ExamCopyResource {
	resources := make([]store.ExamCopyResource, 0, len(p.resources))
	for _, copied := range p.resources {
		resources = append(resources, store.ExamCopyResource{Resource: copied.resource, LeaseID: copied.lease.ID, Rendition: copied.rendition})
	}
	return resources
}

func (p *copyPlan) committedEntries() []store.ExamCopyStarterEntry {
	entries := make([]store.ExamCopyStarterEntry, 0, len(p.entries))
	for _, copied := range p.entries {
		entry := store.ExamCopyStarterEntry{Entry: copied.entry}
		if copied.content != nil {
			entry.ContentVersion, entry.MediaType = copied.version, copied.content.MediaType
			entry.SizeBytes, entry.SHA256 = copied.content.SizeBytes, copied.content.SHA256
		}
		entries = append(entries, entry)
	}
	return entries
}
