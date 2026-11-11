// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestClassTransitionMailPreparerCreatesOneBoundedOrdinaryNotice(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	recipient := &model.User{Username: "student", Email: "student@example.edu", DisplayName: "Student", EmailVerified: true}
	recipient.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	renderer := &classTransitionRendererFake{}
	preparer, err := newDirectMailPreparer(renderer, &mailSenderFake{enabled: true,
		from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	details := ClassTransitionDetails{PreviousClassDisplayName: `Old <Class>`, ClassDisplayName: `New & Class`,
		StartsAt: at.Add(24 * time.Hour), EndsAt: at.Add(180 * 24 * time.Hour)}
	prepared, err := preparer.PrepareClassTransition(ClassTransitionPreparation{Recipient: recipient,
		OccurrenceID: model.NewMailOccurrenceID(), TemplateKey: model.MailTemplateAcademicClassTransferred,
		Details: details, ActionAt: at})
	require.NoError(t, err)
	require.Equal(t, model.MailOccurrenceAcademicAdministration, prepared.Occurrence.Kind)
	require.Equal(t, recipient.ID, prepared.Occurrence.ActorUserID)
	require.Equal(t, recipient.ID, prepared.Delivery.TargetUserID)
	require.Equal(t, model.MailTemplateAcademicClassTransferred, prepared.Delivery.TemplateKey)
	require.Equal(t, 72*time.Hour, prepared.Delivery.Deadline.Sub(prepared.Delivery.CreatedAt))
	require.Equal(t, model.JobTypeMailDeliver, prepared.Job.Type)
	require.Equal(t, details, renderer.details, "render details")
}

func TestClassTransitionMailPreparerRecordsDisabledSuppression(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	recipient := &model.User{Username: "student", Email: "student@example.edu", DisplayName: "Student"}
	recipient.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	recipient.DisabledAt = model.OptionalTimeFrom(at.Add(-time.Minute))
	renderer := &classTransitionRendererFake{}
	preparer, err := newDirectMailPreparer(renderer, &mailSenderFake{enabled: true,
		from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	prepared, err := preparer.PrepareClassTransition(ClassTransitionPreparation{
		Recipient: recipient, OccurrenceID: model.NewMailOccurrenceID(),
		TemplateKey: model.MailTemplateAcademicClassEnrolled,
		Details:     ClassTransitionDetails{ClassDisplayName: "Class A", StartsAt: at.Add(24 * time.Hour)},
		ActionAt:    at,
	})
	require.NoError(t, err)
	require.Equal(t, model.MailDeliverySuppressed, prepared.Delivery.State, "disabled Class transition mail = %#v / %#v", prepared.Delivery, prepared.Job)
	require.Equal(t, model.MailDeliveryRecipientIneligibleCode, prepared.Delivery.PublicFailureCode, "disabled Class transition mail = %#v / %#v", prepared.Delivery, prepared.Job)
	require.Empty(t, prepared.Delivery.EncryptedPayload, "disabled Class transition mail = %#v / %#v", prepared.Delivery, prepared.Job)
	require.Equal(t, model.JobStatusCanceled, prepared.Job.Status, "disabled Class transition mail = %#v / %#v", prepared.Delivery, prepared.Job)
	require.Equal(t, 0, renderer.calls, "renderer calls")
}

type classTransitionRendererFake struct {
	details ClassTransitionDetails
	calls   int
}

func (r *classTransitionRendererFake) Render(request RenderRequest) (FrozenContent, error) {
	details, ok := request.Presentation.(ClassTransitionDetails)
	if !ok {
		return FrozenContent{}, nil
	}
	r.calls++
	r.details = details
	return FrozenContent{Subject: "Class changed", Text: details.ClassDisplayName, HTML: "<p>Class changed</p>"}, nil
}
