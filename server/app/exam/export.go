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
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExportArchiveInput is what ExportContent writes into an archive. The adapter
// verifies the original bytes and the finished archive but never chooses what
// is in scope.
type ExportArchiveInput struct {
	ExportID  model.ExamExportID
	AttemptID model.JobAttemptID
	Records   []byte
	Files     []ExportArchiveFile
}

// ExportArchiveFile names one sealed submission file to include in an archive.
type ExportArchiveFile struct {
	SubmissionID model.SubmissionID
	Entry        model.ExamSubmissionManifestEntry
}

// ExportArchiveContent describes the verified archive ExportContent wrote.
type ExportArchiveContent struct {
	SizeBytes int64
	SHA256    string
}

// ExportContent stores export archives, keyed by export and job attempt.
type ExportContent interface {
	// A failed write whose backend outcome is unknown must return an error
	// implementing ExamExportWriteUncertain(); its artifact reservation must
	// stay even after one absence check.
	BuildExamExport(context.Context, ExportArchiveInput) (*ExportArchiveContent, error)
	OpenExamExport(context.Context, model.ExamExportID, model.JobAttemptID) (io.ReadCloser, error)
	PurgeExamExport(context.Context, model.ExamExportID, model.JobAttemptID) error
}

// Exports requests, reads and builds examination exports.
type Exports struct {
	exports             store.ExamExportStore
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer *access.Authorizer
	auditor    access.MutationAuditAdapter
	content    ExportContent
	now        func() time.Time
	newID      func() model.ExamExportID
	newJobID   func() model.JobID
}

// ExportsDependencies are the collaborators of NewExports.
type ExportsDependencies struct {
	Persistence store.ExamExportStore
	Access      store.ExamAuthoringStore
	Memberships store.AcademicUnitMemberStore
	Authorizer  *access.Authorizer
	Auditor     *access.Auditor
	Content     ExportContent
	Now         func() time.Time
	NewID       func() model.ExamExportID
	NewJobID    func() model.JobID
}

// NewExports returns an Exports service, or an error when a required
// dependency is missing.
func NewExports(deps ExportsDependencies) (*Exports, error) {
	if deps.Persistence == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.Content == nil ||
		deps.Now == nil ||
		deps.NewID == nil ||
		deps.NewJobID == nil {
		return nil, errors.New("Exam export dependencies are required")
	}

	return &Exports{
		exports:             deps.Persistence,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor.MutationAudit(),
		content:             deps.Content,
		now:                 deps.Now,
		newID:               deps.NewID,
		newJobID:            deps.NewJobID,
	}, nil
}
