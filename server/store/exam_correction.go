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

// ExamCorrectionResourceStageTarget says whether a stage adds a new resource
// or replaces the content of one already in the base revision.
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

// ExamCorrectionResourceStage is one staged resource. Storage keys never cross
// this boundary. Rendition is nil until the bytes and metadata are staged.
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

// ConsistentWith reports whether the stage belongs to the commanded exam,
// sitting, base revision, target and actor, names every identity it needs and
// an expiry, and, for a replacement, names the replaced resource.
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

// RenditionConsistent reports whether a stage past pending carries the valid
// rendition it names, for its file revision. A pending stage is consistent.
func (s *ExamCorrectionResourceStage) RenditionConsistent() bool {
	return !(s == nil ||
		(s.State != ExamCorrectionResourceStagePending &&
			(s.Rendition == nil ||
				s.Rendition.ID != s.RenditionID ||
				s.Rendition.RevisionID != s.FileRevisionID ||
				s.Rendition.Validate() != nil)))
}

// ExamCorrectionResourceStageReservation allocates every identity before the
// VFS write. An addition carries a new Entry; a replacement names the
// FileEntryID pinned by the base revision. Revision and Lease stay invisible
// until Apply consumes the ready stage.
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

// ExamCorrectionResourceManifestItem is one entry of the full, ordered
// resource manifest. A zero StageID keeps the base rendition; otherwise the
// ready stage adds or replaces the content.
type ExamCorrectionResourceManifestItem struct {
	ResourceID          model.ExamResourceID
	DisplayName         string
	DescriptionMarkdown string
	StageID             model.ExamCorrectionResourceStageID
}

type ExamCorrectionApplication struct {
	// InstitutionOrigin comes from installation configuration, never from the
	// request.
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

// ExamCorrectionResult deliberately omits the authored content; read it with
// ExamRevisionStore.GetSnapshot.
type ExamCorrectionResult struct {
	Revision           *ExamRevisionSummary
	Sitting            *ExamSittingSnapshot
	PreviousRevisionID model.ExamRevisionID
	EffectiveAt        time.Time
	Replayed           bool
}

// Consistent reports whether the revision and sitting snapshot are present;
// ConsistentWith compares them with the command.
func (r *ExamCorrectionResult) Consistent() bool {
	return !(r == nil ||
		r.Revision == nil ||
		r.Sitting == nil ||
		r.Sitting.Sitting == nil)
}

// ConsistentWith reports whether the result belongs to the commanded exam and
// sitting, follows the expected current revision, and satisfies Applied.
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

// Applied is the application's postcondition: a new live-correction revision,
// the sitting on it at the next sitting revision and still open or paused,
// and the correction effective at the revision's publication instant.
func (r *ExamCorrectionResult) Applied(expectedCurrentRevisionID model.ExamRevisionID, expectedSittingRevision int64) bool {
	return r.Revision.ID != expectedCurrentRevisionID &&
		r.Revision.Kind == model.ExamRevisionPublicationLiveCorrection &&
		r.Sitting.Sitting.ExamRevisionID == r.Revision.ID &&
		r.Sitting.Sitting.Revision == expectedSittingRevision+1 &&
		(r.Sitting.Sitting.State == model.ExamSittingOpen || r.Sitting.Sitting.State == model.ExamSittingPaused) &&
		model.TimeUTC(r.EffectiveAt).Equal(model.TimeUTC(r.Revision.PublishedAt))
}

// ExamCorrectionStore persists live corrections. ReserveResourceStage
// resolves a replayed command before checking current state. Apply is the
// only write that makes a correction visible: after replay it locks and
// revalidates the exam, sitting, base and stages, creates one sealed
// revision, consumes the stages, retargets only that sitting and completes
// the audit, atomically. AffectedCapabilities must cover the actual change;
// the acknowledgement gates only those, and a no-op change gets no notice.
type ExamCorrectionStore interface {
	ReserveResourceStage(context.Context, *ExamCorrectionResourceStageReservation, *CommandIdempotency) (*ExamCorrectionResourceStage, error)
	MarkResourceStageReady(context.Context, *ExamCorrectionResourceStageReadiness) (*ExamCorrectionResourceStage, error)
	Apply(context.Context, *ExamCorrectionApplication, *CommandIdempotency) (*ExamCorrectionResult, error)
}
