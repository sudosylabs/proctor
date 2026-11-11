// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type StageResourceContentCommand struct {
	ExamID         model.ExamID
	SittingID      model.ExamSittingID
	BaseRevisionID model.ExamRevisionID
	Target         store.ExamCorrectionResourceStageTarget
	ResourceID     model.ExamResourceID
	MediaType      model.ExamResourceMediaType
	Body           io.Reader
	Size           int64
	ExpectedSHA256 string
	IdempotencyKey string
}

// ResourceStage is staged Resource content an ApplyCommand may select by
// StageID until ExpiresAt, with the size and checksum the server verified.
type ResourceStage struct {
	StageID    model.ExamCorrectionResourceStageID
	ResourceID model.ExamResourceID
	MediaType  model.ExamResourceMediaType
	Size       int64
	SHA256     string
	ExpiresAt  time.Time
}

// StageExamSittingCorrectionResourceContent idempotently uploads and verifies
// Exam Resource content against its expected SHA-256 for a later correction
// of the Sitting based on BaseRevisionID. The returned stage expires at
// ExpiresAt.
func (s *Service) StageExamSittingCorrectionResourceContent(
	ctx context.Context,
	inv invocation.Invocation,
	c StageResourceContentCommand,
) (_ ResourceStage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !c.ExamID.IsValid() ||
		!c.SittingID.IsValid() ||
		!c.BaseRevisionID.IsValid() ||
		!c.Target.IsValid() ||
		!c.MediaType.IsValid() ||
		c.Body == nil ||
		c.Size < 0 ||
		c.Size > model.ExamResourceMaximumBytes ||
		!validSHA256(c.ExpectedSHA256) ||
		(c.Target == store.ExamCorrectionResourceAddition && !c.ResourceID.IsZero()) ||
		(c.Target == store.ExamCorrectionResourceReplacement && !c.ResourceID.IsValid()) {
		return ResourceStage{}, apperror.Invalid("exam.sitting.correction.invalid", "upload")
	}

	commandIdempotency, err := prepareStageIdempotency(inv, c)
	if err != nil {
		return ResourceStage{}, err
	}

	auth, err := s.authorizeSitting(ctx, inv, c.ExamID, c.SittingID)
	if err != nil {
		return ResourceStage{}, err
	}

	at := model.TimeUTC(s.now())
	principal := inv.Principal()
	resourceID := c.ResourceID
	entryID := model.FileEntryID("")
	var entry *model.FileEntry
	if c.Target == store.ExamCorrectionResourceAddition {
		resourceID = s.newResourceID()
		entryID = s.newEntryID()
		entry, err = model.NewFileEntryForPurpose(entryID, model.FilePurposeExamResource, model.FileIndexingNone, at)
		if err != nil {
			return ResourceStage{}, apperror.Invalid("exam.sitting.correction.invalid", "file_entry").Wrap(err)
		}
	} else {
		var base *model.ExamRevision
		base, err = s.revisions.GetSnapshot(ctx, c.ExamID, c.BaseRevisionID)
		if err != nil {
			return ResourceStage{}, storeErrors.Translate(err)
		}
		if base == nil || base.ID != c.BaseRevisionID || base.ExamID != c.ExamID {
			return ResourceStage{}, apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("missing base revision"))
		}

		found := false
		for _, r := range base.Resources {
			if r.ResourceID == resourceID {
				entryID = r.FileEntryID
				found = true
				break
			}
		}

		if !found {
			return ResourceStage{}, apperror.New("exam.sitting.correction.not_found")
		}
	}

	revisionID, leaseID, renditionID := s.newFileRevisionID(), s.newLeaseID(), s.newRenditionID()
	revision, e := model.NewFileRevision(revisionID, entryID, model.FileAvailabilityPending, model.FileIndexingNotRequired, at)
	if e != nil {
		return ResourceStage{}, apperror.Invalid("exam.sitting.correction.invalid", "file_revision").Wrap(e)
	}

	lease, e := model.NewUploadLease(leaseID, revisionID, principal.UserID, at, at.Add(model.UploadLeaseMaximumLifetime))
	if e != nil {
		return ResourceStage{}, apperror.Invalid("exam.sitting.correction.invalid", "upload_lease").Wrap(e)
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     auth.action,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: c.SittingID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    auth.academicUnitID.String(),
		Operation:  "exam_sitting_correction_resource_stage_reserve",
		Value: map[string]any{
			"exam_id":          c.ExamID.String(),
			"exam_sitting_id":  c.SittingID.String(),
			"base_revision_id": c.BaseRevisionID.String(),
			"target":           string(c.Target),
		},
	})
	if err != nil {
		return ResourceStage{}, err
	}

	stage, err := s.corrections.ReserveResourceStage(
		ctx,
		&store.ExamCorrectionResourceStageReservation{
			StageID:         s.newStageID(),
			ExamID:          c.ExamID,
			SittingID:       c.SittingID,
			BaseRevisionID:  c.BaseRevisionID,
			Target:          c.Target,
			ResourceID:      resourceID,
			Entry:           entry,
			FileEntryID:     entryID,
			Revision:        revision,
			Lease:           lease,
			RenditionID:     renditionID,
			ActorUserID:     principal.UserID,
			ManagerOverride: auth.override,
			CreatedAt:       at,
			AuditEventID:    auditID,
			AuditAt:         at,
		},
		commandIdempotency,
	)
	if err != nil {
		return ResourceStage{}, s.failAudit(ctx, auditID, err)
	}
	if stage == nil {
		return ResourceStage{}, apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("missing stage outcome"))
	}
	if err = validateStage(stage, c, principal.UserID); err != nil {
		return ResourceStage{}, err
	}

	if stage.State == store.ExamCorrectionResourceStagePending {
		rendition, writeErr := s.content.StoreExamResourceRendition(
			ctx,
			stage.FileRevisionID,
			stage.RenditionID,
			c.MediaType,
			c.Body,
			c.Size,
			stage.CreatedAt,
		)
		if writeErr != nil {
			return ResourceStage{}, mapContentError(writeErr)
		}
		if subtle.ConstantTimeCompare([]byte(rendition.SHA256), []byte(c.ExpectedSHA256)) != 1 {
			return ResourceStage{}, apperror.New("exam.sitting.correction.content_invalid").Wrap(errors.New("content checksum mismatch"))
		}

		stage, err = s.corrections.MarkResourceStageReady(ctx, &store.ExamCorrectionResourceStageReadiness{
			StageID:     stage.ID,
			ActorUserID: principal.UserID,
			Rendition:   &rendition,
			ReadyAt:     model.TimeUTC(s.now()),
		})
		if err != nil {
			return ResourceStage{}, storeErrors.Translate(err)
		}
		if err = validateStage(stage, c, principal.UserID); err != nil {
			return ResourceStage{}, err
		}
	}

	if stage.State != store.ExamCorrectionResourceStageReady && stage.State != store.ExamCorrectionResourceStageConsumed {
		return ResourceStage{}, apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("stage did not become ready"))
	}

	return projectStage(stage), nil
}

