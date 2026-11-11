// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func TestLocalUserCreationRejectsPasswordWorkBeforePersistence(t *testing.T) {
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	saturatePasswordWork(t, passwordHasherOf(t, service))
	command := CreateLocalUserCommand{User: &model.User{Username: "new-user", Email: "new-user@example.edu"}, Password: "CorrectHorseBatteryStaple1!"}
	user, err := service.CreateLocalUser(context.Background(), command)
	require.Nil(t, user, "saturated creation = %#v/%v", user, err)
	require.True(t, apperror.Is(err, "service.busy"), "saturated creation = %#v/%v", user, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	user, err = service.CreateLocalUser(ctx, command)
	require.Nil(t, user, "canceled creation")
	require.True(t, apperror.Is(err, "authentication.internal"), "canceled creation")
	require.ErrorIs(t, err, context.Canceled, "canceled creation")
	require.Empty(t, persistence.users, "rejected password work created account state")
	require.Empty(t, persistence.passwords, "rejected password work created account state")
	require.Nil(t, persistence.createdJob, "rejected password work created account state")
}
