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
	"github.com/sudosylabs/proctor/server/app/internal/exportfile"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExportQuery names one export within its scope.
type ExportQuery struct {
	Scope    model.RetentionHoldScope
	ExportID model.ExamExportID
}

// ExportDownload pairs a ready export with its archive body, which the caller
// must close.
type ExportDownload struct {
	Export *model.ExamExport
	Body   io.ReadCloser
}

// GetExamExport returns the caller's own export after re-checking authority
// over its scope and sources.
func (s *Exports) GetExamExport(
	ctx context.Context,
	inv invocation.Invocation,
	q ExportQuery,
) (_ *model.ExamExport, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	result, err := s.getExport(ctx, inv, q)
	if err != nil {
		return nil, err
	}

	return result.Export, nil
}

// OpenExamExport opens the archive of the caller's own ready export.
func (s *Exports) OpenExamExport(
	ctx context.Context,
	inv invocation.Invocation,
	q ExportQuery,
) (_ *ExportDownload, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	// The archive's lifetime runs on database time, never the node's clock.
	export, body, err := exportfile.OpenStable(ctx,
		func(ctx context.Context) (*model.ExamExport, store.ExamExportArtifact, error) {
			result, err := s.getExport(ctx, inv, q)
			if err == nil {
				err = requireReadyExport(result)
			}
			if err != nil {
				return nil, store.ExamExportArtifact{}, err
			}
			return result.Export, result.Artifact, nil
		},
		func(ctx context.Context, a store.ExamExportArtifact) (io.ReadCloser, error) {
			return s.content.OpenExamExport(ctx, a.ExportID, a.AttemptID)
		},
		func(err error) error { return apperror.New("exam.export.unavailable").Wrap(err) },
	)
	if err != nil {
		return nil, err
	}

	return &ExportDownload{Export: export, Body: body}, nil
}

func (s *Exports) getExport(ctx context.Context, inv invocation.Invocation, q ExportQuery) (*store.ExamExportDownload, error) {
	if !q.ExportID.IsValid() {
		return nil, apperror.Invalid("exam.invalid", "exam_export_id")
	}

	action, unit, err := s.authorizeExport(ctx, inv, q.Scope)
	if err != nil {
		return nil, err
	}

	result, err := s.exports.Get(
		ctx,
		&store.ExamExportAccess{
			ExamRecordsMutation: store.ExamRecordsMutation{Scope: q.Scope, Principal: inv.Principal(), Action: action},
			ExportID:            q.ExportID,
		},
	)
	if err != nil {
		return nil, examExportStoreErrors.Translate(err)
	}

	if err = s.authorizeSources(ctx, inv, q.Scope, action, unit, result.Export.Categories, result.Submissions); err != nil {
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
