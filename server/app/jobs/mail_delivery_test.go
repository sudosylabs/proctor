// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

type mailStoreFake struct {
	store.MailStore
	delivery    *model.MailDelivery
	gets        int
	permit      func() *store.MailSendPermit
	classes     []store.MailSendClass
	completeErr error
}

func (s *mailStoreFake) GetDelivery(_ context.Context, id model.MailDeliveryID) (*model.MailDelivery, error) {
	s.gets++
	if s.delivery == nil || s.delivery.ID != id {
		return nil, store.NewErrNotFound("mail_delivery", id.String())
	}
	return s.delivery.Clone(), nil
}

func (s *mailStoreFake) AcquireSendPermit(_ context.Context, class store.MailSendClass) (*store.MailSendPermit, error) {
	s.classes = append(s.classes, class)
	if s.permit != nil {
		return s.permit(), nil
	}
	return &store.MailSendPermit{Allowed: true}, nil
}

func TestCredentialMailDescriptorUsesDedicatedPoolAndReserve(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	record.Type = model.JobTypeMailDeliverCredential
	persistence := &mailStoreFake{delivery: delivery}
	sender := &mailSenderFake{enabled: true, from: mail.Address{Address: "no-reply@example.test"}}
	descriptor := NewMailCredentialDelivery(MailDeliveryDependencies{
		Deliveries: persistence, Sender: sender, Sealer: sealer, Relevance: mail.DeliveryIsRelevant,
		Now: func() time.Time { return at.Add(time.Second) },
	}).Descriptor
	require.Equal(t, model.JobTypeMailDeliverCredential, descriptor.Type, "credential descriptor = type")
	require.Equal(t, 4, descriptor.Concurrency, "credential descriptor = type")
	outcome := descriptor.Handler.Run(context.Background(), job.NewExecution(record, nil, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v permit classes", outcome)
	require.Len(t, persistence.classes, 1, "outcome=%#v permit classes", outcome)
	require.Equal(t, store.MailSendCredential, persistence.classes[0], "outcome=%#v permit classes", outcome)
}

func (s *mailStoreFake) StartDelivery(_ context.Context, input *store.MailDeliveryStart) (*model.MailDelivery, error) {
	id := input.ID
	revision := input.ExpectedRevision
	at := input.At
	if s.delivery == nil || s.delivery.ID != id || s.delivery.Revision != revision {
		return nil, store.NewErrConflict("mail_delivery", "stale", nil)
	}
	next, err := s.delivery.Start(at)
	if err == nil {
		s.delivery = next
	}
	return next, err
}

func (s *mailStoreFake) CompleteDelivery(_ context.Context, input *store.MailDeliveryCompletion) (*model.MailDelivery, error) {
	if s.completeErr != nil {
		return nil, s.completeErr
	}
	if s.delivery == nil || s.delivery.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("mail_delivery", "stale", nil)
	}
	var next *model.MailDelivery
	var err error
	switch input.Kind {
	case store.MailDeliveryCompletionAccepted:
		next, err = s.delivery.Accept(input.At)
	case store.MailDeliveryCompletionRetry:
		next, err = s.delivery.Retry(input.PublicFailureCode, input.At)
	case store.MailDeliveryCompletionFailed:
		next, err = s.delivery.Fail(input.PublicFailureCode, input.At)
	case store.MailDeliveryCompletionExpired:
		next, err = s.delivery.Expire(input.At)
	case store.MailDeliveryCompletionSuppress:
		next, err = s.delivery.Suppress(input.PublicFailureCode, input.At)
	}
	if err == nil {
		s.delivery = next
	}
	return next, err
}

// jobMailMetricsFake records the delivery and attempt metrics the delivery
// Jobs emit; they record no queue or health metric.
type jobMailMetricsFake struct {
	mail.DeliveryRecorder
	values   []mail.DeliveryMetric
	attempts []mail.AttemptMetric
}

func (r *jobMailMetricsFake) RecordMailDelivery(_ context.Context, metric mail.DeliveryMetric) {
	r.values = append(r.values, metric)
}

func (r *jobMailMetricsFake) RecordMailAttempt(_ context.Context, metric mail.AttemptMetric) {
	r.attempts = append(r.attempts, metric)
}

type mailRendererFake struct{ content mail.FrozenContent }

func (r mailRendererFake) Render(mail.RenderRequest) (mail.FrozenContent, error) {
	return r.content, nil
}

func TestMailDeliveryHandlerUsesStableMessageIDAndRecordsAcceptance(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, 24*time.Hour)
	persistence := &mailStoreFake{delivery: delivery}
	sender := &mailSenderFake{enabled: true, from: mail.Address{Address: "new-sender@example.test"}}
	now := at.Add(time.Second)
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { now = now.Add(time.Second); return now }, record)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Len(t, sender.messages, 1, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, delivery.MessageID, sender.messages[0].MessageID, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.True(t, sender.messages[0].Date.Equal(at), "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, "no-reply@example.test", sender.messages[0].From.Address, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, "auto-generated", sender.messages[0].Headers["Auto-Submitted"][0], "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, "All", sender.messages[0].Headers["X-Auto-Response-Suppress"][0], "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliveryAccepted, persistence.delivery.State, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, persistence.delivery.EncryptedPayload, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	second := runMailDeliveryJob(persistence, sender, sealer, time.Now, record)
	require.Equal(t, job.OutcomeSucceeded, second.Kind)
	require.Len(t, sender.messages, 1)
}

func TestMailDeliveryHandlerRecordsAttemptBeforeCompletionFailure(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	completionErr := errors.New("completion unavailable")
	persistence := &mailStoreFake{delivery: delivery, completeErr: completionErr}
	sender := &mailSenderFake{enabled: true, from: mail.Address{Address: "no-reply@example.test"}}
	recorder := &jobMailMetricsFake{}
	descriptor := NewMailDelivery(MailDeliveryDependencies{
		Deliveries: persistence, Sender: sender, Sealer: sealer, Recorder: recorder, Relevance: mail.DeliveryIsRelevant,
		Now: func() time.Time { return at.Add(time.Second) },
	}).Descriptor
	outcome := descriptor.Handler.Run(context.Background(), job.NewExecution(record, nil, nil, nil))
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind)
	require.ErrorIs(t, outcome.Err, completionErr)
	require.Equal(t, model.MailDeliverySending, persistence.delivery.State)
	require.Equal(t, 1, persistence.delivery.AttemptCount)
	require.Empty(t, recorder.values, "mail metrics = deliveries")
	require.Len(t, recorder.attempts, 1, "mail metrics = deliveries")
	require.Equal(t, model.MailDeliverySending, recorder.attempts[0].State, "mail metrics = deliveries")
	require.Equal(t, delivery.TemplateKey, recorder.attempts[0].TemplateKey, "mail metrics = deliveries")
}

