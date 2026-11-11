// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"errors"
	"io"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Opened is one Exam Resource with an open stream of its selected rendition.
// The caller closes Body.
type Opened struct {
	Record store.ExamResourceRecord
	Body   io.ReadCloser
}

// ListExamResources returns the Exam Draft's Exam Resources to a caller
// authorized to view the Exam.
func (s *Service) ListExamResources(
	ctx context.Context,
	invocation invocation.Invocation,
	examID model.ExamID,
) (_ []store.ExamResourceRecord, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !examID.IsValid() {
		return nil, apperror.Invalid("exam.resource.invalid", "exam_id")
	}
	if _, err := s.authorizeExam(ctx, invocation, examID, false); err != nil {
		return nil, err
	}

	items, err := s.resources.List(ctx, examID)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}

	return items, nil
}

// OpenExamResource opens the selected content of one Exam Draft Exam Resource
// for a caller authorized to view the Exam. The caller must close the body.
func (s *Service) OpenExamResource(
	ctx context.Context,
	invocation invocation.Invocation,
	examID model.ExamID,
	resourceID model.ExamResourceID,
) (_ Opened, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !examID.IsValid() || !resourceID.IsValid() {
		return Opened{}, apperror.Invalid("exam.resource.invalid", "identity")
	}
	if _, err := s.authorizeExam(ctx, invocation, examID, false); err != nil {
		return Opened{}, err
	}
	record, err := s.resources.Get(ctx, examID, resourceID)
	if err != nil {
		return Opened{}, storeErrors.Translate(err)
	}
	if !record.Consistent() {
		return Opened{}, apperror.New("exam.resource.unavailable").Wrap(errors.New("incomplete resource record"))
	}
	body, err := s.content.OpenExamResource(ctx, record.Resource.SelectedFileRevisionID, record.Rendition.ID)
	if err != nil {
		return Opened{}, apperror.New("exam.resource.unavailable").Wrap(err)
	}
	return Opened{Record: *record, Body: body}, nil
}
