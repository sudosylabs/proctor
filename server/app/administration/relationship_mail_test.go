// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// relationshipUserStoreTestFake holds an active User, at revision 1, for
// every identifier, so a Role Binding notice can be prepared for anyone.
type relationshipUserStoreTestFake struct{ store.UserStore }

func (relationshipUserStoreTestFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	parsed, err := model.ParseUserID(id.String())
	if err != nil {
		return nil, err
	}
	user := &model.User{Email: "member@example.test", DisplayName: "Member", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(parsed, time.UnixMilli(1))
	return user, nil
}

func (relationshipUserStoreTestFake) ListIdentities(_ context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	identities := make([]store.UserIdentity, 0, len(ids))
	for _, id := range ids {
		identities = append(identities, store.UserIdentity{ID: id, Username: "user-" + id.String(), DisplayName: "User " + id.String(), Email: id.String() + "@example.edu"})
	}
	return identities, nil
}

// newMail builds the production mail Composer over the real renderer and an
// enabled sender, so every eligible recipient's message is rendered, and
// returns the record of what it rendered.
func newMail(t *testing.T) (*mail.Composer, *mailtest.Renders) {
	t.Helper()
	renderer, renders := mailtest.NewRecordingRenderer(t, nil)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	composer, err := mail.NewComposer(mail.ComposerDependencies{
		Renderer: renderer,
		Sender:   mailSenderFake{},
		Sealer:   sealer,
	})
	require.NoError(t, err)
	return composer, renders
}

// mailSenderFake is an enabled mail transport port.
type mailSenderFake struct{}

func (mailSenderFake) Enabled() bool { return true }
func (mailSenderFake) From() mail.Address {
	return mail.Address{Name: "Proctor", Address: "no-reply@example.test"}
}

func (mailSenderFake) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, nil
}
