// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// publisherIn returns a recording Publisher that also places its calls in the
// log a test shares with its other fakes. Consecutive calls collapse into one
// "effects" entry: the shared log proves where the effects of a use case fall
// relative to its Store writes, and the Publisher's own call log proves what
// they are.
func publisherIn(order *[]string) *realtimetest.Publisher {
	return &realtimetest.Publisher{Observe: func(realtimetest.Call) {
		if last := len(*order) - 1; last >= 0 && (*order)[last] == "effects" {
			return
		}
		*order = append(*order, "effects")
	}}
}

// published returns a function that turns a constructed event into the call
// that publishes it, failing the test when the event could not be built.
func published(t *testing.T) func(realtime.Event, error) realtimetest.Call {
	t.Helper()
	return func(event realtime.Event, err error) realtimetest.Call {
		t.Helper()
		require.NoError(t, err)
		return realtimetest.Call{Method: realtimetest.Publish, Event: event}
	}
}

// sittingBoardChangedCall is the Sitting board invalidation.
func sittingBoardChangedCall(t *testing.T, examID model.ExamID, sittingID model.ExamSittingID) realtimetest.Call {
	t.Helper()
	return published(t)(realtime.NewManagerSittingBoardChangedEvent(examID, sittingID))
}

// operationalInvalidationCalls follow every change to what an Attempt may do:
// the candidate's Exam activity, the Sitting board, then the candidate's
// current-user context.
func operationalInvalidationCalls(
	t *testing.T,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	candidateID model.UserID,
) []realtimetest.Call {
	t.Helper()
	return []realtimetest.Call{
		published(t)(realtime.NewCandidateExamActivityChangedEvent(candidateID)),
		sittingBoardChangedCall(t, examID, sittingID),
		{Method: realtimetest.InvalidateCurrentUserContext, UserID: candidateID},
	}
}

// connectionOpenedCalls are the calls of an opened Attempt Connection that
// revoked no Session: the opening, then the operational invalidations.
func connectionOpenedCalls(t *testing.T, result ConnectionResult) []realtimetest.Call {
	t.Helper()
	opened := published(t)(realtime.NewExamAttemptConnectionOpenedEvent(result.Attempt.SittingID, result.Attempt.ID,
		result.Attempt.CandidateUserID, result.Connection.ID, result.Connection.OpenedAt))
	return append([]realtimetest.Call{opened},
		operationalInvalidationCalls(t, result.Attempt.ExamID, result.Attempt.SittingID, result.Attempt.CandidateUserID)...)
}

// expiryCalls are the calls of an expired Participation. When the expiry
// closed the Attempt Connection the close leads and the unbinding ends them.
func expiryCalls(t *testing.T, result ParticipationExpiry, closedConnection bool) []realtimetest.Call {
	t.Helper()
	pub := published(t)
	var calls []realtimetest.Call
	if closedConnection {
		calls = append(calls, pub(realtime.NewExamAttemptConnectionClosedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID,
			result.Connection.ID, result.Connection.CloseReason, result.Connection.ClosedAt.Time)))
	}
	calls = append(calls,
		pub(realtime.NewExamAttemptPolicySuspendedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID, result.Connection.ID,
			result.Flag.ID, result.Suspension.ID, result.Attempt.Revision, result.DatabaseTime)),
		pub(realtime.NewCandidateExamAttemptSuspendedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID,
			result.Suspension.Source, result.Suspension.CandidateReason, result.DatabaseTime)),
	)
	calls = append(calls, operationalInvalidationCalls(t, result.ExamID, result.SittingID, result.CandidateUserID)...)
	if closedConnection {
		calls = append(calls, realtimetest.Call{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: result.Connection.ID})
	}
	return calls
}

// reallowedCalls are the calls of a re-allowed Attempt: managers, then the
// candidate, then the operational invalidations.
func reallowedCalls(t *testing.T, result ReallowResult) []realtimetest.Call {
	t.Helper()
	pub := published(t)
	return append([]realtimetest.Call{
		pub(realtime.NewExamAttemptReallowedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID, result.Suspension.ID,
			result.Attempt.Revision, result.Attempt.UpdatedAt)),
		pub(realtime.NewCandidateExamAttemptReallowedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID, result.Attempt.UpdatedAt)),
	}, operationalInvalidationCalls(t, result.ExamID, result.SittingID, result.CandidateUserID)...)
}

