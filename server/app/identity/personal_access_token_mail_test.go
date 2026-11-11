// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
)

func TestPersonalAccessTokenSecurityNoticeIsTerminalWhenMailIsDisabled(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	preparer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: mailRendererFake{}, Sender: &mailSenderFake{enabled: false}, Sealer: nil, Institution: nil})
	require.NoError(t, err)
	prepared, err := preparer.PreparePersonalAccessTokenSecurityNotice(mail.PersonalAccessTokenPreparation{
		Recipient:   mailTestUser(principal, at),
		TemplateKey: model.MailTemplateIdentityPersonalAccessTokenCreated,
		Description: "automation", ExpiresAt: at.Add(time.Hour), ActionAt: at,
		ActionCount: 1,
	})
	require.NoError(t, err)
	require.Equal(t, model.MailOccurrenceSecurityNotice, prepared.Occurrence.Kind, "disabled-mail preparation = %#v %#v %#v", prepared.Occurrence, prepared.Delivery, prepared.Job)
	require.Equal(t, model.MailDeliverySuppressed, prepared.Delivery.State, "disabled-mail preparation = %#v %#v %#v", prepared.Occurrence, prepared.Delivery, prepared.Job)
	require.Equal(t, model.MailDeliveryDisabledCode, prepared.Delivery.PublicFailureCode, "disabled-mail preparation = %#v %#v %#v", prepared.Occurrence, prepared.Delivery, prepared.Job)
	require.Empty(t, prepared.Delivery.EncryptedPayload, "disabled-mail preparation = %#v %#v %#v", prepared.Occurrence, prepared.Delivery, prepared.Job)
	require.Equal(t, model.JobStatusCanceled, prepared.Job.Status, "disabled-mail preparation = %#v %#v %#v", prepared.Occurrence, prepared.Delivery, prepared.Job)
	require.Equal(t, model.JobTypeMailDeliver, prepared.Job.Type, "disabled-mail preparation = %#v %#v %#v", prepared.Occurrence, prepared.Delivery, prepared.Job)
}