func validateStage(stage *store.ExamCorrectionResourceStage, c StageResourceContentCommand, actor model.UserID) error {
	if !stage.ConsistentWith(c.ExamID, c.SittingID, c.BaseRevisionID, c.Target, c.ResourceID, actor) {
		return apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("inconsistent stage outcome"))
	}

	if stage.State != store.ExamCorrectionResourceStagePending &&
		stage.State != store.ExamCorrectionResourceStageReady &&
		stage.State != store.ExamCorrectionResourceStageConsumed {
		return apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("invalid stage state"))
	}

	if !stage.RenditionConsistent() {
		return apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("incomplete ready stage"))
	}

	if stage.Rendition != nil &&
		(model.ExamResourceMediaType(stage.Rendition.MediaType) != c.MediaType ||
			stage.Rendition.Size != c.Size ||
			subtle.ConstantTimeCompare([]byte(stage.Rendition.SHA256), []byte(c.ExpectedSHA256)) != 1) {
		return apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("stage metadata differs from command"))
	}

	return nil
}

func projectStage(stage *store.ExamCorrectionResourceStage) ResourceStage {
	return ResourceStage{
		StageID:    stage.ID,
		ResourceID: stage.ResourceID,
		MediaType:  model.ExamResourceMediaType(stage.Rendition.MediaType),
		Size:       stage.Rendition.Size,
		SHA256:     stage.Rendition.SHA256,
		ExpiresAt:  stage.ExpiresAt,
	}
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