// manualSuspensionCalls are the calls of a manager's kick. When the kick
// closed a live Attempt Connection the close leads and the unbinding ends
// them.
func manualSuspensionCalls(t *testing.T, result ManualSuspensionResult, closedConnection bool) []realtimetest.Call {
	t.Helper()
	pub := published(t)
	var connectionID model.AttemptConnectionID
	if result.Connection != nil {
		connectionID = result.Connection.ID
	}
	var calls []realtimetest.Call
	if closedConnection {
		calls = append(calls, pub(realtime.NewExamAttemptConnectionClosedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID,
			connectionID, result.Connection.CloseReason, result.Connection.ClosedAt.Time)))
	}
	calls = append(calls,
		pub(realtime.NewExamAttemptKickedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID, connectionID,
			result.Suspension.ID, result.Attempt.Revision, result.Suspension.StartedAt)),
		pub(realtime.NewCandidateExamAttemptSuspendedEvent(result.SittingID, result.Attempt.ID, result.CandidateUserID,
			result.Suspension.Source, result.Suspension.CandidateReason, result.Suspension.StartedAt)),
	)
	calls = append(calls, operationalInvalidationCalls(t, result.ExamID, result.SittingID, result.CandidateUserID)...)
	if closedConnection {
		calls = append(calls, realtimetest.Call{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: connectionID})
	}
	return calls
}

// submittedCalls are the calls of a sealed Submission: the manager fact, the
// candidate fact, the closed Attempt Connection when sealing for a closed
// Sitting closed one, the operational invalidations, then the unbinding of
// the submitting Connection.
func submittedCalls(t *testing.T, result SubmissionResult, sittingClosedConnection bool) []realtimetest.Call {
	t.Helper()
	pub := published(t)
	calls := []realtimetest.Call{
		pub(realtime.NewExamAttemptSubmittedEvent(result.SittingID, result.Receipt.AttemptID, result.CandidateUserID, result.Receipt.SubmissionID,
			result.Receipt.WorkspaceCursor, result.Receipt.ManifestDigest, result.Provenance, result.Receipt.SubmittedAt)),
		pub(realtime.NewCandidateExamAttemptSubmittedEvent(result.SittingID, result.Receipt.AttemptID, result.CandidateUserID, result.Receipt.SubmissionID,
			result.Receipt.WorkspaceCursor, result.Receipt.ManifestDigest, result.Provenance, result.Receipt.SubmittedAt)),
	}
	if sittingClosedConnection {
		calls = append(calls, pub(realtime.NewExamAttemptConnectionClosedEvent(result.SittingID, result.Receipt.AttemptID, result.CandidateUserID,
			result.ConnectionID, model.AttemptConnectionCloseSittingClosed, result.Receipt.SubmittedAt)))
	}
	calls = append(calls, operationalInvalidationCalls(t, result.ExamID, result.SittingID, result.CandidateUserID)...)
	return append(calls, realtimetest.Call{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: result.ConnectionID})
}

// connectionClosedCalls are the calls of a closed Attempt Connection: the
// close to managers, then the Sitting board invalidation.
func connectionClosedCalls(t *testing.T, result ConnectionClosedResult) []realtimetest.Call {
	t.Helper()
	return []realtimetest.Call{
		published(t)(realtime.NewExamAttemptConnectionClosedEvent(result.SittingID, result.AttemptID, result.CandidateUserID,
			result.Connection.ID, result.Connection.CloseReason, result.Connection.ClosedAt.Time)),
		sittingBoardChangedCall(t, result.ExamID, result.SittingID),
	}
}

// workspaceChangedCalls is the one call of an Attempt Workspace mutation.
func workspaceChangedCalls(t *testing.T, result WorkspaceMutationResult) []realtimetest.Call {
	t.Helper()
	return []realtimetest.Call{
		published(t)(realtime.NewCandidateExamAttemptWorkspaceChangedEvent(result.SittingID, result.AttemptID, result.CandidateUserID,
			result.Change.EntryID, result.Change.Operation, result.Change.Cursor, result.Change.ChangedAt)),
	}
}

// expiryOf is the Participation expiry a use case derives from the Store's
// committed outcome.
func expiryOf(stored *store.ExamAttemptParticipationExpiryResult) ParticipationExpiry {
	return ParticipationExpiry{
		ExamID: stored.ExamID, SittingID: stored.SittingID, CandidateUserID: stored.CandidateUserID,
		Attempt: *stored.Attempt, Connection: *stored.Connection, ConnectionClosed: stored.ConnectionClosed,
		Flag: *stored.Flag, Suspension: *stored.Suspension, DatabaseTime: stored.DatabaseTime,
	}
}

