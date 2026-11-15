// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListAttemptEvidence returns one page of the Attempt evidence sealed
// with a Submission to a caller authorized to view its Submission Review. The
// records raise no Flag, so they are listed beside the Flags rather than
// under one; the snapshot carries their sealed inventory.
func (s *Service) ListAttemptEvidence(
	ctx context.Context,
	inv invocation.Invocation,
	query store.AttemptEvidenceListOptions,
) (_ *store.AttemptEvidencePage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.SubmissionID.IsValid() || query.Limit < 1 || query.Limit > store.AttemptEvidenceReviewMaximum ||
		query.AfterID != "" && !query.AfterID.IsValid() {
		return nil, apperror.Invalid("exam.integrity_review.invalid", "attempt_evidence_list")
	}
	if err := s.authorizer.AuthorizeSubmissionView(ctx, inv, query.SubmissionID); err != nil {
		return nil, err
	}

	page, err := s.reviews.ListAttemptEvidence(ctx, query)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}
	if page == nil || page.Items == nil || len(page.Items) > query.Limit || page.HasMore && len(page.Items) != query.Limit {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid Attempt evidence page"))
	}

	result := &store.AttemptEvidencePage{Items: make([]model.AttemptEvidence, len(page.Items)), HasMore: page.HasMore}
	previous := query.AfterID.String()
	for i, value := range page.Items {
		if value.Validate() != nil || value.ID.String() <= previous {
			return nil, apperror.New("exam.integrity_review.unavailable").Wrap(model.ErrAttemptEvidenceInvalid)
		}
		previous = value.ID.String()
		value.Detail = append([]byte(nil), value.Detail...)
		result.Items[i] = value
	}
	return result, nil
}
