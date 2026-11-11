// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func TestLocalAuthenticationRejectsRehashCapacityBeforeProofAndMutation(t *testing.T) {
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	const password = "CorrectHorseBatteryStaple1!"
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "rehash-capacity", Email: "rehash-capacity@example.edu"}, Password: password,
	})
	require.NoError(t, err)
	credential := *persistence.passwords[user.ID.String()]
	passwordHasherOf(t, service).parameters.iterations++
	recorder := &passwordWorkRecorderFake{}
	passwordHasherOf(t, service).deps.Recorder = recorder
	mfa := &mfaApplicationStoreFake{}
	service.mfaCredentials = mfa
	// Admit competing work at the next context check after verification has
	// returned its permit. This selects the rehash admission boundary without
	// sleeps, cryptographic substitutes, or a production-only test hook.
	var competed sync.Once
	ctx := passwordWorkBoundaryContext{Context: context.Background(), check: func() {
		if recorder.finished.Load() == 1 && len(passwordHasherOf(t, service).work) == 0 {
			competed.Do(func() { saturatePasswordWork(t, passwordHasherOf(t, service)) })
		}
	}}
	proof, err := service.authenticateLocal(ctx, LoginCommand{
		LoginID: user.Email, Password: password, ClientType: model.SessionClientWeb, Source: "192.0.2.8",
	})
	require.Nil(t, proof, "saturated rehash = %#v/%v, rejected work", proof, err)
	require.True(t, apperror.Is(err, "service.busy"), "saturated rehash = %#v/%v, rejected work", proof, err)
	require.Equal(t, int64(1), recorder.rejected.Load(), "saturated rehash = %#v/%v, rejected work", proof, err)
	require.Equal(t, 0, mfa.credentialGets, "saturated rehash = %#v/%v, rejected work", proof, err)
	require.Equal(t, credential, *persistence.passwords[user.ID.String()], "rejected rehash changed credentials or created a Session")
	require.Empty(t, persistence.sessions, "rejected rehash changed credentials or created a Session")
}

type passwordWorkBoundaryContext struct {
	context.Context
	check func()
}

func (ctx passwordWorkBoundaryContext) Err() error {
	ctx.check()
	return ctx.Context.Err()
}

func TestLocalAuthenticationRejectsRehashConflictBeforeReturningProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	const password = "CorrectHorseBatteryStaple1!"
	user, err := service.CreateLocalUser(ctx, CreateLocalUserCommand{
		User: &model.User{Username: "removed-password", Email: "removed-password@example.edu"}, Password: password,
	})
	require.NoError(t, err)
	passwordHasherOf(t, service).parameters.iterations++
	service.mfaCredentials = &mfaApplicationStoreFake{
		afterCredentialGet: func() { delete(persistence.passwords, user.ID.String()) },
	}
	proof, err := service.authenticateLocal(ctx, LoginCommand{
		LoginID: user.Email, Password: password, ClientType: model.SessionClientWeb, Source: "192.0.2.11",
	})
	require.Nil(t, proof, "authenticate removed password = %#v, %v", proof, err)
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "authenticate removed password = %#v, %v", proof, err)
	require.Nil(t, persistence.passwords[user.ID.String()], "rehash resurrected removed password credential")
}

func TestLocalAuthenticationRehashPreservesPasswordProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	const password = "CorrectHorseBatteryStaple1!"
	user, err := service.CreateLocalUser(ctx, CreateLocalUserCommand{
		User: &model.User{Username: "rehash-proof", Email: "rehash-proof@example.edu"}, Password: password,
	})
	require.NoError(t, err)
	before := *persistence.passwords[user.ID.String()]
	passwordHasherOf(t, service).parameters.iterations++
	recorder := &passwordWorkRecorderFake{}
	passwordHasherOf(t, service).deps.Recorder = recorder
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base.Add(time.Duration(recorder.finished.Load()) * time.Second) }
	service.mfaCredentials = &mfaApplicationStoreFake{afterCredentialGet: func() {
		require.Equal(t, int64(2), recorder.finished.Load(), "MFA ran before password work completed and released its permits")
		require.Empty(t, passwordHasherOf(t, service).work, "MFA ran before password work completed and released its permits")
	}}
	proof, err := service.authenticateLocal(ctx, LoginCommand{
		LoginID: user.Email, Password: password, ClientType: model.SessionClientWeb, Source: "192.0.2.12",
	})
	require.NoError(t, err)
	require.Equal(t, base.Add(2*time.Second).UnixMilli(), proof.AuthenticatedAt, "authentication time")
	after := persistence.passwords[user.ID.String()]
	require.Equal(t, before.ID, proof.PasswordProof.ID, "rehash changed the proved password identity or revision")
	require.Equal(t, before.Revision, proof.PasswordProof.Revision, "rehash changed the proved password identity or revision")
	require.Equal(t, before.Revision, after.Revision, "rehash changed the proved password identity or revision")
	require.Equal(t, before.PasswordChangedAt, after.PasswordChangedAt, "rehash changed the proved password identity or revision")
	require.NotEqual(t, before.PasswordHash, after.PasswordHash, "rehash did not preserve password verification under the new parameters")
	require.False(t, passwordHasherOf(t, service).NeedsRehash(after.PasswordHash), "rehash did not preserve password verification under the new parameters")
	require.NoError(t, passwordHasherOf(t, service).Verify(context.Background(), after.PasswordHash, password), "rehash did not preserve password verification under the new parameters")
}