// sealedSubmission is the Submission a use case derives from the Store's
// committed seal, with the provenance of the use case that sealed it.
func sealedSubmission(stored store.ExamSubmissionSealResult, provenance model.ExamSubmissionProvenance) SubmissionResult {
	return SubmissionResult{
		Receipt: stored.Receipt, Provenance: provenance, ExamID: stored.ExamID, SittingID: stored.SittingID,
		CandidateUserID: stored.CandidateUserID, ConnectionID: stored.ConnectionID,
	}
}

// requireEventDataOmits fails when any published event's payload contains a
// forbidden word.
func requireEventDataOmits(t *testing.T, calls []realtimetest.Call, forbidden ...string) {
	t.Helper()
	for _, call := range calls {
		encoded := strings.ToLower(string(call.Event.Data))
		for _, word := range forbidden {
			require.NotContains(t, encoded, word, "%s event contains %q: %s", call.Event.Name, word, call.Event.Data)
		}
	}
}

func TestConnectionOpenedEffectRevokesSupersededSessionsBeforeAnnouncingTheConnection(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	at := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	result := ConnectionResult{
		Attempt: model.ExamAttempt{ID: model.NewExamAttemptID(), ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(),
			CandidateUserID: model.NewUserID()},
		Connection:                model.AttemptConnection{ID: model.NewAttemptConnectionID(), OpenedAt: at},
		RevokedSessionIDs:         []model.SessionID{model.NewSessionID()},
		RevokedAccessTokenDigests: []string{"digest"},
	}
	require.NoError(t, service.publishConnectionOpened(context.Background(), result))
	// Contract: the Sessions admission revoked are closed before anything
	// announces the new Connection.
	require.Equal(t, append([]realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: result.Attempt.CandidateUserID,
		SessionIDs: result.RevokedSessionIDs, AccessTokenHashes: result.RevokedAccessTokenDigests,
	}}, connectionOpenedCalls(t, result)...), publisher.Calls())
}

func TestParticipationExpiryEffectPublishesCloseManagerSuspensionAndCandidateSuspension(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	at := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	result := ParticipationExpiry{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), CandidateUserID: model.NewUserID(), DatabaseTime: at,
		Attempt: model.ExamAttempt{ID: model.NewExamAttemptID(), Revision: 2},
		Connection: store.ExamAttemptManagerConnection{ID: model.NewAttemptConnectionID(), State: model.AttemptConnectionClosed,
			OpenedAt: at.Add(-time.Minute), ClosedAt: model.OptionalTimeFrom(at), CloseReason: model.AttemptConnectionCloseLeaseExpired},
		ConnectionClosed: true,
		Flag:             model.IntegrityFlag{ID: model.NewIntegrityFlagID()},
		Suspension: store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), Source: model.AttemptSuspensionSourcePolicy,
			CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost}}
	require.NoError(t, service.publishParticipationExpired(context.Background(), result))
	calls := publisher.Calls()
	// Contract: the Connection close reaches managers before the Suspension,
	// the candidate learns of the Suspension next, the operational
	// invalidations follow, and the Connection is unbound last.
	require.Equal(t, expiryCalls(t, result, true), calls)
	require.Len(t, calls, 7)
	require.Equal(t, "exam_attempt_connection_closed", calls[0].Event.Name)
	require.Equal(t, model.ActionExamSittingView, calls[0].Event.Action)
	require.Equal(t, "exam_attempt_suspended", calls[1].Event.Name)
	require.Equal(t, model.ActionExamSittingView, calls[1].Event.Action)
	require.Equal(t, "exam_attempt_access_suspended", calls[2].Event.Name)
	require.Equal(t, model.ActionExamSittingParticipate, calls[2].Event.Action)
	require.Equal(t, result.CandidateUserID, calls[2].Event.UserID)
	require.Equal(t, "candidate.exam_activity.changed", calls[3].Event.Name)
	require.Equal(t, "manager.sitting_board.changed", calls[4].Event.Name)
	require.Equal(t, realtimetest.InvalidateCurrentUserContext, calls[5].Method)
	require.Equal(t, realtimetest.UnbindExamAttemptConnection, calls[6].Method)
	requireEventDataOmits(t, calls, "continuity_credential", "credential_hash", "private_reason")
	require.Contains(t, string(calls[2].Event.Data), `"reason_code":"secure_connectivity_lost"`)
}

