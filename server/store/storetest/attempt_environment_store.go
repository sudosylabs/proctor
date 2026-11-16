// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// saveFixtureEnvironmentProfile makes actorID a system administrator and
// creates a live environment profile, returning its ID.
func saveFixtureEnvironmentProfile(t *testing.T, ctx context.Context, ss store.Store, institutionID model.InstitutionID,
	actorID model.UserID,
) model.EnvironmentProfileID {
	t.Helper()
	role, err := ss.Role().GetByName(ctx, model.SystemAdministratorRoleName)
	if store.IsNotFound(err) {
		role, err = SaveRole(ctx, ss, &model.Role{Name: model.SystemAdministratorRoleName, DisplayName: "System Administrator",
			Permissions: model.AllActions(), BuiltIn: true})
	}
	require.NoError(t, err)
	_, err = SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: actorID, RoleID: role.ID, ScopeType: model.RoleScopeInstitution,
		ScopeID: institutionID.String(), StartsAt: model.NowUTC().Add(-time.Hour)})
	require.NoError(t, err)
	profile, err := model.NewEnvironmentProfile(model.NewEnvironmentProfileID(), model.EnvironmentProfileSpecification{
		Name: "fixture-" + strings.ToLower(model.NewID()[:10]), Image: "python-3.12",
		Resources: model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 2 << 30},
		Network:   model.EnvironmentNetworkNone, IdleWindow: 10 * time.Minute, MaxTerminals: 4,
	}, actorID, model.NowUTC())
	require.NoError(t, err)
	input := &store.EnvironmentProfileCreation{Profile: profile}
	input.AuditEventID, input.AuditAt = saveEnvironmentProfileAudit(t, ctx, ss, institutionID, actorID)
	created, err := ss.EnvironmentProfile().Create(ctx, input,
		environmentProfileCommand(actorID, store.EnvironmentProfileCreateOperation, "fixture-"+profile.ID.String()))
	require.NoError(t, err)
	return created.Profile.ID
}

