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

func TestResultReleaseMailPreparerContainsOnlyAvailabilityFacts(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	recipient := &model.User{Username: "candidate", Email: "candidate@example.edu", DisplayName: "Candidate"}
	recipient.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	preparer, err := newDirectMailPreparer(mailTestRenderer(t), &mailSenderFake{enabled: true,
		from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	reviewID := model.NewSubmissionReviewID()
	details := ResultReleaseDetails{ExamTitle: "Algorithms", ReleasedAt: at}
	prepared, err := preparer.PrepareResultReleaseMail(ResultReleasePreparation{Recipient: recipient,
		OccurrenceID: model.MailOccurrenceID(reviewID.String()), Details: details, ReleasedAt: at})
	require.NoError(t, err)
	require.Equal(t, model.MailOccurrenceResultRelease, prepared.Occurrence.Kind, "prepared=%#v details", prepared)
	require.Equal(t, model.MailOccurrenceID(reviewID.String()), prepared.Occurrence.ID, "prepared=%#v details", prepared)
	require.Equal(t, recipient.ID, prepared.Occurrence.ActorUserID, "prepared=%#v details", prepared)
	require.Equal(t, recipient.ID, prepared.Delivery.TargetUserID, "prepared=%#v details", prepared)
	require.Equal(t, model.MailTemplateExamResultReleased, prepared.Delivery.TemplateKey, "prepared=%#v details", prepared)
	require.Equal(t, 72*time.Hour, prepared.Delivery.Deadline.Sub(prepared.Delivery.CreatedAt), "prepared=%#v details", prepared)
	opened := openedMail(t, prepared.Delivery)
	require.Contains(t, opened.Text, "Exam: "+details.ExamTitle, "the notice names the Exam")
	require.Contains(t, opened.Text, "Released: Friday 21 August 2026, 14:00 UTC", "the notice shows when the result was released")
	relevance, relevanceErr := evaluateDeliveryRelevance(context.Background(), prepared.Delivery)
	require.NoError(t, relevanceErr, "release relevance")
	require.Equal(t, deliveryRelevant, relevance, "release relevance")
}

func TestResultReleaseMailPreparerSuppressesInactiveCandidate(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	recipient := &model.User{Username: "candidate", Email: "candidate@example.edu", DisplayName: "Candidate"}
	recipient.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	recipient.DisabledAt = model.OptionalTimeFrom(at.Add(-time.Minute))
	preparer, err := newDirectMailPreparer(mailTestRenderer(t), &mailSenderFake{enabled: true,
		from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	prepared, err := preparer.PrepareResultReleaseMail(ResultReleasePreparation{Recipient: recipient,
		OccurrenceID: model.MailOccurrenceID(model.NewSubmissionReviewID().String()),
		Details:      ResultReleaseDetails{ExamTitle: "Algorithms", ReleasedAt: at}, ReleasedAt: at})
	require.NoError(t, err)
	require.Equal(t, model.MailDeliverySuppressed, prepared.Delivery.State)
	require.Equal(t, model.MailDeliveryRecipientIneligibleCode, prepared.Delivery.PublicFailureCode)
}
