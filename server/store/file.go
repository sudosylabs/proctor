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

type FileUploadCreation struct {
	Entry    *model.FileEntry
	Revision *model.FileRevision
	Lease    *model.UploadLease
}

type FileUpload struct {
	Entry    *model.FileEntry
	Revision *model.FileRevision
	Lease    *model.UploadLease
}

type FileRevisionUploadCreation struct {
	EntryID  model.FileEntryID
	Revision *model.FileRevision
	Lease    *model.UploadLease
}

type ProfilePicturePublication struct {
	ActorID              model.UserID
	UserID               model.UserID
	ExpectedUserRevision int64
	EntryID              model.FileEntryID
	RevisionID           model.FileRevisionID
	LeaseID              model.UploadLeaseID
	Renditions           []model.FileRendition
	ChangedAt            time.Time
	AuditEventID         model.AuditEventID
	AuditAt              time.Time
}

type ProfilePicturePublicationResult struct {
	User     *model.User
	Revision *model.FileRevision
}

type DefaultProfilePicturePublication struct {
	UserID               model.UserID
	ExpectedUserRevision int64
	EntryID              model.FileEntryID
	RevisionID           model.FileRevisionID
	LeaseID              model.UploadLeaseID
	Renditions           []model.FileRendition
	AttachedAt           time.Time
}

type ProfilePictureState struct {
	EntryID    model.FileEntryID
	RevisionID model.FileRevisionID
	Renditions []model.FileRendition
}

type ProfilePictureUploadDiscard struct {
	ActorID                   model.UserID
	UserID                    model.UserID
	ExpectedUserRevision      int64
	ExpectedActiveEntryID     model.FileEntryID
	ExpectedCurrentRevisionID model.FileRevisionID
	UploadEntryID             model.FileEntryID
	RevisionID                model.FileRevisionID
	LeaseID                   model.UploadLeaseID
}

type ProfilePictureRemoval struct {
	ActorID                   model.UserID
	UserID                    model.UserID
	ExpectedUserRevision      int64
	EntryID                   model.FileEntryID
	ExpectedCurrentRevisionID model.FileRevisionID
	ExpectedSHA256            string
	ChangedAt                 time.Time
	AuditEventID              model.AuditEventID
	AuditAt                   time.Time
}

type FilePurgeCandidateKind string

const (
	FilePurgeCandidateExpiredLease   FilePurgeCandidateKind = "expired_lease"
	FilePurgeCandidateArchivedCustom FilePurgeCandidateKind = "archived_custom"
)

// FilePurgeCandidate is one unit of physical cleanup, derived from metadata.
// Storage paths never cross this boundary.
type FilePurgeCandidate struct {
	Cursor       string
	Kind         FilePurgeCandidateKind
	LeaseID      model.UploadLeaseID
	EntryID      model.FileEntryID
	RevisionID   model.FileRevisionID
	RenditionIDs []model.FileRenditionID
}

type FilePurgeCandidateRequest struct {
	After string
	Limit int
}

type FilePurgeCandidatePage struct {
	Candidates []FilePurgeCandidate
}

// FilePurgeClaim is the tombstone written before content is removed; reusing
// it lets cleanup recover after a worker crash.
type FilePurgeClaim struct {
	ID        string
	Candidate FilePurgeCandidate
}

// FileStore persists file metadata and publishes it to the records that
// reference it.
type FileStore interface {
	// CreateUpload creates an entry, its first pending revision and the
	// actor's lease, atomically.
	CreateUpload(context.Context, *FileUploadCreation) (*FileUpload, error)
	// CreateRevisionUpload adds a pending revision and lease to an unarchived
	// entry without changing its current revision.
	CreateRevisionUpload(context.Context, *FileRevisionUploadCreation) (*FileUpload, error)
	// DiscardProfilePictureUpload removes the pending revision and lease (and
	// the entry, if the upload created it) only while the actor, user
	// revision, active entry and current revision still match. It never
	// changes the visible picture.
	DiscardProfilePictureUpload(context.Context, *ProfilePictureUploadDiscard) error
	RenewUploadLease(context.Context, *UploadLeaseRenewal) (*model.UploadLease, error)
	// PublishProfilePicture consumes the actor's unexpired lease, stores every
	// rendition, makes the revision current, sets the user's custom entry under
	// the revision fence and completes the audit. Any failed check rolls back
	// everything.
	PublishProfilePicture(context.Context, *ProfilePicturePublication) (*ProfilePicturePublicationResult, error)
	// PublishDefaultProfilePicture attaches the entry only while the user still
	// has no default and the revision matches. It advances the user revision
	// without changing the visible-picture time.
	PublishDefaultProfilePicture(context.Context, *DefaultProfilePicturePublication) (*ProfilePicturePublicationResult, error)
	GetProfilePictureState(context.Context, model.UserID) (*ProfilePictureState, error)
	GetProfilePictureRendition(context.Context, model.UserID, string) (*model.FileRendition, error)
	// RemoveProfilePicture checks the user revision, custom entry, current
	// revision and checksum, then archives the entry and completes the audit
	// atomically. A race fails with no partial change.
	RemoveProfilePicture(context.Context, *ProfilePictureRemoval) (*model.User, error)
	// ListPurgeCandidates derives candidates from metadata on the database
	// clock; it never finds ownership by listing VFS objects.
	ListPurgeCandidates(context.Context, *FilePurgeCandidateRequest) (*FilePurgeCandidatePage, error)
	// ClaimPurgeCandidate rechecks eligibility on the database clock and writes
	// the tombstone before any content is removed.
	ClaimPurgeCandidate(context.Context, *FilePurgeCandidate) (*FilePurgeClaim, error)
	// CompletePurge deletes the metadata. Call it only after removing the
	// claimed content.
	CompletePurge(context.Context, *FilePurgeClaim) error
}

// UploadLeaseRenewal is the input to FileStore.RenewUploadLease.
type UploadLeaseRenewal struct {
	ID               model.UploadLeaseID
	ActorID          model.UserID
	ExpectedRevision int64
	BytesReceived    int64
	ExpiresAt        time.Time
}
