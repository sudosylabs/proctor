// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// examExportLabel names the exam export in errors.
const examExportLabel = "Exam export"

// BuildExamExport writes only to the export identity registered in input. It
// verifies each source file as the format reads it, and re-reads the stored
// archive before returning. It fails when no export format is configured.
func (c *Content) BuildExamExport(ctx context.Context, input exam.ExportArchiveInput) (*exam.ExportArchiveContent, error) {
	if c == nil || c.examExportFormat == nil {
		return nil, errExamExportFormatUnavailable
	}
	format := c.examExportFormat()
	if format == nil {
		return nil, errExamExportFormatUnavailable
	}
	return c.buildExamExport(ctx, input, format)
}

// errExamExportFormatUnavailable means the plugin that offers the format is
// off or has failed.
var errExamExportFormatUnavailable = errors.New("Exam export format is unavailable")

// format only encodes the archive; validation, limits, verification and
// storage stay here so a plugin cannot weaken them.
func (c *Content) buildExamExport(ctx context.Context, input exam.ExportArchiveInput, format plugin.ExportFormat) (*exam.ExportArchiveContent, error) {
	if err := c.validateExamExportInput(input); err != nil {
		return nil, err
	}

	release, err := c.beginWork(ctx)
	if err != nil {
		return nil, err
	}

	defer release()
	spool, releaseSpool, err := createUnlinkedSpool("proctor-exam-export-*")
	if err != nil {
		return nil, sanitize("create Exam export spool", err)
	}

	defer releaseSpool()
	bounded := &examExportBoundedWriter{writer: spool, remaining: model.ExamExportMaximumArchiveBytes}
	if err = c.writeExamExport(ctx, input, format, bounded); err != nil {
		return nil, err
	}
	size, digest, err := hashSpool(ctx, examExportLabel, spool)
	if err != nil {
		return nil, err
	}

	result := &exam.ExportArchiveContent{SizeBytes: size, SHA256: digest}
	if !model.ValidExamExportContent(result.SizeBytes, result.SHA256) {
		return nil, errors.New("invalid Exam export archive size")
	}

	if err = c.storeVerifiedExport(ctx, examExportLabel, examExportKey(input.ExportID, input.AttemptID), spool, size, digest); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Content) validateExamExportInput(input exam.ExportArchiveInput) error {
	if c == nil ||
		c.filesystem == nil ||
		!input.ExportID.IsValid() ||
		!input.AttemptID.IsValid() ||
		len(input.Records) < 1 ||
		len(input.Records) > model.ExamExportMaximumSnapshotBytes ||
		!json.Valid(input.Records) ||
		len(input.Files) > model.ExamExportMaximumEntries {
		return errors.New("invalid Exam export input")
	}

	var envelope struct {
		ExportID model.ExamExportID `json:"export_id"`
	}
	if json.Unmarshal(input.Records, &envelope) != nil || envelope.ExportID != input.ExportID {
		return errors.New("invalid Exam export records owner")
	}

	seen := make(map[string]struct{}, len(input.Files))
	var total int64
	for _, f := range input.Files {
		if !f.SubmissionID.IsValid() || f.Entry.Validate() != nil || f.Entry.Kind != model.StarterWorkspaceEntryFile {
			return errors.New("invalid Exam export file")
		}

		name := examExportFileMember(f)
		if _, exists := seen[name]; exists {
			return errors.New("duplicate Exam export file")
		}

		seen[name] = struct{}{}
		total += f.Entry.SizeBytes
		if total > model.ExamExportMaximumSourceBytes {
			return errors.New("Exam export exceeds byte limit")
		}
	}

	return nil
}

func examExportFileMember(f exam.ExportArchiveFile) string {
	return "files/" + f.SubmissionID.String() + "/" + f.Entry.EntryID.String()
}

type examExportBoundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *examExportBoundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("Exam export archive exceeds size limit")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func (c *Content) OpenExamExport(ctx context.Context, id model.ExamExportID, attempt model.JobAttemptID) (io.ReadCloser, error) {
	if c == nil || c.filesystem == nil || !id.IsValid() || !attempt.IsValid() {
		return nil, errors.New("invalid Exam export identity")
	}
	return c.openExport(ctx, examExportLabel, examExportKey(id, attempt))
}

func (c *Content) PurgeExamExport(ctx context.Context, id model.ExamExportID, attempt model.JobAttemptID) error {
	if c == nil || c.filesystem == nil || !id.IsValid() || !attempt.IsValid() {
		return errors.New("invalid Exam export identity")
	}
	return c.purgeExport(ctx, examExportLabel, examExportKey(id, attempt))
}

func examExportKey(id model.ExamExportID, attempt model.JobAttemptID) string {
	return fmt.Sprintf("exam-exports/%s/%s.zip", id, attempt)
}
