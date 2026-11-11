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
	"io"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExportQuery names one Examination Export within its scope.
type ExportQuery struct {
	Scope    model.RetentionHoldScope
	ExportID model.ExamExportID
}

// ExportDownload pairs a ready Examination Export with its archive body,
// which the caller must close.
type ExportDownload struct {
	Export *model.ExamExport
	Body   io.ReadCloser
}

// GetExamExport returns the caller's own Examination Export after re-checking
// authority over its scope and sources.
func (s *Exports) GetExamExport(
	ctx context.Context,
	invocation invocation.Invocation,
	q ExportQuery,
) (_ *model.ExamExport, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	result, err := s.getExport(ctx, invocation, q)
	if err != nil {
		return nil, err
	}

	return result.Export, nil
}

// OpenExamExport opens the archive of the caller's own ready Examination
// Export, re-reading the export after opening so expiry or a change in
// authority is not bypassed.
func (s *Exports) OpenExamExport(
	ctx context.Context,
	invocation invocation.Invocation,
	q ExportQuery,
) (_ *ExportDownload, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	result, err := s.getExport(ctx, invocation, q)
	if err != nil {
		return nil, err
	}
	if err = requireReadyExport(result); err != nil {
		return nil, err
	}

	body, err := s.content.OpenExamExport(ctx, result.Artifact.ExportID, result.Artifact.AttemptID)
	if err != nil {
		return nil, apperror.New("exam.export.unavailable").Wrap(err)
	}
	if body == nil {
		return nil, apperror.New("exam.export.unavailable").Wrap(errors.New("missing export body"))
	}

	// Storage can block across expiry or an authority change. Re-read through
	// the authoritative Store before exposing a body; node wall-clock time
	// cannot extend or shorten the archive's PostgreSQL-clocked lifetime.
	current, err := s.getExport(ctx, invocation, q)
	if err == nil {
		err = requireReadyExport(current)
	}
	if err == nil && current.Artifact != result.Artifact {
		err = apperror.New("exam.export.unavailable").Wrap(errors.New("export artifact changed during open"))
	}

	if err != nil {
		_ = body.Close()
		return nil, err
	}

	return &ExportDownload{Export: current.Export, Body: body}, nil
}

func (s *Exports) getExport(ctx context.Context, invocation invocation.Invocation, q ExportQuery) (*store.ExamExportDownload, error) {
	if !q.ExportID.IsValid() {
		return nil, apperror.Invalid("exam.invalid", "exam_export_id")
	}

	action, unit, err := s.authorizeExport(ctx, invocation, q.Scope)
	if err != nil {
		return nil, err
	}

	result, err := s.exports.Get(
		ctx,
		&store.ExamExportAccess{
			ExamRecordsMutation: store.ExamRecordsMutation{Scope: q.Scope, Principal: invocation.Principal(), Action: action},
			ExportID:            q.ExportID,
		},
	)
	if err != nil {
		return nil, examExportStoreErrors.Translate(err)
	}

	if !result.ConsistentWith(q.ExportID, q.Scope, invocation.Principal().UserID) {
		return nil, apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export read result"))
	}

	if err = s.authorizeSources(ctx, invocation, q.Scope, action, unit, result.Export.Categories, result.Submissions); err != nil {
		return nil, err
	}

	return result, nil
}

func requireReadyExport(result *store.ExamExportDownload) error {
	if result.Export.State == model.ExamExportExpired {
		return apperror.New("exam.export.expired")
	}
	if result.Export.State != model.ExamExportReady {
		return apperror.New("exam.export.not_ready")
	}
	if result.Artifact.ExportID != result.Export.ID || !result.Artifact.AttemptID.IsValid() {
		return apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export artifact"))
	}
	return nil
}
