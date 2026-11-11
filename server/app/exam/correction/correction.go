// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Authorizer is the part of *access.Authorizer the correction use cases call.
type Authorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// Auditor records the attempt and failure of each audited correction
// command; the Store completes the audit event with the change it commits.
type Auditor interface {
	Begin(context.Context, access.Mutation) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// Content writes the bytes of a staged Exam Resource under the File Revision
// and Rendition identifiers the Store reserved, so a retry of the same stage
// writes the same object.
type Content interface {
	StoreExamResourceRendition(
		context.Context,
		model.FileRevisionID,
		model.FileRenditionID,
		model.ExamResourceMediaType,
		io.Reader,
		int64,
		time.Time,
	) (model.FileRendition, error)
}

// Service stages Exam Resource content for, and applies, live corrections to
// an Exam Sitting.
type Service struct {
	corrections store.ExamCorrectionStore
	revisions   store.ExamRevisionStore
	// sittings finds the candidates whose Exam activity a correction refreshes.
	sittings            store.ExamSittingStore
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer        Authorizer
	auditor           Auditor
	publisher         realtime.Publisher
	content           Content
	now               func() time.Time
	institutionOrigin string

	newStageID        func() model.ExamCorrectionResourceStageID
	newResourceID     func() model.ExamResourceID
	newEntryID        func() model.FileEntryID
	newFileRevisionID func() model.FileRevisionID
	newLeaseID        func() model.UploadLeaseID
	newRenditionID    func() model.FileRenditionID
	newExamRevisionID func() model.ExamRevisionID
}

// Dependencies are the collaborators New validates once and distributes to
// the Service.
type Dependencies struct {
	Persistence       store.ExamCorrectionStore
	Revisions         store.ExamRevisionStore
	Sittings          store.ExamSittingStore
	Access            store.ExamAuthoringStore
	Memberships       store.AcademicUnitMemberStore
	Authorizer        Authorizer
	Auditor           Auditor
	Publisher         realtime.Publisher
	Content           Content
	Now               func() time.Time
	NewStageID        func() model.ExamCorrectionResourceStageID
	NewResourceID     func() model.ExamResourceID
	NewEntryID        func() model.FileEntryID
	NewFileRevisionID func() model.FileRevisionID
	NewLeaseID        func() model.UploadLeaseID
	NewRenditionID    func() model.FileRenditionID
	NewExamRevisionID func() model.ExamRevisionID
	InstitutionOrigin string
}

// New returns a correction Service, or an error when a required dependency is
// missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Persistence == nil ||
		deps.Revisions == nil ||
		deps.Sittings == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Content == nil ||
		deps.Now == nil ||
		deps.NewStageID == nil ||
		deps.NewResourceID == nil ||
		deps.NewEntryID == nil ||
		deps.NewFileRevisionID == nil ||
		deps.NewLeaseID == nil ||
		deps.NewRenditionID == nil ||
		deps.NewExamRevisionID == nil {
		return nil, errors.New("exam correction dependencies are required")
	}

	return &Service{
		corrections:         deps.Persistence,
		revisions:           deps.Revisions,
		sittings:            deps.Sittings,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor,
		publisher:           deps.Publisher,
		content:             deps.Content,
		now:                 deps.Now,
		institutionOrigin:   deps.InstitutionOrigin,
		newStageID:          deps.NewStageID,
		newResourceID:       deps.NewResourceID,
		newEntryID:          deps.NewEntryID,
		newFileRevisionID:   deps.NewFileRevisionID,
		newLeaseID:          deps.NewLeaseID,
		newRenditionID:      deps.NewRenditionID,
		newExamRevisionID:   deps.NewExamRevisionID,
	}, nil
}
