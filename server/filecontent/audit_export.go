// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/model"
)

const auditExportLabel = "Audit export"

// BuildAuditExport spools what write produces, bounded by
// model.AuditExportMaximumBytes, then stores and verifies it. The use case
// chooses and encodes every byte; this adapter only bounds and keeps them.
func (c *Content) BuildAuditExport(
	ctx context.Context,
	id model.AuditExportID,
	attempt model.JobAttemptID,
	write func(io.Writer) error,
) (*administration.AuditExportFile, error) {
	if c == nil || c.filesystem == nil || !id.IsValid() || !attempt.IsValid() || write == nil {
		return nil, errors.New("invalid Audit export identity")
	}

	release, err := c.beginWork(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	spool, releaseSpool, err := createUnlinkedSpool("proctor-audit-export-*")
	if err != nil {
		return nil, sanitize("create "+auditExportLabel+" spool", err)
	}
	defer releaseSpool()

	if err = write(&boundedExportWriter{label: auditExportLabel, writer: spool, remaining: model.AuditExportMaximumBytes}); err != nil {
		return nil, err
	}
	size, digest, err := hashSpool(ctx, auditExportLabel, spool)
	if err != nil {
		return nil, err
	}
	if !model.ValidAuditExportContent(size, digest) {
		return nil, errors.New("invalid Audit export size")
	}
	if err = c.storeVerifiedExport(ctx, auditExportLabel, auditExportKey(id, attempt), spool, size, digest); err != nil {
		return nil, err
	}
	return &administration.AuditExportFile{SizeBytes: size, SHA256: digest}, nil
}

func (c *Content) OpenAuditExport(ctx context.Context, id model.AuditExportID, attempt model.JobAttemptID) (io.ReadCloser, error) {
	if c == nil || c.filesystem == nil || !id.IsValid() || !attempt.IsValid() {
		return nil, errors.New("invalid Audit export identity")
	}
	return c.openExport(ctx, auditExportLabel, auditExportKey(id, attempt))
}

func (c *Content) PurgeAuditExport(ctx context.Context, id model.AuditExportID, attempt model.JobAttemptID) error {
	if c == nil || c.filesystem == nil || !id.IsValid() || !attempt.IsValid() {
		return errors.New("invalid Audit export identity")
	}
	return c.purgeExport(ctx, auditExportLabel, auditExportKey(id, attempt))
}

// auditExportKey carries no format: a purge knows only the export and the
// attempt that wrote it.
func auditExportKey(id model.AuditExportID, attempt model.JobAttemptID) string {
	return fmt.Sprintf("audit-exports/%s/%s", id, attempt)
}

type boundedExportWriter struct {
	label     string
	writer    io.Writer
	remaining int64
}

func (w *boundedExportWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New(w.label + " exceeds size limit")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}