// TestAttemptEnvironmentStore checks that facts create and steer an Attempt's
// environment, that runtime leases fence their owners, and that an owner
// writes only under its current lease and the revision it read.
func TestAttemptEnvironmentStore(t *testing.T, ss store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	environments, leases := ss.AttemptEnvironment(), ss.AttemptRuntimeLease()

	fixture := newExamAttemptFixtureWithExecution(t, ctx, ss, model.ExecutionPolicy{Enabled: true, RecordInput: true, Terminals: 2})
	connected, _ := connectEnvironmentFixture(t, ctx, ss, fixture, "attempt-environment-connect")
	attemptID, sittingID := connected.Attempt.ID, fixture.sitting.ID
	fact := func(kind model.AttemptEnvironmentFactKind, generation int64, reason model.AttemptParticipationEndReason) model.AttemptEnvironmentFact {
		return model.AttemptEnvironmentFact{Kind: kind, AttemptID: attemptID, SittingID: sittingID, Generation: generation,
			Reason: reason, OccurredAt: model.NowUTC()}
	}

	// Only a start creates the environment, and a repeated start changes
	// nothing.
	ended, err := environments.ApplyFact(ctx, fact(model.AttemptEnvironmentFactParticipationEnded, 1, model.AttemptParticipationEndInterrupted))
	require.NoError(t, err)
	require.Empty(t, ended.Changed)
	_, err = environments.Get(ctx, attemptID)
	require.True(t, store.IsNotFound(err), "an end creates no environment: %v", err)

	started, err := environments.ApplyFact(ctx, fact(model.AttemptEnvironmentFactParticipationStarted, 1, ""))
	require.NoError(t, err)
	require.Equal(t, []model.ExamAttemptID{attemptID}, started.Changed)
	environment, err := environments.Get(ctx, attemptID)
	require.NoError(t, err)
	require.Equal(t, model.AttemptEnvironmentDesiredRunning, environment.Desired)
	require.Equal(t, model.AttemptEnvironmentPending, environment.State)
	require.Equal(t, 2, environment.Terminals)
	require.True(t, environment.RecordInput)
	require.Equal(t, "python-3.12", environment.Image, "the profile is copied")
	repeated, err := environments.ApplyFact(ctx, fact(model.AttemptEnvironmentFactParticipationStarted, 1, ""))
	require.NoError(t, err)
	require.Empty(t, repeated.Changed)

	// The Sitting's managers see the environment on the board.
	board, err := ss.ExamAttempt().ListSittingCandidateStatuses(ctx, store.SittingCandidateStatusListOptions{
		ExamID: fixture.examID, SittingID: sittingID, ExcludeCandidateUserID: fixture.manager.ID, Limit: 10})
	require.NoError(t, err)
	require.Zero(t, board.EnvironmentsWaitingCapacity)
	var listed *store.SittingCandidateEnvironment
	for _, item := range board.Items {
		if item.Attempt != nil && item.Attempt.ID == attemptID {
			listed = item.Environment
		}
	}
	require.NotNil(t, listed, "the board shows the candidate's environment")
	require.Equal(t, model.AttemptEnvironmentPending, listed.State)
	require.Equal(t, model.AttemptEnvironmentDesiredRunning, listed.Desired)

	// A manager asks for a new machine of the incarnation they saw; asking
	// again repeats, and another incarnation conflicts.
	replace := func(t *testing.T, expected int64, key string) (*store.AttemptEnvironmentReplacementResult, error) {
		t.Helper()
		return environments.Replace(ctx, &store.AttemptEnvironmentReplacement{ExamID: fixture.examID, SittingID: sittingID,
			AttemptID: attemptID, ActorUserID: fixture.manager.ID, ExpectedIncarnation: expected,
			AuditEventID: saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()},
			examCommand(fixture.manager.ID, store.AttemptEnvironmentReplaceOperation, key, key))
	}
	replaced, err := replace(t, 0, "replace-first")
	require.NoError(t, err)
	require.Equal(t, int64(1), replaced.ReplaceBefore)
	require.True(t, replaced.Environment.ReplacePending())
	replayed, err := replace(t, 0, "replace-first")
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	again, err := replace(t, 0, "replace-again")
	require.NoError(t, err)
	require.Equal(t, replaced.Environment.Revision, again.Environment.Revision, "the same incarnation again changes nothing")
	_, err = replace(t, 1, "replace-newer")
	requireConflictConstraint(t, err, "attempt_environment_incarnation")
	_, err = environments.Replace(ctx, &store.AttemptEnvironmentReplacement{ExamID: fixture.examID, SittingID: sittingID,
		AttemptID: attemptID, ActorUserID: fixture.candidate.ID, ExpectedIncarnation: 0,
		AuditEventID: saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()},
		examCommand(fixture.candidate.ID, store.AttemptEnvironmentReplaceOperation, "replace-self", "replace-self"))
	require.True(t, store.IsNotFound(err), "the candidate is no manager: %v", err)

	// A Sitting fact reaches every environment in the Sitting.
	paused, err := environments.ApplyFact(ctx, model.AttemptEnvironmentFact{Kind: model.AttemptEnvironmentFactSittingPaused,
		SittingID: sittingID, OccurredAt: model.NowUTC()})
	require.NoError(t, err)
	require.Equal(t, []model.ExamAttemptID{attemptID}, paused.Changed)
	resumed, err := environments.ApplyFact(ctx, model.AttemptEnvironmentFact{Kind: model.AttemptEnvironmentFactSittingResumed,
		SittingID: sittingID, OccurredAt: model.NowUTC()})
	require.NoError(t, err)
	require.Equal(t, []model.ExamAttemptID{attemptID}, resumed.Changed, "the Participation is active, so it thaws")
	environment, err = environments.Get(ctx, attemptID)
	require.NoError(t, err)
	require.Equal(t, model.AttemptEnvironmentDesiredRunning, environment.Desired)

	// One node holds the lease at a time, and each claim takes a new fence.
	lease, err := leases.Claim(ctx, &store.AttemptRuntimeLeaseClaim{AttemptID: attemptID, NodeID: "node-a", Lifetime: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, lease)
	other, err := leases.Claim(ctx, &store.AttemptRuntimeLeaseClaim{AttemptID: attemptID, NodeID: "node-b", Lifetime: time.Minute})
	require.NoError(t, err)
	require.Nil(t, other, "a live lease is not taken over")
	available, err := leases.ClaimAvailable(ctx, &store.AttemptRuntimeLeaseAvailableClaim{NodeID: "node-b", Lifetime: time.Minute, Limit: 1000})
	require.NoError(t, err)
	for _, claimed := range available {
		require.NotEqual(t, attemptID, claimed.AttemptID, "a held environment is not available")
		require.NoError(t, leases.Release(ctx, &store.AttemptRuntimeLeaseRelease{Lease: claimed}))
	}
	renewed, err := leases.Renew(ctx, &store.AttemptRuntimeLeaseRenewal{Lease: *lease, Lifetime: 2 * time.Minute})
	require.NoError(t, err)
	require.Equal(t, lease.Fence, renewed.Fence)
	require.True(t, renewed.ExpiresAt.After(lease.ExpiresAt))

	// The owner writes under its lease and the revision it read.
	owned := *environment
	owned.State, owned.Incarnation, owned.Host, owned.GrantID, owned.Epoch = model.AttemptEnvironmentRunning, 1, "exec-1", "grant-1", "epoch-1"
	owned.ProjectionCursor, owned.EvidenceSequence = 3, 2
	projectedFile := model.NewAttemptWorkspaceEntryID()
	owned.ProjectedVersions = map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion{
		projectedFile: model.NewWorkspaceContentVersion(),
	}
	owned.SyncPaths = []model.AttemptEnvironmentSyncPath{
		{Path: "node_modules", Status: model.AttemptEnvironmentPathIgnored, Reason: model.AttemptEnvironmentIgnoredByDefault},
		{Path: "big.bin", Status: model.AttemptEnvironmentPathRefused, Reason: "exam.attempt.workspace.size_limit"},
	}
	saved, err := environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: *renewed,
		ExpectedRevision: environment.Revision, Environment: owned})
	require.NoError(t, err)
	require.Equal(t, environment.Revision+1, saved.Revision)
	require.Equal(t, "grant-1", saved.GrantID)
	reread, err := environments.Get(ctx, attemptID)
	require.NoError(t, err)
	require.Equal(t, owned.ProjectedVersions, reread.ProjectedVersions, "the projected versions round-trip")
	require.Equal(t, owned.SyncPaths, reread.SyncPaths, "the sync statuses round-trip in order")
	invalid := owned
	invalid.SyncPaths = []model.AttemptEnvironmentSyncPath{{Path: "x", Status: "synced", Reason: "default"}}
	_, err = environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: *renewed,
		ExpectedRevision: saved.Revision, Environment: invalid})
	requireInvalidInput(t, err, "SaveOwner(an unknown sync status)")
	require.Equal(t, int64(2), reread.EvidenceSequence)
	require.Equal(t, model.AttemptEnvironmentDesiredRunning, saved.Desired, "the owner does not set what is desired")
	_, err = environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: *renewed,
		ExpectedRevision: environment.Revision, Environment: owned})
	requireConflictConstraint(t, err, "attempt_environment_revision")

	// A released lease fences its old owner out, and the next claim moves on.
	require.NoError(t, leases.Release(ctx, &store.AttemptRuntimeLeaseRelease{Lease: *renewed}))
	_, err = environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: *renewed,
		ExpectedRevision: saved.Revision, Environment: owned})
	require.ErrorIs(t, err, store.ErrAttemptRuntimeLeaseLost, "a released lease still writes")
	_, err = leases.Renew(ctx, &store.AttemptRuntimeLeaseRenewal{Lease: *renewed, Lifetime: time.Minute})
	require.ErrorIs(t, err, store.ErrAttemptRuntimeLeaseLost, "a released lease renews")
	next, err := leases.Claim(ctx, &store.AttemptRuntimeLeaseClaim{AttemptID: attemptID, NodeID: "node-b", Lifetime: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Greater(t, next.Fence, lease.Fence)

	// A seal asks for a settle; the owner completes it, or the seal records
	// it as a gap and goes ahead.
	first, err := environments.RequestSettle(ctx, attemptID)
	require.NoError(t, err)
	require.Equal(t, int64(1), first)
	pending, err := environments.Get(ctx, attemptID)
	require.NoError(t, err)
	require.True(t, pending.SettlePending())
	require.False(t, pending.SettleRequestedAt.IsZero(), "the settle's budget counts from when it was asked")
	settled := *pending
	settled.SettleCompleted = 2
	_, err = environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: *next,
		ExpectedRevision: pending.Revision, Environment: settled})
	requireInvalidInput(t, err, "SaveOwner(a settle nobody asked)")
	settled.SettleCompleted = 1
	completed, err := environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: *next,
		ExpectedRevision: pending.Revision, Environment: settled})
	require.NoError(t, err)
	require.False(t, completed.SettlePending())
	closing, err := environments.ApplyFact(ctx, model.AttemptEnvironmentFact{Kind: model.AttemptEnvironmentFactSittingClosing,
		SittingID: sittingID, OccurredAt: model.NowUTC()})
	require.NoError(t, err)
	require.Equal(t, []model.ExamAttemptID{attemptID}, closing.Changed, "a closing Sitting asks for a settle")
	gap := &store.AttemptEnvironmentSettleGap{AttemptID: attemptID, Settle: 2, EvidenceID: model.NewIntegrityEvidenceID(),
		ObservedAt: model.NowUTC()}
	require.NoError(t, environments.RecordSettleIncomplete(ctx, gap))
	require.NoError(t, environments.RecordSettleIncomplete(ctx, gap), "a recorded settle gap repeats as nothing")
	given, err := environments.Get(ctx, attemptID)
	require.NoError(t, err)
	require.Equal(t, int64(2), given.SettleCompleted)
	err = environments.RecordSettleIncomplete(ctx, &store.AttemptEnvironmentSettleGap{AttemptID: attemptID, Settle: 3,
		EvidenceID: model.NewIntegrityEvidenceID(), ObservedAt: model.NowUTC()})
	requireInvalidInput(t, err, "RecordSettleIncomplete(a settle nobody asked)")

	// An older generation's end changes nothing; the Submission ends it, and
	// an ended environment ignores later facts.
	stale, err := environments.ApplyFact(ctx, fact(model.AttemptEnvironmentFactParticipationEnded, 0, model.AttemptParticipationEndLeaseExpired))
	require.NoError(t, err)
	require.Empty(t, stale.Changed)
	submitted, err := environments.ApplyFact(ctx, fact(model.AttemptEnvironmentFactSubmitted, 0, ""))
	require.NoError(t, err)
	require.Equal(t, []model.ExamAttemptID{attemptID}, submitted.Changed)
	after, err := environments.ApplyFact(ctx, fact(model.AttemptEnvironmentFactParticipationStarted, 2, ""))
	require.NoError(t, err)
	require.Empty(t, after.Changed)
	environment, err = environments.Get(ctx, attemptID)
	require.NoError(t, err)
	require.Equal(t, model.AttemptEnvironmentDesiredEnded, environment.Desired)
	require.False(t, environment.Done(), "it is done once its owner ends it")

	finished := *environment
	finished.State = model.AttemptEnvironmentEnded
	_, err = environments.SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: *next,
		ExpectedRevision: environment.Revision, Environment: finished})
	require.NoError(t, err)
	require.NoError(t, leases.Release(ctx, &store.AttemptRuntimeLeaseRelease{Lease: *next}))
	available, err = leases.ClaimAvailable(ctx, &store.AttemptRuntimeLeaseAvailableClaim{NodeID: "node-c", Lifetime: time.Minute, Limit: 1000})
	require.NoError(t, err)
	for _, claimed := range available {
		require.NotEqual(t, attemptID, claimed.AttemptID, "a done environment is never claimed again")
		require.NoError(t, leases.Release(ctx, &store.AttemptRuntimeLeaseRelease{Lease: claimed}))
	}

	// An Attempt whose Revision does not enable execution has none.
	plain := newExamAttemptFixture(t, ctx, ss)
	plainConnected, _ := connectEnvironmentFixture(t, ctx, ss, plain, "attempt-environment-plain")
	none, err := environments.ApplyFact(ctx, model.AttemptEnvironmentFact{Kind: model.AttemptEnvironmentFactParticipationStarted,
		AttemptID: plainConnected.Attempt.ID, SittingID: plain.sitting.ID, Generation: 1, OccurredAt: model.NowUTC()})
	require.NoError(t, err)
	require.Empty(t, none.Changed)
}
