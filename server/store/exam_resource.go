// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// ExamResourceRecord is one draft resource with its selected rendition.
// Storage keys and upload leases never leave persistence.
type ExamResourceRecord struct {
	Resource      *model.ExamResource
	Rendition     *model.FileRendition
	DraftRevision int64
}

// Consistent reports whether both Resource and Rendition are present.
func (r *ExamResourceRecord) Consistent() bool {
	return !(r == nil ||
		r.Resource == nil ||
		r.Rendition == nil)
}

type ExamResourceUploadReservation struct {
	ExamID                model.ExamID
	ActorUserID           model.UserID
	ManagerOverride       bool
	ExpectedDraftRevision int64
	ResourceID            model.ExamResourceID
	Entry                 *model.FileEntry
	EntryID               model.FileEntryID
	Revision              *model.FileRevision
	Lease                 *model.UploadLease
	Replacement           bool
}

type ExamResourceUploadFinalization struct {
	ExamID                model.ExamID
	ActorUserID           model.UserID
	ManagerOverride       bool
	ExpectedDraftRevision int64
	Resource              *model.ExamResource
	LeaseID               model.UploadLeaseID
	Rendition             *model.FileRendition
	ChangedAt             time.Time
	AuditEventID          model.AuditEventID
	AuditAt               time.Time
}

type ExamResourceMetadataUpdate struct {
	ExamID                model.ExamID
	ActorUserID           model.UserID
	ManagerOverride       bool
	ExpectedDraftRevision int64
	ResourceID            model.ExamResourceID
	DisplayName           string
	DescriptionMarkdown   string
	ChangedAt             time.Time
	AuditEventID          model.AuditEventID
	AuditAt               time.Time
}

type ExamResourceReorder struct {
	ExamID                model.ExamID
	ActorUserID           model.UserID
	ManagerOverride       bool
	ExpectedDraftRevision int64
	ResourceIDs           []model.ExamResourceID
	ChangedAt             time.Time
	AuditEventID          model.AuditEventID
	AuditAt               time.Time
}

type ExamResourceRemoval struct {
	ExamID                model.ExamID
	ActorUserID           model.UserID
	ManagerOverride       bool
	ExpectedDraftRevision int64
	ResourceID            model.ExamResourceID
	ChangedAt             time.Time
	AuditEventID          model.AuditEventID
	AuditAt               time.Time
}

type ExamResourceCommandResult struct {
	Value         *ExamResourceRecord
	Items         []ExamResourceRecord
	DraftRevision int64
	Replayed      bool
}

// ExamResourceStore persists draft resources, uploaded in two phases.
// ReserveUpload stores only pending file metadata and deliberately ignores the
// exam and draft, so a retry can still reach its stored outcome after the
// original advanced the draft. FinalizeUpload rechecks the active exam, draft
// revision, manager relationship, lease, purpose and rendition, then
// atomically publishes the file revision, selects it, advances the draft and
// completes the audit. The other mutations recheck the same things.
type ExamResourceStore interface {
	ReserveUpload(context.Context, *ExamResourceUploadReservation) (*FileUpload, error)
	FinalizeUpload(context.Context, *ExamResourceUploadFinalization, *CommandIdempotency) (*ExamResourceCommandResult, error)
	List(context.Context, model.ExamID) ([]ExamResourceRecord, error)
	Get(context.Context, model.ExamID, model.ExamResourceID) (*ExamResourceRecord, error)
	UpdateMetadata(context.Context, *ExamResourceMetadataUpdate, *CommandIdempotency) (*ExamResourceCommandResult, error)
	Reorder(context.Context, *ExamResourceReorder, *CommandIdempotency) (*ExamResourceCommandResult, error)
	Remove(context.Context, *ExamResourceRemoval, *CommandIdempotency) (*ExamResourceCommandResult, error)
}
