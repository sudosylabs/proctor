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

const ExamCorrectionResourceStageOperation = "exam.sitting.correction.resource.stage"

// ExamCorrectionResourceStageTarget says whether a purpose-bound stage
// allocates a new stable resource identity or replaces the content behind an
// identity already present in the correction's exact base Revision.
type ExamCorrectionResourceStageTarget string

const (
	ExamCorrectionResourceAddition    ExamCorrectionResourceStageTarget = "addition"
	ExamCorrectionResourceReplacement ExamCorrectionResourceStageTarget = "replacement"
)

func (t ExamCorrectionResourceStageTarget) IsValid() bool {
	return t == ExamCorrectionResourceAddition || t == ExamCorrectionResourceReplacement
}

type ExamCorrectionResourceStageState string

const (
	ExamCorrectionResourceStagePending  ExamCorrectionResourceStageState = "pending"
	ExamCorrectionResourceStageReady    ExamCorrectionResourceStageState = "ready"
	ExamCorrectionResourceStageConsumed ExamCorrectionResourceStageState = "consumed"
)

// ExamCorrectionResourceStage is the complete bounded result of reserving or
// completing one staged resource. Storage keys never cross this boundary.
// Rendition is present only after exact bytes and metadata have been staged.
type ExamCorrectionResourceStage struct {
	ID              model.ExamCorrectionResourceStageID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	BaseRevisionID  model.ExamRevisionID
	Target          ExamCorrectionResourceStageTarget
	ResourceID      model.ExamResourceID
	FileEntryID     model.FileEntryID
	FileRevisionID  model.FileRevisionID
	UploadLeaseID   model.UploadLeaseID
	RenditionID     model.FileRenditionID
	CreatedByUserID model.UserID
	State           ExamCorrectionResourceStageState
	CreatedAt       time.Time
	ExpiresAt       time.Time
	ReadyAt         time.Time
	ConsumedAt      time.Time
	Rendition       *model.FileRendition
}

// ConsistentWith reports whether the stage is identified, belongs to the
// commanded Exam, Sitting and base Revision with the commanded target, names
// its Resource, file entry, File Revision, upload lease and rendition, was
// created by the acting User, carries an expiry, and, for a replacement, names
// the Resource being replaced.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (s *ExamCorrectionResourceStage) ConsistentWith(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	baseRevisionID model.ExamRevisionID,
	target ExamCorrectionResourceStageTarget,
	resourceID model.ExamResourceID,
	actor model.UserID,
) bool {
	return !(s == nil ||
		!s.ID.IsValid() ||
		s.ExamID != examID ||
		s.SittingID != sittingID ||
		s.BaseRevisionID != baseRevisionID ||
		s.Target != target ||
		!s.ResourceID.IsValid() ||
		!s.FileEntryID.IsValid() ||
		!s.FileRevisionID.IsValid() ||
		!s.UploadLeaseID.IsValid() ||
		!s.RenditionID.IsValid() ||
		s.CreatedByUserID != actor ||
		s.ExpiresAt.IsZero() ||
		(target == ExamCorrectionResourceReplacement && s.ResourceID != resourceID))
}

// RenditionConsistent reports whether a stage that has left pending carries
// the valid rendition it names, for the File Revision it names. A pending
// stage has no rendition yet and is consistent. ConsistentWith asks about the
// stage's identity; this asks only about its rendition.
func (s *ExamCorrectionResourceStage) RenditionConsistent() bool {
	return !(s == nil ||
		(s.State != ExamCorrectionResourceStagePending &&
			(s.Rendition == nil ||
				s.Rendition.ID != s.RenditionID ||
				s.Rendition.RevisionID != s.FileRevisionID ||
				s.Rendition.Validate() != nil)))
}

// ExamCorrectionResourceStageReservation preallocates every opaque identity
// before a VFS write. Addition carries a pristine File Entry; replacement
// names the stable File Entry pinned by the base Revision. Revision and Lease
// remain pending/invisible until Apply consumes a Ready stage.
type ExamCorrectionResourceStageReservation struct {
	StageID         model.ExamCorrectionResourceStageID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	BaseRevisionID  model.ExamRevisionID
	Target          ExamCorrectionResourceStageTarget
	ResourceID      model.ExamResourceID
	Entry           *model.FileEntry
	FileEntryID     model.FileEntryID
	Revision        *model.FileRevision
	Lease           *model.UploadLease
	RenditionID     model.FileRenditionID
	ActorUserID     model.UserID
	ManagerOverride bool
	CreatedAt       time.Time
	AuditEventID    model.AuditEventID
	AuditAt         time.Time
}

type ExamCorrectionResourceStageReadiness struct {
	StageID     model.ExamCorrectionResourceStageID
	ActorUserID model.UserID
	Rendition   *model.FileRendition
	ReadyAt     time.Time
}