func TestMailDeliveryHandlerExpiresQueuedDeliveryWithoutSending(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Second)
	persistence := &mailStoreFake{delivery: delivery}
	sender := &mailSenderFake{enabled: true, from: mail.Address{Address: "no-reply@example.test"}}
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return delivery.Deadline }, record)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, sender.messages, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliverySuppressed, persistence.delivery.State, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliveryExpiredCode, persistence.delivery.PublicFailureCode, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, persistence.delivery.EncryptedPayload, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
}

func TestMailDeliveryHandlerSuppressesDisabledWorkWithoutSealerOrSend(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	_, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	persistence := &mailStoreFake{delivery: delivery}
	sender := &mailSenderFake{enabled: false}
	outcome := runMailDeliveryJob(persistence, sender, nil, func() time.Time { return at.Add(time.Second) }, record)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, sender.messages, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliverySuppressed, persistence.delivery.State, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliveryDisabledCode, persistence.delivery.PublicFailureCode, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, persistence.delivery.EncryptedPayload, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
}

func TestMailDeliveryHandlerRechecksDisabledStateAfterWaitingForPermit(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	sender := &mailSenderFake{enabled: true}
	persistence := &mailStoreFake{delivery: delivery}
	persistence.permit = func() *store.MailSendPermit {
		sender.enabled = false
		return &store.MailSendPermit{Allowed: true}
	}
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(time.Second) }, record)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, sender.messages, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliverySuppressed, persistence.delivery.State, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliveryDisabledCode, persistence.delivery.PublicFailureCode, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, persistence.delivery.EncryptedPayload, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
}

