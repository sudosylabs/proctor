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

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ResultReleaseMail is the ResultReleaseMailPreparer the application runs. It
// reads the candidate, the Sitting and the published Exam title, and has the
// mail composer freeze a notice that says only that a result is available.
type ResultReleaseMail struct {
	Composer  *mail.Composer
	Users     store.UserStore
	Sittings  store.ExamSittingStore
	Revisions store.ExamRevisionStore
}

// PrepareResultRelease prepares the notice for one release. The mail
// occurrence is derived from the Review and the Review revision the release
// expects, and the result carries the revision of the candidate the notice
// was prepared from.
func (m ResultReleaseMail) PrepareResultRelease(
	ctx context.Context,
	request ResultReleaseMailPreparation,
) (*PreparedResultReleaseMail, error) {
	if m.Composer == nil ||
		m.Users == nil ||
		m.Sittings == nil ||
		m.Revisions == nil ||
		!request.CandidateUserID.IsValid() ||
		!request.ExamID.IsValid() ||
		!request.SittingID.IsValid() ||
		!request.ReviewID.IsValid() ||
		request.ReleasedAt.IsZero() {
		return nil, errors.New("result release mail dependencies or request are invalid")
	}

	recipient, err := m.Users.Get(ctx, request.CandidateUserID)
	if err != nil {
		return nil, err
	}

	snapshot, err := m.Sittings.Get(ctx, request.ExamID, request.SittingID)
	if err != nil {
		return nil, err
	}

	revision, err := m.Revisions.GetSummary(ctx, request.ExamID, snapshot.Sitting.ExamRevisionID)
	if err != nil {
		return nil, err
	}

	occurrence, err := model.ResultReleaseOccurrenceID(request.ReviewID, request.ExpectedReviewRevision)
	if err != nil {
		return nil, err
	}

	prepared, err := m.Composer.PrepareResultReleaseMail(mail.ResultReleasePreparation{
		Recipient:    recipient,
		OccurrenceID: occurrence,
		Details:      mail.ResultReleaseDetails{ExamTitle: revision.Title, ReleasedAt: request.ReleasedAt},
		ReleasedAt:   request.ReleasedAt,
	})
	if err != nil {
		return nil, err
	}

	return &PreparedResultReleaseMail{Notice: &store.PreparedMail{
		Occurrence: prepared.Occurrence,
		Delivery:   prepared.Delivery,
		Job:        prepared.Job,
	}, ExpectedRecipientRevision: recipient.Revision}, nil
}
