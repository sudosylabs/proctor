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
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// BuildFromJob builds and publishes the archive for one queued Examination
// Export on behalf of its build Job. An export that is already ready is a
// no-op, and the build is bounded by the source expiry. system names that
// Job execution and must match the claimed build.
func (s *Exports) BuildFromJob(ctx context.Context, system invocation.System, input store.ExamExportBuild) (err error) {
	if !system.Valid() || system.Task != "" || system.JobID != input.JobID || system.AttemptID != input.AttemptID {
		return apperror.New("exam.export.unavailable").Wrap(errors.New("Exam export build Job identity is invalid"))
	}

	snapshot, err := s.exports.BeginBuild(ctx, &input)
	if err != nil {
		return examExportStoreErrors.Translate(err)
	}
	if snapshot == nil || snapshot.Export.Validate() != nil || snapshot.Export.ID != input.ExportID {
		return apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export build snapshot"))
	}
	if snapshot.Export.State == model.ExamExportReady {
		return nil
	}

	// Publication may have an unknown outcome. Cleanup consults durable ready
	// metadata; this path never guesses whether it is safe to delete bytes.
	defer func() {
		var uncertain interface{ ExamExportWriteUncertain() }
		if errors.As(err, &uncertain) {
			return
		}

		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if finishErr := s.exports.FinishWriter(cleanup, input.ExamExportArtifact); finishErr != nil && err == nil {
			err = apperror.New("exam.export.unavailable").Wrap(finishErr)
		}
	}()
	if !snapshot.BuildInputConsistent() {
		return apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export build input"))
	}

	buildCtx, cancel := context.WithDeadline(ctx, snapshot.Export.SourceExpiresAt)
	defer cancel()
	archive := ExportArchiveInput{ExportID: input.ExportID, AttemptID: input.AttemptID, Records: snapshot.Records}
	for _, f := range snapshot.Files {
		archive.Files = append(archive.Files, ExportArchiveFile{SubmissionID: f.SubmissionID, Entry: f.Entry})
	}

	content, err := s.content.BuildExamExport(buildCtx, archive)
	if err != nil {
		return apperror.New("exam.export.unavailable").Wrap(err)
	}
	if content == nil || !model.ValidExamExportContent(content.SizeBytes, content.SHA256) {
		return apperror.New("exam.export.unavailable").Wrap(errors.New("invalid verified export content"))
	}

	if err = s.exports.Publish(buildCtx, &store.ExamExportPublication{
		ExamExportBuild: input,
		SizeBytes:       content.SizeBytes,
		SHA256:          content.SHA256,
	}); err != nil {
		return examExportStoreErrors.Translate(err)
	}

	return nil
}
