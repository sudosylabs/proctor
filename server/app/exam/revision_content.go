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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// RevisionContent opens the bytes a published Revision froze. It checks no
// access itself; the caller authorizes first.
type RevisionContent interface {
	OpenExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error)
	OpenStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID) (io.ReadCloser, error)
}

// RevisionText is what a Revision froze besides its files: the title,
// instructions and every policy.
type RevisionText struct {
	RevisionID           model.ExamRevisionID
	ExamID               model.ExamID
	Number               int64
	Title                string
	InstructionsMarkdown string
	Policy               model.ExamPolicySet
	BrowserPolicy        model.BrowserPolicy
}

// OpenedRevisionResource is one frozen Resource with an open stream of its
// bytes. The caller closes Body.
type OpenedRevisionResource struct {
	Resource model.ExamRevisionResource
	Body     io.ReadCloser
}

// OpenedRevisionStarterFile is one frozen Starter Workspace file with an open
// stream of its bytes. The caller closes Body.
type OpenedRevisionStarterFile struct {
	Entry model.ExamRevisionStarterWorkspaceEntry
	Body  io.ReadCloser
}

// GetExamRevisionText returns a Revision's title, instructions and policies.
// Like reading the Draft, it needs permission to view the exam.
func (p *Publication) GetExamRevisionText(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	revisionID model.ExamRevisionID,
) (_ RevisionText, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	revision, err := p.revisionSnapshot(ctx, inv, examID, revisionID)
	if err != nil {
		return RevisionText{}, err
	}

	policy, err := model.DecodeExamPolicySet(revision.Policy.Bytes)
	if err != nil {
		return RevisionText{}, apperror.New("exam.unavailable").Wrap(err)
	}

	return RevisionText{
		RevisionID:           revision.ID,
		ExamID:               revision.ExamID,
		Number:               revision.Number,
		Title:                revision.Title,
		InstructionsMarkdown: revision.InstructionsMarkdown,
		Policy:               policy,
		BrowserPolicy:        revision.BrowserPolicy.Clone(),
	}, nil
}

// ListExamRevisionResources returns a Revision's Resources in position order.
// The capacity policy the Revision froze bounds them.
func (p *Publication) ListExamRevisionResources(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	revisionID model.ExamRevisionID,
) (_ []model.ExamRevisionResource, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	revision, err := p.revisionSnapshot(ctx, inv, examID, revisionID)
	if err != nil {
		return nil, err
	}

	return append([]model.ExamRevisionResource{}, revision.Resources...), nil
}

// OpenExamRevisionResource opens one Resource as the Revision froze it.
func (p *Publication) OpenExamRevisionResource(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	revisionID model.ExamRevisionID,
	resourceID model.ExamResourceID,
) (_ OpenedRevisionResource, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !resourceID.IsValid() {
		return OpenedRevisionResource{}, apperror.Invalid("exam.invalid", "exam_resource_id")
	}

	revision, err := p.revisionSnapshot(ctx, inv, examID, revisionID)
	if err != nil {
		return OpenedRevisionResource{}, err
	}

	for _, frozen := range revision.Resources {
		if frozen.ResourceID != resourceID {
			continue
		}

		body, openErr := p.content.OpenExamResource(ctx, frozen.FileRevisionID, frozen.RenditionID)
		if openErr != nil {
			return OpenedRevisionResource{}, apperror.New("exam.unavailable").Wrap(openErr)
		}
		return OpenedRevisionResource{Resource: frozen, Body: body}, nil
	}

	return OpenedRevisionResource{}, apperror.New("exam.not_found")
}

// ListExamRevisionStarterWorkspace returns a Revision's Starter Workspace in
// path order. The capacity policy the Revision froze bounds it.
func (p *Publication) ListExamRevisionStarterWorkspace(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	revisionID model.ExamRevisionID,
) (_ []model.ExamRevisionStarterWorkspaceEntry, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	revision, err := p.revisionSnapshot(ctx, inv, examID, revisionID)
	if err != nil {
		return nil, err
	}

	return append([]model.ExamRevisionStarterWorkspaceEntry{}, revision.StarterWorkspace...), nil
}

// OpenExamRevisionStarterFile opens one Starter Workspace file as the
// Revision froze it. A directory has no content and is not found.
func (p *Publication) OpenExamRevisionStarterFile(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	revisionID model.ExamRevisionID,
	entryID model.StarterWorkspaceEntryID,
) (_ OpenedRevisionStarterFile, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !entryID.IsValid() {
		return OpenedRevisionStarterFile{}, apperror.Invalid("exam.invalid", "starter_workspace_entry_id")
	}

	revision, err := p.revisionSnapshot(ctx, inv, examID, revisionID)
	if err != nil {
		return OpenedRevisionStarterFile{}, err
	}

	for _, entry := range revision.StarterWorkspace {
		if entry.EntryID != entryID {
			continue
		}
		if entry.Kind != model.StarterWorkspaceEntryFile {
			break
		}

		body, openErr := p.content.OpenStarterWorkspaceObject(ctx, entry.ObjectID)
		if openErr != nil {
			return OpenedRevisionStarterFile{}, apperror.New("exam.unavailable").Wrap(openErr)
		}
		return OpenedRevisionStarterFile{Entry: entry, Body: body}, nil
	}

	return OpenedRevisionStarterFile{}, apperror.New("exam.not_found")
}

// revisionSnapshot authorizes a view of the exam, then reads the Revision.
func (p *Publication) revisionSnapshot(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	revisionID model.ExamRevisionID,
) (*model.ExamRevision, error) {
	if !revisionID.IsValid() {
		return nil, apperror.Invalid("exam.invalid", "exam_revision_id")
	}
	if err := p.authorizeView(ctx, inv, examID); err != nil {
		return nil, err
	}

	revision, err := p.revisions.GetSnapshot(ctx, examID, revisionID)
	if err != nil {
		return nil, examStoreErrors.Translate(err)
	}
	if revision == nil || revision.ID != revisionID || revision.ExamID != examID {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Exam Revision store returned a mismatched snapshot"))
	}

	return revision, nil
}
