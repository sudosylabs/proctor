// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
package attempt

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SubmissionReceiptMail prepares the candidate's receipt, frozen for that
// candidate alone.
type SubmissionReceiptMail struct {
	Composer  *mail.Composer
	Users     store.UserStore
	Sittings  store.ExamSittingStore
	Revisions store.ExamRevisionStore
}

// PrepareSubmissionReceipt words the receipt by how the submission was
// sealed. The submission itself identifies the mail occurrence.
func (m SubmissionReceiptMail) PrepareSubmissionReceipt(
	ctx context.Context,
	request SubmissionMailPreparation,
) (*PreparedSubmissionMail, error) {
	if m.Composer == nil ||
		m.Users == nil ||
		m.Sittings == nil ||
		m.Revisions == nil ||
		!request.CandidateUserID.IsValid() ||
		!request.ExamID.IsValid() ||
		!request.SittingID.IsValid() ||
		!request.SubmissionID.IsValid() ||
		!request.ReceiptCode.IsValid() ||
		request.SealedAt.IsZero() {
		return nil, errors.New("Submission receipt dependencies or request are invalid")
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

	key := model.MailTemplateExamSubmissionReceived
	switch request.Provenance {
	case model.ExamSubmissionCandidateSubmitted:
	case model.ExamSubmissionManagerEndedAttempt:
		key = model.MailTemplateExamSubmissionManagerEnded
	case model.ExamSubmissionSittingClosed:
		key = model.MailTemplateExamSubmissionAutomaticallySealed
	default:
		return nil, errors.New("Submission receipt provenance is invalid")
	}

	prepared, err := m.Composer.PrepareSubmissionReceiptMail(mail.SubmissionReceiptPreparation{
		Recipient:    recipient,
		OccurrenceID: model.MailOccurrenceID(request.SubmissionID.String()),
		TemplateKey:  key,
		Details: mail.SubmissionReceiptDetails{
			ExamTitle:    revision.Title,
			SittingID:    request.SittingID,
			SubmissionID: request.SubmissionID,
			ReceiptCode:  request.ReceiptCode,
			SealedAt:     request.SealedAt,
		},
		ActionAt: request.SealedAt,
	})
	if err != nil {
		return nil, err
	}

	return &PreparedSubmissionMail{Notice: &store.PreparedMail{
		Occurrence: prepared.Occurrence,
		Delivery:   prepared.Delivery,
		Job:        prepared.Job,
	}, ExpectedRecipientRevision: recipient.Revision}, nil
}
