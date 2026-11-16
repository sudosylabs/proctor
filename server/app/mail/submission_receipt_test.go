// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestSubmissionReceiptMailPreparerCreatesBoundedReceipt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 21, 9, 30, 0, 0, time.UTC)
	recipient := &model.User{Username: "candidate", Email: "candidate@example.edu", DisplayName: "Candidate"}
	recipient.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	preparer, err := newDirectMailPreparer(mailTestRenderer(t), &mailSenderFake{enabled: true,
		from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	details := SubmissionReceiptDetails{ExamTitle: "Algorithms", SittingID: model.NewExamSittingID(),
		SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(), SealedAt: at}
	prepared, err := preparer.PrepareSubmissionReceiptMail(SubmissionReceiptPreparation{Recipient: recipient,
		OccurrenceID: model.MailOccurrenceID(details.SubmissionID.String()), TemplateKey: model.MailTemplateExamSubmissionReceived,
		Details: details, ActionAt: at})
	require.NoError(t, err)
	require.Equal(t, model.MailOccurrenceSubmissionReceipt, prepared.Occurrence.Kind, "prepared=%#v details", prepared)
	require.Equal(t, recipient.ID, prepared.Occurrence.ActorUserID, "prepared=%#v details", prepared)
	require.Equal(t, recipient.ID, prepared.Delivery.TargetUserID, "prepared=%#v details", prepared)
	require.Equal(t, model.MailTemplateExamSubmissionReceived, prepared.Delivery.TemplateKey, "prepared=%#v details", prepared)
	require.Equal(t, 72*time.Hour, prepared.Delivery.Deadline.Sub(prepared.Delivery.CreatedAt), "prepared=%#v details", prepared)
	require.Equal(t, model.JobTypeMailDeliver, prepared.Job.Type, "prepared=%#v details", prepared)
	opened := openedMail(t, prepared.Delivery)
	require.Contains(t, opened.Text, "Exam: "+details.ExamTitle, "the receipt names the Exam")
	require.Contains(t, opened.Text, "Receipt code: "+details.ReceiptCode.Display(), "the receipt shows its code")
	require.Contains(t, opened.Text, "Submitted: Friday 21 August 2026, 09:30:00 UTC", "the receipt shows when it was sealed")
	relevance, relevanceErr := evaluateDeliveryRelevance(context.Background(), prepared.Delivery)
	require.NoError(t, relevanceErr, "receipt relevance")
	require.Equal(t, deliveryRelevant, relevance, "receipt relevance")

	for name, code := range map[string]model.SubmissionReceiptCode{"missing": "", "display form": model.SubmissionReceiptCode(details.ReceiptCode.Display())} {
		invalid := details
		invalid.ReceiptCode = code
		_, err = preparer.PrepareSubmissionReceiptMail(SubmissionReceiptPreparation{Recipient: recipient,
			OccurrenceID: model.MailOccurrenceID(details.SubmissionID.String()), TemplateKey: model.MailTemplateExamSubmissionReceived,
			Details: invalid, ActionAt: at})
		require.Error(t, err, "%s receipt code accepted", name)
	}
}

func TestSubmissionReceiptMailPreparerSuppressesInactiveRecipient(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 21, 9, 30, 0, 0, time.UTC)
	recipient := &model.User{Username: "candidate", Email: "candidate@example.edu", DisplayName: "Candidate"}
	recipient.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	recipient.DisabledAt = model.OptionalTimeFrom(at.Add(-time.Minute))
	preparer, err := newDirectMailPreparer(mailTestRenderer(t), &mailSenderFake{enabled: true,
		from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	submissionID := model.NewSubmissionID()
	prepared, err := preparer.PrepareSubmissionReceiptMail(SubmissionReceiptPreparation{Recipient: recipient,
		OccurrenceID: model.MailOccurrenceID(submissionID.String()), TemplateKey: model.MailTemplateExamSubmissionAutomaticallySealed,
		Details: SubmissionReceiptDetails{ExamTitle: "Algorithms", SittingID: model.NewExamSittingID(),
			SubmissionID: submissionID, ReceiptCode: model.NewSubmissionReceiptCode(), SealedAt: at}, ActionAt: at})
	require.NoError(t, err)
	require.Equal(t, model.MailDeliverySuppressed, prepared.Delivery.State, "suppressed=%#v", prepared.Delivery)
	require.Equal(t, model.MailDeliveryRecipientIneligibleCode, prepared.Delivery.PublicFailureCode, "suppressed=%#v", prepared.Delivery)
	require.Empty(t, prepared.Delivery.EncryptedPayload, "a suppressed receipt renders and seals nothing")
}