// ExamCorrectionResourceManifestItem is one entry in the complete desired
// ordered resource manifest. A zero StageID retains the exact base rendition;
// a StageID replaces or adds content according to that bound Ready stage.
type ExamCorrectionResourceManifestItem struct {
	ResourceID          model.ExamResourceID
	DisplayName         string
	DescriptionMarkdown string
	StageID             model.ExamCorrectionResourceStageID
}

type ExamCorrectionApplication struct {
	// InstitutionOrigin is installation configuration supplied by the application, never request data.
	InstitutionOrigin       string
	RevisionID              model.ExamRevisionID
	ExamID                  model.ExamID
	SittingID               model.ExamSittingID
	CurrentRevisionID       model.ExamRevisionID
	ExpectedSittingRevision int64
	ActorUserID             model.UserID
	ManagerOverride         bool
	InstructionsMarkdown    *string
	BrowserPolicy           *model.BrowserPolicy
	Resources               []ExamCorrectionResourceManifestItem
	CandidateSummary        string
	AcknowledgementRequired bool
	AffectedCapabilities    []model.CandidateCapability
	PrivateReason           string
	AppliedAt               time.Time
	AuditEventID            model.AuditEventID
	AuditAt                 time.Time
}

// ExamCorrectionResult is intentionally bounded. Full authored snapshot
// content remains available only through ExamRevisionStore.GetSnapshot.
type ExamCorrectionResult struct {
	Revision           *ExamRevisionSummary
	Sitting            *ExamSittingSnapshot
	PreviousRevisionID model.ExamRevisionID
	EffectiveAt        time.Time
	Replayed           bool
}

// Consistent reports whether the outcome carries the correction Revision and
// the corrected Sitting's snapshot. It is the presence check; ConsistentWith
// compares what they hold against the command.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamCorrectionResult) Consistent() bool {
	return !(r == nil ||
		r.Revision == nil ||
		r.Sitting == nil ||
		r.Sitting.Sitting == nil)
}

// ConsistentWith reports whether the outcome's Revision is a new, numbered
// live-correction Revision of the commanded Exam that follows the expected
// current Revision, its Sitting is the commanded one, now on that Revision at
// the next Sitting revision and still open or paused, and the correction took
// effect at the instant the Revision was published. An outcome without its
// Revision or Sitting is inconsistent.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *ExamCorrectionResult) ConsistentWith(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	expectedCurrentRevisionID model.ExamRevisionID,
	expectedSittingRevision int64,
) bool {
	return !(r == nil ||
		r.Revision == nil ||
		r.Sitting == nil ||
		r.Sitting.Sitting == nil ||
		r.Revision.ExamID != examID ||
		r.Revision.Number < 1 ||
		r.PreviousRevisionID != expectedCurrentRevisionID ||
		r.Sitting.Sitting.ID != sittingID ||
		r.Sitting.Sitting.ExamID != examID ||
		r.Sitting.Sitting.UpdatedAt.IsZero() ||
		r.EffectiveAt.IsZero() ||
		!r.Applied(expectedCurrentRevisionID, expectedSittingRevision))
}

// Applied is the application's postcondition on a live correction: its
// Revision is a new live-correction Revision, the Sitting is now on it at the
// next Sitting revision and still open or paused, and the correction took
// effect at the instant the Revision was published.
func (r *ExamCorrectionResult) Applied(expectedCurrentRevisionID model.ExamRevisionID, expectedSittingRevision int64) bool {
	return r.Revision.ID != expectedCurrentRevisionID &&
		r.Revision.Kind == model.ExamRevisionPublicationLiveCorrection &&
		r.Sitting.Sitting.ExamRevisionID == r.Revision.ID &&
		r.Sitting.Sitting.Revision == expectedSittingRevision+1 &&
		(r.Sitting.Sitting.State == model.ExamSittingOpen || r.Sitting.Sitting.State == model.ExamSittingPaused) &&
		model.TimeUTC(r.EffectiveAt).Equal(model.TimeUTC(r.Revision.PublishedAt))
}

// ExamCorrectionStore is the narrow durable core for live correction. Reserve
// resolves an exact committed command before current-state checks and returns
// the stage's current Pending/Ready/Consumed state. Apply is the sole atomic
// visibility mutation: it resolves replay first, locks and revalidates the
// Exam/Sitting/base/stages, creates one sealed immutable Revision, consumes
// referenced stages, retargets only that Sitting, appends private provenance,
// completes safe audit, and stores a bounded outcome. The immutable affected
// selection must cover the actual semantic change. Its required acknowledgement
// gates only selected capabilities; no-op content cannot allocate a notice.
type ExamCorrectionStore interface {
	ReserveResourceStage(context.Context, *ExamCorrectionResourceStageReservation, *CommandIdempotency) (*ExamCorrectionResourceStage, error)
	MarkResourceStageReady(context.Context, *ExamCorrectionResourceStageReadiness) (*ExamCorrectionResourceStage, error)
	Apply(context.Context, *ExamCorrectionApplication, *CommandIdempotency) (*ExamCorrectionResult, error)
}
