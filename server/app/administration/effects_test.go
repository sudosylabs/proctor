// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAccessPolicyChangeRevokesSessionsBeforePublishingTheRevision(t *testing.T) {
	t.Parallel()
	institutionID := model.NewInstitutionID()
	revoked, shortened := model.NewUserID(), model.NewUserID()
	revokedSessions := []model.SessionID{model.NewSessionID(), model.NewSessionID()}
	replacement := &store.AccessPolicyReplacementResult{
		Snapshot: &store.AccessPolicySnapshot{Policy: &model.AccessPolicy{Revision: 7}},
		SessionRevocations: []store.AccessPolicySessionRevocation{
			{UserID: revoked, SessionIDs: revokedSessions, AccessTokenHashes: []string{"revoked-hash"}},
		},
		SessionDeadlineChanges: []store.AccessPolicySessionDeadlineChange{
			{UserID: shortened, SessionIDs: []model.SessionID{model.NewSessionID()}, AccessTokenHashes: []string{"shortened-hash"}},
		},
	}
	policyChanged := realtimetest.Call{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
		Name:     "access_policy_changed",
		Action:   model.ActionAccessPolicyView,
		Resource: model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		Data:     json.RawMessage(`{"revision":7}`),
	}}
	sessionEffects := []realtimetest.Call{
		{Method: realtimetest.SessionsRevoked, UserID: revoked, SessionIDs: revokedSessions, AccessTokenHashes: []string{"revoked-hash"}},
		{Method: realtimetest.AuthenticationCacheInvalidated, UserID: shortened, AccessTokenHashes: []string{"shortened-hash"}},
	}

	t.Run("published", func(t *testing.T) {
		t.Parallel()
		publisher := &realtimetest.Publisher{}
		service := &Service{publisher: publisher}
		require.NoError(t, service.publishAccessPolicyChanged(context.Background(), institutionID, replacement))
		// Contract: revoked Sessions close first, shortened Sessions lose only
		// their cached authentication, and the refetch signal comes last.
		require.Equal(t, append(sessionEffects[:2:2], policyChanged), publisher.Calls())
	})

	t.Run("publication fails", func(t *testing.T) {
		t.Parallel()
		deliveryErr := errors.New("peer delivery unavailable")
		publisher := &realtimetest.Publisher{Fail: func(realtimetest.Call) error { return deliveryErr }}
		service := &Service{publisher: publisher}
		require.ErrorIs(t, service.publishAccessPolicyChanged(context.Background(), institutionID, replacement), deliveryErr)
		// Contract: the Session effects are applied even when the signal fails.
		require.Equal(t, append(sessionEffects[:2:2], policyChanged), publisher.Calls())
	})

	t.Run("invalid institution", func(t *testing.T) {
		t.Parallel()
		publisher := &realtimetest.Publisher{}
		service := &Service{publisher: publisher}
		require.Error(t, service.publishAccessPolicyChanged(context.Background(), "", replacement))
		// Contract: the Session effects never wait on the signal being buildable.
		require.Equal(t, sessionEffects, publisher.Calls())
	})
}
