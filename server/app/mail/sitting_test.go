// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSittingComposerFreezesAndSelectsRecipientLocales(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	sealer := sittingTestSealer(t)
	composer, err := NewSittingComposer(SittingComposerDependencies{Renderer: frenchSittingRenderer(t), Sender: &sittingSenderFake{enabled: true,
		from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, Sealer: sealer, Now: func() time.Time { return at }})
	require.NoError(t, err)
	sitting, err := model.NewExamSitting(model.NewExamSittingID(), model.NewExamID(), model.NewExamRevisionID(), model.NewClassID(),
		at.Add(24*time.Hour), at.Add(26*time.Hour), model.ExamSittingJoinWindow{}, at)
	require.NoError(t, err)
	prepared, err := composer.Prepare(model.NewUserID(), sitting, store.ExamSittingMailScheduled,
		SittingScheduleDetails{ExamTitle: "Algorithms", ClassDisplayName: "Class A"})
	require.NoError(t, err)
	bundle, err := composer.OpenBundle(prepared.Bundle)
	require.NoError(t, err)
	require.Len(t, bundle.Messages, 2)
	require.NotNil(t, bundle.Messages["en"])
	require.NotNil(t, bundle.Messages["fr"])
	fanout := &store.ExamSittingMailFanoutSnapshot{Occurrence: prepared.Occurrence, Deadline: at.Add(72 * time.Hour)}
	for _, test := range []struct {
		locale      string
		wantSubject string
	}{{locale: "fr-FR", wantSubject: "Examen planifié : Algorithms"}, {locale: "de", wantSubject: "Exam scheduled: Algorithms"}} {
		t.Run(test.locale, func(t *testing.T) {
			recipient := &model.User{Username: "student", Email: model.NewID() + "@example.test", DisplayName: "Student",
				EmailVerified: true, Locale: test.locale, Timezone: "UTC"}
			recipient.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
			delivery, _, prepareErr := composer.PrepareRecipient(fanout, recipient, model.MailTemplateExamSittingScheduled, bundle)
			require.NoError(t, prepareErr, "PrepareRecipient(%q)", test.locale)
			payload, openErr := OpenDelivery(sealer, delivery)
			require.NoError(t, openErr)
			require.Contains(t, payload.HTML, `src="cid:`+testLockupCID+`"`, "fan-out changed the immutable logo reference")
			require.True(t, strings.HasPrefix(payload.Subject, test.wantSubject), "PrepareRecipient(%q) subject = %q", test.locale, payload.Subject)
		})
	}
}

// frenchSittingRenderer is the production renderer over the server's
// catalogs plus a French catalog that translates only the scheduled subject,
// so locale selection and English fallback are both visible.
func frenchSittingRenderer(t *testing.T) *TemplateRenderer {
	t.Helper()
	english, err := os.ReadFile("../../i18n/en.json")
	require.NoError(t, err)
	french := `[{"id": "mail.exam.sitting_scheduled.subject", "translation": "Examen planifié : {{.ExamTitle}}, {{.When}}"}]`
	localizer, err := localization.New(fstest.MapFS{"en.json": {Data: english}, "fr.json": {Data: []byte(french)}}, localization.EnglishLocale)
	require.NoError(t, err)
	renderer, err := NewRenderer(os.DirFS("../../templates"), localizer)
	require.NoError(t, err)
	return renderer
}

type sittingSenderFake struct {
	enabled bool
	from    Address
}

func (s *sittingSenderFake) Enabled() bool { return s.enabled }
func (s *sittingSenderFake) From() Address { return s.from }
func (s *sittingSenderFake) Send(context.Context, Outbound) (TransportOutcome, error) {
	return TransportUnknown, nil
}

func sittingTestSealer(t *testing.T) *secretseal.Sealer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	return sealer
}
