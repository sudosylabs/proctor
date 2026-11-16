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
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Content writes staged resource bytes under the revision and rendition IDs
// the store reserved, so a retried stage writes the same object.
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

// Service stages resource content for, and applies, live corrections; build
// it with New.
type Service struct {
	corrections         store.ExamCorrectionStore
	revisions           store.ExamRevisionStore
	sittings            store.ExamSittingStore
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer *access.Authorizer
	// auditor only begins and fails audits; the store completes them in the
	// same transaction as the change.
	auditor           access.MutationAuditAdapter
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

// Dependencies are the Service's collaborators. New requires every field
// except InstitutionOrigin.
type Dependencies struct {
	Persistence       store.ExamCorrectionStore
	Revisions         store.ExamRevisionStore
	Sittings          store.ExamSittingStore
	Access            store.ExamAuthoringStore
	Memberships       store.AcademicUnitMemberStore
	Authorizer        *access.Authorizer
	Auditor           *access.Auditor
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

// New returns an error when a required dependency is nil.
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
		auditor:             deps.Auditor.MutationAudit(),
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