func TestMailDeliveryHandlerExpiresRetryWhenSMTPReturnsAfterDeadline(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, 2*time.Second)
	persistence := &mailStoreFake{delivery: delivery}
	now := at.Add(time.Second)
	sender := &mailSenderFake{
		enabled: true, from: mail.Address{Address: "no-reply@example.test"},
		outcome: mail.TransportTemporary, err: errors.New("temporary SMTP failure"),
		afterSend: func() { now = delivery.Deadline },
	}
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return now }, record)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Len(t, sender.messages, 1, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliverySuppressed, persistence.delivery.State, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Equal(t, model.MailDeliveryExpiredCode, persistence.delivery.PublicFailureCode, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
	require.Empty(t, persistence.delivery.EncryptedPayload, "outcome=%#v messages=%#v delivery=%#v", outcome, sender.messages, persistence.delivery)
}

func mailDeliveryHandlerFixture(t *testing.T, at time.Time, deadlineAfter time.Duration) (*secretseal.Sealer, *model.Job, *model.MailDelivery) {
	t.Helper()
	sealer := mailTestSealer(t)
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: mailRendererFake{mail.FrozenContent{Subject: "test", Text: "text", HTML: "<p>html</p>"}}, Sender: &mailSenderFake{enabled: true, from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}}, Sealer: sealer})
	require.NoError(t, err)
	prepared, err := composer.PrepareOperatorTest(mail.NoticePreparation{Recipient: mailTestUser(mailTestPrincipal(at), at), At: at})
	require.NoError(t, err)
	prepared.Delivery.Deadline = at.Add(deadlineAfter)
	return sealer, prepared.Job, prepared.Delivery
}

func runMailDeliveryJob(persistence store.MailStore, sender mail.Sender,
	sealer *secretseal.Sealer, now func() time.Time, record *model.Job,
) job.Outcome {
	descriptor := NewMailDelivery(MailDeliveryDependencies{
		Deliveries: persistence, Sender: sender, Sealer: sealer, Relevance: mail.DeliveryIsRelevant, Now: now,
	}).Descriptor
	return descriptor.Handler.Run(context.Background(), job.NewExecution(record, nil, nil, nil))
}

func TestMailDeliveryHandlerClassifiesRetryableAndPermanentFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		outcome mail.TransportOutcome
		code    string
		kind    job.OutcomeKind
		state   model.MailDeliveryState
	}{
		{"temporary", mail.TransportTemporary, "mail.transport.temporary", job.OutcomeRetryableFailure, model.MailDeliveryQueued},
		{"permanent", mail.TransportPermanent, "mail.transport.permanent", job.OutcomePermanentFailure, model.MailDeliveryFailed},
		{"unknown", mail.TransportUnknown, "mail.transport.unknown", job.OutcomeRetryableFailure, model.MailDeliveryQueued},
		{"acceptance uncertain", mail.TransportAcceptanceUncertain, "mail.transport.acceptance_uncertain", job.OutcomeRetryableFailure, model.MailDeliveryQueued},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
			sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
			persistence := &mailStoreFake{delivery: delivery}
			sendErr := errors.New("private provider detail")
			sender := &mailSenderFake{enabled: true, outcome: test.outcome, err: sendErr}
			outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(time.Second) }, record)
			require.Equal(t, test.kind, outcome.Kind, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			require.Equal(t, test.code, outcome.PublicErrorCode, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			require.ErrorIs(t, outcome.Err, sendErr, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			require.Equal(t, test.state, persistence.delivery.State, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			require.Equal(t, test.code, persistence.delivery.PublicFailureCode, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			require.Equal(t, 1, persistence.delivery.AttemptCount, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			require.NotEmpty(t, persistence.delivery.EncryptedPayload, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			require.Len(t, sender.messages, 1, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
			// Both automatic retries and a repeated terminal Job preserve the
			// first send's identity; only retryable work may reach Sender again.
			sender.err = nil
			sender.from = mail.Address{Address: "changed@example.test"}
			second := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(2 * time.Second) }, record)
			if test.state == model.MailDeliveryFailed {
				require.Equal(t, job.OutcomePermanentFailure, second.Kind, "permanent failure was resent: %#v, sends", second)
				require.Len(t, sender.messages, 1, "permanent failure was resent: %#v, sends", second)
				return
			}
			require.Equal(t, job.OutcomeSucceeded, second.Kind, "retry=%#v delivery=%#v messages", second, persistence.delivery)
			require.Equal(t, model.MailDeliveryAccepted, persistence.delivery.State, "retry=%#v delivery=%#v messages", second, persistence.delivery)
			require.Empty(t, persistence.delivery.EncryptedPayload, "retry=%#v delivery=%#v messages", second, persistence.delivery)
			require.Len(t, sender.messages, 2, "retry=%#v delivery=%#v messages", second, persistence.delivery)
			require.Equal(t, sender.messages[1], sender.messages[0], "retry=%#v delivery=%#v messages", second, persistence.delivery)
		})
	}
}

func TestMailDeliveryHandlerFailsTemporaryFailureOnLastJobAttempt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	record.AttemptCount = record.MaximumAttempts
	persistence := &mailStoreFake{delivery: delivery}
	sender := &mailSenderFake{enabled: true, outcome: mail.TransportTemporary, err: errors.New("try later")}
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(time.Second) }, record)
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind, "outcome=%#v", outcome)
	require.Equal(t, model.MailDeliveryFailed, persistence.delivery.State, "delivery left without a Job")
	require.Equal(t, "mail.transport.temporary", persistence.delivery.PublicFailureCode)
}