func TestManualSuspensionEffectPublishesKickedCloseAndSafeCandidateReason(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	at := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	result := ManualSuspensionResult{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(),
		CandidateUserID: model.NewUserID(), Attempt: model.ExamAttempt{ID: model.NewExamAttemptID(), Revision: 3},
		Connection: &store.ExamAttemptManagerConnection{ID: model.NewAttemptConnectionID(), State: model.AttemptConnectionClosed,
			OpenedAt: at.Add(-time.Minute), ClosedAt: model.OptionalTimeFrom(at), CloseReason: model.AttemptConnectionCloseKicked},
		ConnectionClosed: true,
		Suspension: store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), Source: model.AttemptSuspensionSourceManual,
			CandidateReason: model.AttemptSuspensionCandidateReasonManagerSuspended, StartedAt: at, SuspendedByUserID: model.NewUserID()}}
	require.NoError(t, service.publishAttemptSuspendedByManager(context.Background(), result))
	calls := publisher.Calls()
	// Contract: the Connection close reaches managers before the kick, the
	// candidate learns of the Suspension next, the operational invalidations
	// follow, and the Connection is unbound last.
	require.Equal(t, manualSuspensionCalls(t, result, true), calls)
	require.Len(t, calls, 7)
	require.Equal(t, "exam_attempt_connection_closed", calls[0].Event.Name)
	require.Equal(t, "exam_attempt_suspended", calls[1].Event.Name)
	require.Equal(t, "exam_attempt_access_suspended", calls[2].Event.Name)
	require.Equal(t, result.CandidateUserID, calls[2].Event.UserID)
	require.NotContains(t, string(calls[1].Event.Data), "integrity_flag_id")
	require.Contains(t, string(calls[0].Event.Data), `"kicked"`)
	require.Contains(t, string(calls[2].Event.Data), `"reason_code":"manager_suspended"`)
	require.Equal(t, realtimetest.Call{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: result.Connection.ID}, calls[6])
}

func TestSubmissionEffectPublishesSafeManagerAndCandidateFactsThenUnbindsExactConnection(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	result := SubmissionResult{Receipt: store.ExamSubmissionReceipt{BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
		SubmissionID: model.NewSubmissionID(), AttemptID: model.NewExamAttemptID(), State: model.ExamAttemptSubmitted,
		WorkspaceCursor: 13, ManifestDigest: strings.Repeat("d", 64),
		SubmittedAt: time.Date(2026, time.August, 21, 11, 0, 0, 0, time.UTC),
	}, Provenance: model.ExamSubmissionCandidateSubmitted, ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), CandidateUserID: model.NewUserID(),
		ConnectionID: model.NewAttemptConnectionID()}

	require.NoError(t, service.publishAttemptSubmitted(context.Background(), result))
	calls := publisher.Calls()
	// Contract: managers learn of the Submission before the candidate, the
	// operational invalidations follow, and the submitting Connection is
	// unbound last.
	require.Equal(t, submittedCalls(t, result, false), calls)
	require.Len(t, calls, 6)
	require.Equal(t, "exam_attempt_submitted", calls[0].Event.Name)
	require.Empty(t, calls[0].Event.UserID)
	require.Equal(t, "exam_attempt_submitted", calls[1].Event.Name)
	require.Equal(t, result.CandidateUserID, calls[1].Event.UserID)
	require.Equal(t, "candidate.exam_activity.changed", calls[2].Event.Name)
	require.Equal(t, "manager.sitting_board.changed", calls[3].Event.Name)
	require.Equal(t, realtimetest.Call{Method: realtimetest.InvalidateCurrentUserContext, UserID: result.CandidateUserID}, calls[4])
	require.Equal(t, realtimetest.Call{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: result.ConnectionID}, calls[5])
	requireEventDataOmits(t, calls, "path", "content", "source", "evidence", "sequence", "credential", "session", "private")
}

