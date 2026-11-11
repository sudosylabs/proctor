// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestConnectDelegatesAtomicAdmissionAndPassesOnlyCredentialHash(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	command := validConnectCommand(f, credential, "test-key")
	result, err := f.service.ConnectExamAttempt(context.Background(), f.inv, command)
	require.NoError(t, err)
	require.Equal(t, f.revision.ID, result.Attempt.AdmissionRevisionID)
	require.Equal(t, f.sitting.ClassID, result.ClassID)
	require.True(t, result.FirstAdmission)
	require.True(t, result.ConnectionOpened)
	// Contract: a first admission that revoked no Session publishes the opened
	// Connection to managers, then the operational invalidations.
	require.Equal(t, connectionOpenedCalls(t, result), f.publisher.Calls())
	input := f.persistence.connect
	require.NotNil(t, input)
	require.Equal(t, model.HashToken(credential), input.ContinuityCredentialHash)
	require.NotContains(t, input.ContinuityCredentialHash, credential)
	require.NotNil(t, input.InitialConfiguration)
	require.Equal(t, f.configuration.UserSettingsRevision, input.InitialConfiguration.UserSettingsRevision)
	require.Equal(t, f.desktopBuild, input.DesktopBuild)
	require.Equal(t, int64(1), input.DesktopCompatibilityPolicyRevision)
	require.Equal(t, model.CurrentAttemptConfigurationManifestFingerprint(), input.ConfigurationManifestFingerprint)
	require.Equal(t, f.configuration.Digest, result.Configuration.Digest)
	require.Equal(t, f.configuration.Digest, result.RuntimeCapabilities.AttemptConfiguration.Digest)
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"sitting", "audit", "connect", "effects"}, f.order)
	require.NotContains(t, strings.ToLower(fmt.Sprintf("%#v", result.Participation)), "credential")
	require.NotContains(t, fmt.Sprintf("%#v", result.Participation), credential)
	require.NotContains(t, fmt.Sprintf("%#v", result.Participation), model.HashToken(credential))
	require.Nil(t, f.audit.values["continuity_credential"])
	require.Nil(t, f.audit.values["credential_hash"])
	wantIdempotency, prepareErr := prepareConnectIdempotency(f.inv, command)
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
	auditCapture := fmt.Sprintf("%#v", f.audit.values)
	require.NotContains(t, auditCapture, credential)
	require.NotContains(t, auditCapture, model.HashToken(credential))
}

func TestConnectRejectsMalformedCredentialAndPATBeforeReads(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*fixture){
		func(f *fixture) {
			f.inv = invocation.New(model.Principal{UserID: f.userID}, model.RequestMetadata{})
		},
		func(f *fixture) {
			principal := f.inv.Principal()
			principal.CredentialType = model.CredentialPersonalAccessToken
			principal.SessionID = ""
			principal.AuthenticationStrength = ""
			principal.AuthenticatedAt = time.Time{}
			principal.ClientType = model.SessionClientCLI
			principal.CredentialScopes = []string{string(model.ActionExamSittingView)}
			f.inv = invocation.New(principal, model.RequestMetadata{})
		},
	} {
		f := newFixture(t)
		mutate(f)
		_, err := f.service.ConnectExamAttempt(context.Background(), f.inv, ConnectCommand{
			SittingID: f.sitting.ID, ContinuityCredential: "not-canonical", IdempotencyKey: "test-key",
		})
		require.Error(t, err)
		require.Empty(t, f.order)
	}
}

func TestConnectReplayAfterCorrectionAndPauseSuppressesTransientOpenEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.replayed = true
	f.persistence.connectionOpened = false
	f.sitting.State = model.ExamSittingPaused
	f.sitting.ExamRevisionID = model.NewExamRevisionID()
	result, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	require.NoError(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.Replayed, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	// Contract (guardrail 8): the audit begins before the durable write.
	require.Equal(t, []string{"sitting", "audit", "connect"}, f.order)
}

func TestReconnectThatOpensANewConnectionPublishesOneOpenEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.firstAdmission = false
	result, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	require.NoError(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.FirstAdmission, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.ConnectionOpened, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.Replayed, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Equal(t, connectionOpenedCalls(t, result), f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
}

func TestDifferentKeyConvergenceOnExistingOpenConnectionPublishesNoEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.firstAdmission = false
	f.persistence.connectionOpened = false
	result, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	require.NoError(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.FirstAdmission, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.ConnectionOpened, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.Replayed, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
}

func TestConnectReplayOfClosedConnectionRequiresNewKey(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.replayed = true
	f.persistence.connectClosed = true
	_, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v effects=%#v", err, f.publisher.Calls())
	require.Equal(t, "exam.attempt.connection_closed", fault.Code(), "error=%v effects=%#v", err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "error=%v effects=%#v", err, f.publisher.Calls())
}

func TestConnectReplayOfEndedParticipationRequiresNewKey(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.replayed = true
	f.persistence.participationEnded = true
	_, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v effects=%#v", err, f.publisher.Calls())
	require.Equal(t, "exam.attempt.connection_closed", fault.Code(), "error=%v effects=%#v", err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "error=%v effects=%#v", err, f.publisher.Calls())
}

func TestConnectRejectsAConnectionOutcomeBoundToAnotherSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.connectionSession = model.NewSessionID()
	_, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v effects=%#v", err, f.publisher.Calls())
	require.Equal(t, "exam.attempt.unavailable", fault.Code(), "error=%v effects=%#v", err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "error=%v effects=%#v", err, f.publisher.Calls())
}

