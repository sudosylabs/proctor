// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

type uncertainExamExportWrite struct{ cause error }

func (e *uncertainExamExportWrite) Error() string           { return "Exam export write outcome is uncertain" }
func (e *uncertainExamExportWrite) Unwrap() error           { return e.cause }
func (*uncertainExamExportWrite) ExamExportWriteUncertain() {}

// BuildExamExport writes only the pre-registered opaque attempt identity. It
// verifies every original file while the configured export format reads it,
// then reopens and hashes the managed object before returning verified
// publication metadata. It fails when no export format is configured.
func (c *Content) BuildExamExport(ctx context.Context, input exam.ExportArchiveInput) (*exam.ExportArchiveContent, error) {
	if c == nil || c.examExportFormat == nil {
		return nil, errExamExportFormatUnavailable
	}
	return c.buildExamExport(ctx, input, c.examExportFormat)
}

// errExamExportFormatUnavailable is returned when no plugin offers the Exam
// export format, because the plugin is turned off or has failed.
var errExamExportFormatUnavailable = errors.New("Exam export format is unavailable")

// buildExamExport owns everything around format: validation, the work
// budget, the private spool and its size bound, per-file verification,
// hashing and storage. format only encodes the archive it is handed.
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
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		return nil, sanitize("rewind Exam export spool", err)
	}

	hash := sha256.New()
	size, err := io.CopyBuffer(hash, workReader{ctx: ctx, reader: spool}, make([]byte, 32<<10))
	if err != nil {
		return nil, sanitize("hash Exam export archive", err)
	}

	result := &exam.ExportArchiveContent{SizeBytes: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	if !model.ValidExamExportContent(result.SizeBytes, result.SHA256) {
		return nil, errors.New("invalid Exam export archive size")
	}

	return c.storeExamExport(ctx, examExportKey(input.ExportID, input.AttemptID), spool, result)
}

// validateExamExportInput rejects malformed records, foreign owners, invalid or duplicate files and oversized sources.
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

// storeExamExport writes the spooled archive under key and publishes it only after an independent read verifies it.
func (c *Content) storeExamExport(
	ctx context.Context,
	key string,
	spool *os.File,
	result *exam.ExportArchiveContent,
) (*exam.ExportArchiveContent, error) {
	conditional := c.filesystem.Capabilities().ConditionalWrite
	if !conditional {
		matching, verifyErr := c.verifyExamExport(ctx, key, result)
		if verifyErr == nil {
			if matching {
				return result, nil
			}
			return nil, errors.New("Exam export object conflict")
		}

		if !errors.Is(verifyErr, vfs.ErrNotFound) {
			return nil, sanitize("inspect Exam export object", verifyErr)
		}
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, sanitize("rewind Exam export spool", err)
	}

	size := result.SizeBytes
	_, writeErr := c.filesystem.Write(ctx, key, workReader{ctx: ctx, reader: spool}, vfs.WriteOptions{Size: &size, NoOverwrite: conditional})
	// An error acknowledgement can still mean the exact immutable write
	// committed. Never publish until an independent read proves all bytes.
	matching, verifyErr := c.verifyExamExport(ctx, key, result)
	if verifyErr != nil || !matching {
		if writeErr != nil {
			return nil, &uncertainExamExportWrite{cause: sanitize("store Exam export archive", writeErr)}
		}
		if verifyErr != nil {
			return nil, sanitize("verify Exam export archive", verifyErr)
		}

		return nil, errors.New("Exam export archive verification failed")
	}

	return result, nil
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

func (c *Content) verifyExamExport(ctx context.Context, key string, expected *exam.ExportArchiveContent) (bool, error) {
	file, err := c.filesystem.Open(ctx, key, vfs.OpenOptions{})
	if err != nil {
		return false, err
	}
	defer file.Body.Close()
	if file.Info.Size != expected.SizeBytes {
		return false, nil
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(hash, io.LimitReader(workReader{ctx: ctx, reader: file.Body}, expected.SizeBytes+1), make([]byte, 32<<10))
	return err == nil && size == expected.SizeBytes && hex.EncodeToString(hash.Sum(nil)) == expected.SHA256, err
}

func (c *Content) OpenExamExport(ctx context.Context, id model.ExamExportID, attempt model.JobAttemptID) (io.ReadCloser, error) {
	if c == nil || c.filesystem == nil || !id.IsValid() || !attempt.IsValid() {
		return nil, errors.New("invalid Exam export identity")
	}
	file, err := c.filesystem.Open(ctx, examExportKey(id, attempt), vfs.OpenOptions{})
	if err != nil {
		return nil, sanitize("open Exam export archive", err)
	}
	return file.Body, nil
}

func (c *Content) PurgeExamExport(ctx context.Context, id model.ExamExportID, attempt model.JobAttemptID) error {
	if c == nil || c.filesystem == nil || !id.IsValid() || !attempt.IsValid() {
		return errors.New("invalid Exam export identity")
	}
	key := examExportKey(id, attempt)
	if err := c.filesystem.Remove(ctx, key, vfs.RemoveOptions{}); err != nil && !errors.Is(err, vfs.ErrNotFound) {
		return sanitize("remove Exam export archive", err)
	}
	_, err := c.filesystem.Stat(ctx, key)
	if errors.Is(err, vfs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return sanitize("observe Exam export absence", err)
	}
	return errors.New("Exam export archive remains present")
}

func examExportKey(id model.ExamExportID, attempt model.JobAttemptID) string {
	return fmt.Sprintf("exam-exports/%s/%s.zip", id, attempt)
}