func TestAutomaticSubmissionEffectPublishesSittingClosedFactsAndUnbindsExactConnection(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	result := AutomaticSubmissionResult{SubmissionResult: SubmissionResult{
		Receipt: store.ExamSubmissionReceipt{BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1}, SubmissionID: model.NewSubmissionID(), AttemptID: model.NewExamAttemptID(),
			State: model.ExamAttemptSubmitted, WorkspaceCursor: 21, ManifestDigest: strings.Repeat("e", 64),
			SubmittedAt: time.Date(2026, time.August, 22, 11, 0, 0, 0, time.UTC)},
		Provenance: model.ExamSubmissionSittingClosed, ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(),
		CandidateUserID: model.NewUserID(), ConnectionID: model.NewAttemptConnectionID()},
		ConnectionClosed: true}
	require.NoError(t, service.publishAttemptSealedForSittingClose(context.Background(), result))
	calls := publisher.Calls()
	// Contract: the Submission facts precede the Connection close, the
	// operational invalidations follow, and the Connection is unbound last.
	require.Equal(t, submittedCalls(t, result.SubmissionResult, true), calls)
	require.Len(t, calls, 7)
	require.Equal(t, "exam_attempt_submitted", calls[0].Event.Name)
	require.Empty(t, calls[0].Event.UserID)
	require.Equal(t, "exam_attempt_submitted", calls[1].Event.Name)
	require.Equal(t, result.CandidateUserID, calls[1].Event.UserID)
	require.Equal(t, "exam_attempt_connection_closed", calls[2].Event.Name)
	require.Equal(t, "candidate.exam_activity.changed", calls[3].Event.Name)
	require.Equal(t, "manager.sitting_board.changed", calls[4].Event.Name)
	require.Equal(t, realtimetest.Call{Method: realtimetest.InvalidateCurrentUserContext, UserID: result.CandidateUserID}, calls[5])
	require.Equal(t, realtimetest.Call{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: result.ConnectionID}, calls[6])
	require.Contains(t, string(calls[2].Event.Data), `"close_reason":"sitting_closed"`)
	requireEventDataOmits(t, calls, "path", "content", "source", "evidence", "sequence", "credential", "session", "private")
}

func TestFocusLossWarningEffectPublishesBoundedManagerFlagAndNeutralCandidateWarning(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	at := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	result := FocusLossEvaluation{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), CandidateUserID: model.NewUserID(),
		AttemptID: model.NewExamAttemptID(), ReceivedAt: at, PolicyOutcome: model.IntegrityOutcomeFlagAndWarn,
		Flag: model.IntegrityFlag{ID: model.NewIntegrityFlagID()}, RetainedEvidenceCount: 3,
		FlagCreated: true, ManagerNotificationRequired: true, CandidateWarningCreated: true}
	require.NoError(t, service.publishFocusLossEvaluated(context.Background(), result))
	calls := publisher.Calls()
	pub := published(t)
	// Contract: managers learn of the Integrity Flag before the candidate is
	// warned, and the Sitting board is invalidated after both.
	require.Equal(t, []realtimetest.Call{
		pub(realtime.NewExamAttemptIntegrityFlaggedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.Flag.ID,
			result.PolicyOutcome, result.RetainedEvidenceCount, result.EvidenceOverflowCount, at)),
		pub(realtime.NewCandidateExamAttemptFocusLossWarningEvent(result.SittingID, result.AttemptID, result.CandidateUserID, at)),
		sittingBoardChangedCall(t, result.ExamID, result.SittingID),
	}, calls)
	require.Equal(t, "exam_attempt_integrity_flagged", calls[0].Event.Name)
	require.Equal(t, "exam_attempt_focus_warning", calls[1].Event.Name)
	require.Equal(t, result.CandidateUserID, calls[1].Event.UserID)
	require.Equal(t, "manager.sitting_board.changed", calls[2].Event.Name)
	requireEventDataOmits(t, calls, "duration", "source", "sequence", "credential", "session", "threshold")
}

