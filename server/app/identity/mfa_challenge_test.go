// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestMFAChallengeRejectsReplayedRecoveryCodeAndPreservesEffectOrdering(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 10, 15, 0, 123456789, time.FixedZone("test", 2*60*60))
	principal := mfaTestPrincipal(now, model.AuthenticationSingleFactor)
	events := []string{}
	persistence := &mfaApplicationStoreFake{
		events: &events,
		credential: &model.MFACredential{
			ID: model.NewMFACredentialID(), UserID: principal.UserID,
			State: model.MFAStateActive,
		},
		session: &model.Session{
			ID: principal.SessionID, UserID: principal.UserID,
			AuthenticationStrength: model.AuthenticationMultiFactor,
			MFACompletedAt:         model.OptionalTimeFrom(now),
		},
		hashes: []string{"access-hash"},
	}
	audit := &securityAuditorFake{trace: &events}
	effects := revocationsIn(&events, "effects")
	service := newTestMFAApplicationService(t, persistence, audit, effects, now)
	application := service
	invocation := invocation.New(principal, model.RequestMetadata{RequestID: "request-a"})
	command := ChallengeMFACommand{Code: "aaaa-aaaa-aaaa-aaaa-aaaa-aaaa"}

	_, err := application.ChallengeMFA(context.Background(), invocation, command)
	require.NoError(t, err)
	require.Equal(t, model.TimeUTC(now), persistence.upgradedAt, "Session upgrade instant")
	_, err = application.ChallengeMFA(context.Background(), invocation, command)
	require.True(t, apperror.Is(err, "authentication.mfa.code_invalid"), "replayed challenge error = %v, want authentication.mfa.code_invalid", err)

	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// the audit outcome follows the write, and the write commits before its
	// effect.
	require.Equal(t, []string{"institution", "audit_begin", "credential_get", "consume", "upgrade", "audit_success", "effects", "institution", "audit_begin", "credential_get", "consume", "audit_fail"}, events)
	// Contract: one revocation, of the upgraded Session and its access token.
	require.Equal(t, []realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: principal.UserID,
		SessionIDs: []model.SessionID{principal.SessionID}, AccessTokenHashes: []string{"access-hash"},
	}}, effects.Calls())
	// Self-service MFA is audited against the Institution with the acting
	// User as its subject.
	require.Len(t, audit.events, 2)
	for _, record := range audit.events {
		require.Equal(t, principal.UserID, record.actor)
		require.Equal(t, principal.SessionID, record.sessionID)
		require.Equal(t, principal.UserID, record.target, "self-service MFA subject")
		require.Equal(t, model.ActionMFAChallenge, record.action)
		require.Equal(t, model.ResourceInstitution, record.resource.Type)
		require.Equal(t, "request-a", record.metadata.RequestID)
	}
	require.Len(t, audit.terminal, 1, "the replay's failure is the only outcome the Service records")
	require.Equal(t, "authentication.mfa.code_invalid", audit.terminal[0].errorCode)
}

func TestMFARecoveryCodeLoginConsumptionIsSerialized(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 10, 30, 0, 0, time.UTC)
	userID := model.NewUserID()
	persistence := &mfaApplicationStoreFake{
		events: &[]string{},
		credential: &model.MFACredential{
			ID: model.NewMFACredentialID(), UserID: userID, State: model.MFAStateActive,
		},
	}
	service := newTestMFAApplicationService(
		t, persistence, &securityAuditorFake{}, &realtimetest.Publisher{}, now,
	)
	const contenders = 8
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, err := service.verifyLoginMFA(
				context.Background(), userID.String(),
				"aaaa-aaaa-aaaa-aaaa-aaaa-aaaa", now,
			)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case apperror.Is(err, "authentication.mfa.code_invalid"):
			rejected++
		default:
			require.FailNow(t, fmt.Sprintf("VerifyLogin error = %v", err))
		}
	}
	require.Equal(t, 1, successes, "successes")
	require.Equal(t, contenders-1, rejected, "successes")
}
