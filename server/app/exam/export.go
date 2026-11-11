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

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExportArchiveInput is private byte-processing input owned by its consumer.
// The content adapter writes the versioned portable representation, verifies
// original bytes and the completed archive, and never chooses domain scope.
type ExportArchiveInput struct {
	ExportID  model.ExamExportID
	AttemptID model.JobAttemptID
	Records   []byte
	Files     []ExportArchiveFile
}

// ExportArchiveFile names one sealed Submission file to include in an
// Examination Export archive.
type ExportArchiveFile struct {
	SubmissionID model.SubmissionID
	Entry        model.ExamSubmissionManifestEntry
}

// ExportArchiveContent describes the verified archive bytes written by
// ExportContent.
type ExportArchiveContent struct {
	SizeBytes int64
	SHA256    string
}

// ExportContent writes, opens, and purges the stored archive bytes of an
// Examination Export, keyed by export and Job attempt.
type ExportContent interface {
	// An unsuccessful write whose backend outcome is still uncertain must
	// return an error implementing ExamExportWriteUncertain(). Its durable
	// artifact reservation remains necessary even after one absence check.
	BuildExamExport(context.Context, ExportArchiveInput) (*ExportArchiveContent, error)
	OpenExamExport(context.Context, model.ExamExportID, model.JobAttemptID) (io.ReadCloser, error)
	PurgeExamExport(context.Context, model.ExamExportID, model.JobAttemptID) error
}

// Exports requests, reads, and builds Examination Exports.
type Exports struct {
	exports store.ExamExportStore
	// exams reads the Exam ownership an export is authorized against.
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer RecordsAuthorizer
	auditor    Auditor
	content    ExportContent
	now        func() time.Time
	newID      func() model.ExamExportID
	newJobID   func() model.JobID
}

// ExportsDependencies are the collaborators NewExports validates once and
// distributes to the Exports.
type ExportsDependencies struct {
	Persistence store.ExamExportStore
	Access      store.ExamAuthoringStore
	Memberships store.AcademicUnitMemberStore
	Authorizer  RecordsAuthorizer
	Auditor     Auditor
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
		auditor:             deps.Auditor,
		content:             deps.Content,
		now:                 deps.Now,
		newID:               deps.NewID,
		newJobID:            deps.NewJobID,
	}, nil
}