func TestFocusLossSuspensionEffectPublishesCloseFlagAndSeparatedSuspensions(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	at := time.Date(2026, time.August, 20, 10, 5, 0, 0, time.UTC)
	flagID := model.NewIntegrityFlagID()
	result := FocusLossEvaluation{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), CandidateUserID: model.NewUserID(),
		AttemptID: model.NewExamAttemptID(), ReceivedAt: at, PolicyOutcome: model.IntegrityOutcomeFlagAndSuspend,
		Flag: model.IntegrityFlag{ID: flagID}, RetainedEvidenceCount: 3, FlagCreated: true,
		ManagerNotificationRequired: true, ConnectionClosed: true,
		Connection: store.ExamAttemptManagerConnection{ID: model.NewAttemptConnectionID(), State: model.AttemptConnectionClosed,
			OpenedAt: at.Add(-time.Minute), ClosedAt: model.OptionalTimeFrom(at), CloseReason: model.AttemptConnectionClosePolicySuspended},
		Attempt: model.ExamAttempt{Revision: 2}, SuspensionCreated: true,
		Suspension: store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), Source: model.AttemptSuspensionSourcePolicy, FlagID: flagID,
			CandidateReason: model.AttemptSuspensionCandidateReasonFocusLossPolicy}}
	require.NoError(t, service.publishFocusLossEvaluated(context.Background(), result))
	calls := publisher.Calls()
	pub := published(t)
	// Contract: the Connection close leads, the Integrity Flag and the
	// Suspension reach managers before the candidate's Suspension, the Sitting
	// board and the candidate's Exam activity are invalidated next, and the
	// closed Connection is unbound last.
	require.Equal(t, []realtimetest.Call{
		pub(realtime.NewExamAttemptConnectionClosedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.Connection.ID,
			result.Connection.CloseReason, result.Connection.ClosedAt.Time)),
		pub(realtime.NewExamAttemptIntegrityFlaggedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, flagID,
			result.PolicyOutcome, result.RetainedEvidenceCount, result.EvidenceOverflowCount, at)),
		pub(realtime.NewExamAttemptPolicySuspendedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.Connection.ID,
			flagID, result.Suspension.ID, result.Attempt.Revision, at)),
		pub(realtime.NewCandidateExamAttemptSuspendedEvent(result.SittingID, result.AttemptID, result.CandidateUserID,
			result.Suspension.Source, result.Suspension.CandidateReason, at)),
		sittingBoardChangedCall(t, result.ExamID, result.SittingID),
		pub(realtime.NewCandidateExamActivityChangedEvent(result.CandidateUserID)),
		{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: result.Connection.ID},
	}, calls)
	require.Equal(t, "exam_attempt_connection_closed", calls[0].Event.Name)
	require.Equal(t, "exam_attempt_integrity_flagged", calls[1].Event.Name)
	require.Equal(t, "exam_attempt_suspended", calls[2].Event.Name)
	require.Equal(t, "exam_attempt_access_suspended", calls[3].Event.Name)
	require.Equal(t, result.CandidateUserID, calls[3].Event.UserID)
	require.Equal(t, "manager.sitting_board.changed", calls[4].Event.Name)
	require.Equal(t, "candidate.exam_activity.changed", calls[5].Event.Name)
	require.Contains(t, string(calls[3].Event.Data), `"reason_code":"focus_policy_review_required"`)
	requireEventDataOmits(t, calls, "duration", "source", "sequence", "credential", "session", "threshold", "private")
}

func TestParticipationExpiryEffectOmitsDuplicateCloseAfterTransportTeardown(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	service := &Service{publisher: publisher}
	at := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	result := ParticipationExpiry{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), CandidateUserID: model.NewUserID(), DatabaseTime: at,
		Attempt: model.ExamAttempt{ID: model.NewExamAttemptID(), Revision: 2},
		Connection: store.ExamAttemptManagerConnection{ID: model.NewAttemptConnectionID(), State: model.AttemptConnectionClosed,
			OpenedAt: at.Add(-time.Minute), ClosedAt: model.OptionalTimeFrom(at.Add(-time.Second)), CloseReason: model.AttemptConnectionCloseTransport},
		Flag: model.IntegrityFlag{ID: model.NewIntegrityFlagID()},
		Suspension: store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), Source: model.AttemptSuspensionSourcePolicy,
			CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost}}
	require.NoError(t, service.publishParticipationExpired(context.Background(), result))
	calls := publisher.Calls()
	// Contract: a Connection the transport already closed is neither announced
	// as closed again nor unbound; the Suspension reaches managers, then the
	// candidate, and the operational invalidations follow.
	require.Equal(t, expiryCalls(t, result, false), calls)
	require.Len(t, calls, 5)
	require.Equal(t, "exam_attempt_suspended", calls[0].Event.Name)
	require.Equal(t, "exam_attempt_access_suspended", calls[1].Event.Name)
	require.Equal(t, "candidate.exam_activity.changed", calls[2].Event.Name)
	require.Equal(t, "manager.sitting_board.changed", calls[3].Event.Name)
	require.Equal(t, realtimetest.Call{Method: realtimetest.InvalidateCurrentUserContext, UserID: result.CandidateUserID}, calls[4])
}
