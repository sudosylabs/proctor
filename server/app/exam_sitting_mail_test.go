// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSittingMailPreparerFreezesAllFourSafeRenderVariants(t *testing.T) {
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	sitting, err := model.NewExamSitting(model.NewExamSittingID(), model.NewExamID(), model.NewExamRevisionID(), model.NewClassID(),
		at.Add(24*time.Hour), at.Add(26*time.Hour), at)
	require.NoError(t, err)
	renderer := sittingMailRendererFake{}
	preparer, err := newSittingMailPreparer(renderer, &mailSenderFake{enabled: true,
		from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), func() time.Time { return at }, nil)
	require.NoError(t, err)
	prepared, err := preparer.Prepare(model.NewUserID(), sitting, store.ExamSittingMailScheduled,
		mail.SittingScheduleDetails{ExamTitle: "Algorithms", ClassDisplayName: "Class A"})
	require.NoError(t, err)
	require.Equal(t, model.MailOccurrenceSittingSchedule, prepared.Occurrence.Kind)
	require.NotNil(t, prepared.Bundle)
	require.Equal(t, model.JobTypeMailExpandSitting, prepared.ExpansionJob.Type)
	require.Equal(t, model.JobDedupePermanent, prepared.ExpansionJob.DedupePolicy)
	for _, forbidden := range []string{"Algorithms", "Class A", "no-reply@example.test"} {
		require.NotContains(t, string(prepared.Bundle.EncryptedPayload), forbidden)
	}
	opened, err := preparer.OpenBundle(prepared.Bundle)
	require.NoError(t, err, "opened bundle=(%#v,%v)", opened, err)
	require.Len(t, opened.Messages, 1, "opened bundle=(%#v,%v)", opened, err)
	require.NotEmpty(t, opened.Messages["en"][model.MailTemplateExamSittingCanceled].Subject, "opened bundle=(%#v,%v)", opened, err)
}

type sittingMailRendererFake struct{}

func (sittingMailRendererFake) SittingLocales() (string, []string) { return "en", []string{"en"} }

func (sittingMailRendererFake) Render(request mail.RenderRequest) (mail.FrozenContent, error) {
	details, ok := request.Presentation.(mail.SittingScheduleDetails)
	if !ok {
		return mail.FrozenContent{}, nil
	}
	key := request.Key
	return mail.FrozenContent{Subject: string(key), Text: details.ExamTitle + " " + details.ClassDisplayName,
		HTML: "<p>" + details.ExamTitle + " " + details.ClassDisplayName + "</p>"}, nil
}

type mailSenderFake struct {
	enabled bool
	from    mail.Address
}

func (s *mailSenderFake) Enabled() bool      { return s.enabled }
func (s *mailSenderFake) From() mail.Address { return s.from }
func (s *mailSenderFake) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, nil
}

func mailTestSealer(t *testing.T) *secretseal.Sealer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	value, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	return value
}