func TestConnectFailureCompletesAuditAndPublishesNoEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.connectErr = store.NewErrConflict("exam_sitting", "exam_sitting_state", nil)
	_, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v audit=%q effects=%#v order=%v", err, f.audit.failedCode, f.publisher.Calls(), f.order)
	require.Equal(t, "exam.attempt.sitting_not_open", fault.Code(), "error=%v audit=%q effects=%#v order=%v", err, f.audit.failedCode, f.publisher.Calls(), f.order)
	require.Equal(t, fault.Code(), f.audit.failedCode, "error=%v audit=%q effects=%#v order=%v", err, f.audit.failedCode, f.publisher.Calls(), f.order)
	require.Empty(t, f.publisher.Calls(), "error=%v audit=%q effects=%#v order=%v", err, f.audit.failedCode, f.publisher.Calls(), f.order)
	// Contract (guardrail 8): the audit begins before the durable write and
	// records the rejected commit.
	require.Equal(t, []string{"sitting", "audit", "connect", "audit.fail"}, f.order)
}

func TestExpiredReplayReturnsCandidateSafeConnectionLoss(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.connectErr = store.NewErrConflict("attempt_participation", "attempt_participation_expired", nil)
	_, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v effects=%#v", err, f.publisher.Calls())
	require.Equal(t, "exam.attempt.connection_lost", fault.Code(), "error=%v effects=%#v", err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "error=%v effects=%#v", err, f.publisher.Calls())
}

func TestTrustedCloseCommitsBeforeManagerEffectAndSuppressesNoop(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	connected, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, credential, "test-key"))
	require.NoError(t, err)
	f.order = nil
	f.persistence.closeChanged = true
	closed, err := f.service.CloseExamAttemptConnection(context.Background(), f.inv, CloseConnectionCommand{
		AttemptID: connected.Attempt.ID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		ConnectionID: connected.Connection.ID, Reason: model.AttemptConnectionCloseTransport,
	})
	require.NoError(t, err, "closed=%#v error=%v order=%v", closed, err, f.order)
	require.True(t, closed.Changed, "closed=%#v error=%v order=%v", closed, err, f.order)
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"audit", "close", "effects"}, f.order)
	f.order = nil
	f.persistence.closeChanged = false
	_, err = f.service.CloseExamAttemptConnection(context.Background(), f.inv, CloseConnectionCommand{
		AttemptID: connected.Attempt.ID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		ConnectionID: connected.Connection.ID, Reason: model.AttemptConnectionCloseTransport,
	})
	require.NoError(t, err)
	// Contract: the close that changed the Connection publishes it and the
	// Sitting board invalidation after the open; the unchanged close adds nothing.
	require.Equal(t, append(connectionOpenedCalls(t, connected), connectionClosedCalls(t, closed)...), f.publisher.Calls(), "close effects")
}

func TestTrustedCloseStillFencesCandidateAndSessionOwnership(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*model.Principal){
		func(principal *model.Principal) { principal.UserID = model.NewUserID() },
		func(principal *model.Principal) { principal.SessionID = model.NewSessionID() },
	} {
		f := newFixture(t)
		connected, err := f.service.ConnectExamAttempt(context.Background(), f.inv, validConnectCommand(f, model.NewCredentialToken(), "test-key"))
		require.NoError(t, err)
		principal := f.inv.Principal()
		mutate(&principal)
		_, err = f.service.CloseExamAttemptConnection(context.Background(), invocation.New(principal, f.inv.RequestMetadata()), CloseConnectionCommand{
			AttemptID: connected.Attempt.ID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
			ConnectionID: connected.Connection.ID, Reason: model.AttemptConnectionCloseTransport,
		})
		require.True(t, apperror.Is(err, "resource.not_found"), "error=%v effects=%#v", err, f.publisher.Calls())
		require.True(t, chainHasCode(err, "exam.attempt.not_found"), "error=%v effects=%#v", err, f.publisher.Calls())
		// Contract: the refused close publishes nothing after the open.
		require.Equal(t, connectionOpenedCalls(t, connected), f.publisher.Calls())
	}
}

func TestTrustedCloseRejectsUnknownReasonBeforeAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.CloseExamAttemptConnection(context.Background(), f.inv, CloseConnectionCommand{
		AttemptID: model.NewExamAttemptID(), SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		ConnectionID: model.NewAttemptConnectionID(), Reason: "unknown",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.attempt.invalid", fault.Code())
	require.Empty(t, f.order)
}
