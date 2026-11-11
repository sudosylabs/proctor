// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"errors"
	"io"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// OpenedFile is an open stream of one Starter Workspace file with its stored
// metadata. The caller closes Body.
type OpenedFile struct {
	Body           io.ReadCloser
	MediaType      string
	SizeBytes      int64
	SHA256         string
	ContentVersion model.WorkspaceContentVersion
}

// ListExamStarterWorkspace returns the entries of the Exam Draft's Starter
// Workspace to a caller authorized to view the Exam.
func (s *Service) ListExamStarterWorkspace(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
) (_ []store.ExamStarterWorkspaceItem, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if _, err := s.authorizeExam(ctx, inv, examID, false); err != nil {
		return nil, err
	}

	items, err := s.workspaces.List(ctx, examID)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}

	return items, nil
}

// OpenExamStarterWorkspaceFile opens one file of the Exam Draft's Starter
// Workspace for a caller authorized to view the Exam. The caller must close
// the body.
func (s *Service) OpenExamStarterWorkspaceFile(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	entryID model.StarterWorkspaceEntryID,
) (_ OpenedFile, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !entryID.IsValid() {
		return OpenedFile{}, apperror.Invalid("exam.starter_workspace.invalid", "entry_id")
	}
	if _, err := s.authorizeExam(ctx, inv, examID, false); err != nil {
		return OpenedFile{}, err
	}

	item, err := s.workspaces.GetFile(ctx, examID, entryID)
	if err != nil {
		return OpenedFile{}, storeErrors.Translate(err)
	}

	if item == nil || item.Object == nil {
		return OpenedFile{}, apperror.New("exam.starter_workspace.unavailable").Wrap(
			errors.New("Starter Workspace Store returned incomplete file metadata"),
		)
	}

	body, err := s.content.OpenStarterWorkspaceObject(ctx, item.Object.ID)
	if err != nil {
		return OpenedFile{}, apperror.New("exam.starter_workspace.unavailable").Wrap(err)
	}

	return OpenedFile{
		Body:           body,
		MediaType:      item.Object.MediaType,
		SizeBytes:      item.Object.SizeBytes,
		SHA256:         item.Object.SHA256,
		ContentVersion: item.Object.ContentVersion,
	}, nil
}
