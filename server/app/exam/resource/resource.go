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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Authorizer is the part of *access.Authorizer the Exam Resource use cases
// call.
type Authorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// Auditor records the attempt and failure of each audited Resource command;
// the Store completes the audit event with the change it commits.
type Auditor interface {
	Begin(context.Context, access.Mutation) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// FileContent stores, opens and removes the bytes of one Resource rendition.
// StoreExamResource returns the rendition it wrote, whose checksum the use
// case compares with the one the caller declared; RemoveExamResource discards
// a rendition that never became visible.
type FileContent interface {
	StoreExamResource(
		context.Context,
		model.FileRevisionID,
		model.ExamResourceMediaType,
		io.Reader,
		int64,
		time.Time,
	) (model.FileRendition, error)
	OpenExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error)
	RemoveExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) error
}

// Service manages the Exam Resources of an Exam Draft.
type Service struct {
	resources store.ExamResourceStore
	// exams reads the Exam ownership and Draft a command is authorized and
	// fenced against.
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer    Authorizer
	auditor       Auditor
	publisher     realtime.Publisher
	content       FileContent
	now           func() time.Time
	newResourceID func() model.ExamResourceID
	newEntryID    func() model.FileEntryID
	newRevisionID func() model.FileRevisionID
	newLeaseID    func() model.UploadLeaseID
}

// Dependencies are the collaborators New validates once and distributes to
// the Service.
type Dependencies struct {
	Persistence   store.ExamResourceStore
	Access        store.ExamAuthoringStore
	Memberships   store.AcademicUnitMemberStore
	Authorizer    Authorizer
	Auditor       Auditor
	Publisher     realtime.Publisher
	Content       FileContent
	Now           func() time.Time
	NewResourceID func() model.ExamResourceID
	NewEntryID    func() model.FileEntryID
	NewRevisionID func() model.FileRevisionID
	NewLeaseID    func() model.UploadLeaseID
}

// New returns an Exam Resource Service, or an error when a required
// dependency is missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Persistence == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Content == nil ||
		deps.Now == nil ||
		deps.NewResourceID == nil ||
		deps.NewEntryID == nil ||
		deps.NewRevisionID == nil ||
		deps.NewLeaseID == nil {
		return nil, errors.New("exam resource dependencies are required")
	}

	return &Service{
		resources:           deps.Persistence,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor,
		publisher:           deps.Publisher,
		content:             deps.Content,
		now:                 deps.Now,
		newResourceID:       deps.NewResourceID,
		newEntryID:          deps.NewEntryID,
		newRevisionID:       deps.NewRevisionID,
		newLeaseID:          deps.NewLeaseID,
	}, nil
}
