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
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// FileContent holds the bytes of resource renditions. StoreExamResource
// returns the rendition it wrote so the caller can check the declared
// checksum; RemoveExamResource discards a rendition that never became visible.
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

// Service manages exam draft resources; build it with New.
type Service struct {
	resources           store.ExamResourceStore
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer *access.Authorizer
	// auditor only begins and fails audits; the store completes them in the
	// same transaction as the change.
	auditor       access.MutationAuditAdapter
	publisher     realtime.Publisher
	content       FileContent
	now           func() time.Time
	newResourceID func() model.ExamResourceID
	newEntryID    func() model.FileEntryID
	newRevisionID func() model.FileRevisionID
	newLeaseID    func() model.UploadLeaseID
}

// Dependencies are the Service's collaborators; New requires every field.
type Dependencies struct {
	Persistence   store.ExamResourceStore
	Access        store.ExamAuthoringStore
	Memberships   store.AcademicUnitMemberStore
	Authorizer    *access.Authorizer
	Auditor       *access.Auditor
	Publisher     realtime.Publisher
	Content       FileContent
	Now           func() time.Time
	NewResourceID func() model.ExamResourceID
	NewEntryID    func() model.FileEntryID
	NewRevisionID func() model.FileRevisionID
	NewLeaseID    func() model.UploadLeaseID
}

// New returns an error when any dependency is nil.
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
		auditor:             deps.Auditor.MutationAudit(),
		publisher:           deps.Publisher,
		content:             deps.Content,
		now:                 deps.Now,
		newResourceID:       deps.NewResourceID,
		newEntryID:          deps.NewEntryID,
		newRevisionID:       deps.NewRevisionID,
		newLeaseID:          deps.NewLeaseID,
	}, nil
}
