// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type accessAcademicPeriodStoreFake struct {
	period  *model.AcademicPeriod
	periods map[string]*model.AcademicPeriod
	gets    int
	err     error
}

func (s *accessAcademicPeriodStoreFake) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	s.gets++
	if s.err != nil {
		return nil, s.err
	}
	if period := s.periods[id.String()]; period != nil {
		return period, nil
	}
	if s.period == nil {
		return nil, store.NewErrNotFound("academic_period", "missing")
	}
	return s.period, nil
}

type accessExamAuthoringStoreFake struct {
	store.ExamAuthoringStore
	exam *model.Exam
}

func (s *accessExamAuthoringStoreFake) Resolve(context.Context, model.ExamID) (*model.Exam, error) {
	return s.exam, nil
}

type accessExamSittingStoreFake struct {
	store.ExamSittingStore
	snapshot *store.ExamSittingSnapshot
}

type accessExamSubmissionStoreFake struct {
	store.ExamSubmissionStore
	authorization *store.ExamSubmissionAuthorization
}

func (s *accessExamSubmissionStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	if s.authorization == nil {
		return nil, store.NewErrNotFound("submission", "")
	}
	return s.authorization, nil
}

func (s *accessExamSittingStoreFake) Resolve(context.Context, model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if s.snapshot == nil {
		return nil, store.NewErrNotFound("exam_sitting", "")
	}
	return s.snapshot, nil
}