func TestMailDeliveryHandlerFailsExhaustedSendingDeliveryWithoutSending(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	sending, err := delivery.Start(at)
	require.NoError(t, err)
	sending.AttemptCount = model.MailMaximumAttempts
	persistence := &mailStoreFake{delivery: sending}
	sender := &mailSenderFake{enabled: true}
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(time.Second) }, record)
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind, "outcome=%#v", outcome)
	require.Equal(t, "mail.transport.acceptance_uncertain", outcome.PublicErrorCode)
	require.Equal(t, model.MailDeliveryFailed, persistence.delivery.State, "exhausted delivery stayed Sending")
	require.Empty(t, sender.messages, "exhausted delivery was sent again")
}

func TestMailDeliveryHandlerFailsUnavailablePayloadWithoutSending(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	_, _, otherDelivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	// A valid envelope from another delivery must fail the owner binding.
	delivery.EncryptedPayload = otherDelivery.EncryptedPayload
	persistence := &mailStoreFake{delivery: delivery}
	sender := &mailSenderFake{enabled: true}
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(time.Second) }, record)
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Equal(t, "mail.payload.unavailable", outcome.PublicErrorCode, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Equal(t, model.MailDeliveryFailed, persistence.delivery.State, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Equal(t, 1, persistence.delivery.AttemptCount, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Empty(t, sender.messages, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
}

func TestMailDeliveryHandlerFailsInvalidMessageWithoutSending(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sealer, record, delivery := mailDeliveryHandlerFixture(t, at, time.Hour)
	persistence := &mailInvalidMessageMetadataStoreFake{mailStoreFake: &mailStoreFake{delivery: delivery}}
	sender := &mailSenderFake{enabled: true}
	outcome := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(time.Second) }, record)
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Equal(t, "mail.message.invalid", outcome.PublicErrorCode, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.ErrorIs(t, outcome.Err, mail.ErrInvalidMessage, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Equal(t, model.MailDeliveryFailed, persistence.delivery.State, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Equal(t, "mail.message.invalid", persistence.delivery.PublicFailureCode, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Equal(t, 1, persistence.delivery.AttemptCount, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.Empty(t, sender.messages, "outcome=%#v delivery=%#v sends", outcome, persistence.delivery)
	require.NotContains(t, outcome.Err.Error(), "hidden@example.test", "worker error disclosed invalid message metadata")
	failedRevision := persistence.delivery.Revision
	second := runMailDeliveryJob(persistence, sender, sealer, func() time.Time { return at.Add(2 * time.Second) }, record)
	require.Equal(t, job.OutcomePermanentFailure, second.Kind, "terminal replay changed invalid delivery: outcome=%#v delivery=%#v sends", second, persistence.delivery)
	require.Equal(t, "mail.message.invalid", second.PublicErrorCode, "terminal replay changed invalid delivery: outcome=%#v delivery=%#v sends", second, persistence.delivery)
	require.Equal(t, failedRevision, persistence.delivery.Revision, "terminal replay changed invalid delivery: outcome=%#v delivery=%#v sends", second, persistence.delivery)
	require.Equal(t, 1, persistence.delivery.AttemptCount, "terminal replay changed invalid delivery: outcome=%#v delivery=%#v sends", second, persistence.delivery)
	require.Empty(t, sender.messages, "terminal replay changed invalid delivery: outcome=%#v delivery=%#v sends", second, persistence.delivery)
}

type mailInvalidMessageMetadataStoreFake struct {
	*mailStoreFake
}

func (s *mailInvalidMessageMetadataStoreFake) StartDelivery(ctx context.Context, input *store.MailDeliveryStart) (*model.MailDelivery, error) {
	id := input.ID
	revision := input.ExpectedRevision
	at := input.At
	delivery, err := s.mailStoreFake.StartDelivery(ctx, &store.MailDeliveryStart{ID: id, ExpectedRevision: revision, At: at})
	if err != nil {
		return nil, err
	}
	// Keep the stored record valid while exercising the worker's defensive
	// validation of returned metadata without interpreting Mail's payload.
	delivery = delivery.Clone()
	delivery.MessageID += "\r\nBcc: hidden@example.test"
	return delivery, nil
}
