// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// ListPreviewUsage reads the sealed preview inventory under the same
// authorization as other Attempt evidence.
func (s *Service) ListPreviewUsage(ctx context.Context, inv invocation.Invocation, id model.SubmissionID) (_ []model.PreviewUsage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !id.IsValid() {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "submission_id")
	}
	if err := s.authorizer.AuthorizeSubmissionView(ctx, inv, id); err != nil {
		return nil, err
	}
	if s.environments == nil {
		return nil, apperror.New("exam.integrity_review.unavailable")
	}
	usage, err := s.environments.ListPreviewUsage(ctx, id)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	return usage, nil
}
