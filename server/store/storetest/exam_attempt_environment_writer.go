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

// newExecutionAttemptFixture publishes the fixture Revision with execution
// enabled on a fresh profile.
func newExecutionAttemptFixture(t *testing.T, ctx context.Context, ss store.Store) examAttemptFixture {
	t.Helper()
	return newExamAttemptFixtureWithExecution(t, ctx, ss, model.ExecutionPolicy{Enabled: true, Terminals: 1})
}

// testEnvironmentOwner owns one Attempt's environment in a test: it holds
// the runtime lease and sets the incarnation, as the runtime's owner does.
type testEnvironmentOwner struct {
	ss        store.Store
	attemptID model.ExamAttemptID
	lease     store.AttemptRuntimeLease
}

// ownTestEnvironment creates the Attempt's environment from its start fact,
// claims its lease and returns the owner and its access at incarnation 0.
func ownTestEnvironment(t *testing.T, ctx context.Context, ss store.Store, attemptID model.ExamAttemptID,
	sittingID model.ExamSittingID, generation int64,
) (*testEnvironmentOwner, store.ExamAttemptWorkspaceEnvironmentAccess) {
	t.Helper()
	_, err := ss.AttemptEnvironment().ApplyFact(ctx, model.AttemptEnvironmentFact{Kind: model.AttemptEnvironmentFactParticipationStarted,
		AttemptID: attemptID, SittingID: sittingID, Generation: generation, OccurredAt: model.NowUTC()})
	require.NoError(t, err)
	_, err = ss.AttemptEnvironment().Get(ctx, attemptID)
	require.NoError(t, err, "the Revision enables execution")
	owner := &testEnvironmentOwner{ss: ss, attemptID: attemptID}
	owner.claim(t, ctx)
	return owner, owner.access(0)
}

// claim takes the Attempt's lease, which must be free.
func (o *testEnvironmentOwner) claim(t *testing.T, ctx context.Context) {
	t.Helper()
	lease, err := o.ss.AttemptRuntimeLease().Claim(ctx, o.attemptID, "test-node", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, lease, "the lease is free")
	o.lease = *lease
}

// release gives the lease up.
func (o *testEnvironmentOwner) release(t *testing.T, ctx context.Context) {
	t.Helper()
	require.NoError(t, o.ss.AttemptRuntimeLease().Release(ctx, o.lease))
}

func (o *testEnvironmentOwner) access(incarnation int64) store.ExamAttemptWorkspaceEnvironmentAccess {
	return store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: o.attemptID, Incarnation: incarnation, Fence: o.lease.Fence}
}

// incarnate records a new incarnation of the environment and returns its
// access.
func (o *testEnvironmentOwner) incarnate(t *testing.T, ctx context.Context, incarnation int64) store.ExamAttemptWorkspaceEnvironmentAccess {
	t.Helper()
	current, err := o.ss.AttemptEnvironment().Get(ctx, o.attemptID)
	require.NoError(t, err)
	next := *current
	next.Incarnation, next.State = incarnation, model.AttemptEnvironmentRunning
	_, err = o.ss.AttemptEnvironment().SaveOwner(ctx, &store.AttemptEnvironmentOwnerSave{Lease: o.lease,
		ExpectedRevision: current.Revision, Environment: next})
	require.NoError(t, err)
	return o.access(incarnation)
}

// requireEnvironmentRefused asserts that access neither writes nor reads,
// refused with constraint.
func requireEnvironmentRefused(t *testing.T, ctx context.Context, ss store.Store,
	access store.ExamAttemptWorkspaceEnvironmentAccess, constraint, why string,
) {
	t.Helper()
	_, err := ss.ExamAttemptWorkspace().ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	requireConflictConstraint(t, err, constraint)
	_, err = ss.ExamAttemptWorkspace().List(ctx, store.CandidateWorkspaceListOptions{Access: access, ExpectedCursor: -1, Limit: 10})
	requireConflictConstraint(t, err, constraint)
	require.Error(t, err, why)
}

// requireEnvironmentWrites asserts that access still writes and reads.
func requireEnvironmentWrites(t *testing.T, ctx context.Context, ss store.Store, fixture examAttemptFixture,
	access store.ExamAttemptWorkspaceEnvironmentAccess, key, why string,
) {
	t.Helper()
	_, err := ss.ExamAttemptWorkspace().ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, store.ExamAttemptWorkspaceMutation{
		Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: key}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, key))
	require.NoError(t, err, why)
	_, err = ss.ExamAttemptWorkspace().List(ctx, store.CandidateWorkspaceListOptions{Access: access, ExpectedCursor: -1, Limit: 10})
	require.NoError(t, err, why)
}

func connectEnvironmentFixture(t *testing.T, ctx context.Context, ss store.Store, fixture examAttemptFixture, key string,
) (*store.ExamAttemptConnectResult, store.ExamAttemptWorkspaceEditorAccess) {
	t.Helper()
	credentialHash := model.HashToken(model.NewCredentialToken())
	input := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(),
		WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(),
		ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: credentialHash,
		AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, input)
	connected, err := ss.ExamAttempt().Connect(ctx, input,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, key, key))
	require.NoError(t, err)
	return connected, store.ExamAttemptWorkspaceEditorAccess{AttemptID: connected.Attempt.ID,
		ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation,
		CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint,
		ConnectionID: connected.Connection.ID, ContinuityCredentialHash: credentialHash}
}

// testAttemptEnvironmentWriter covers the execution environment writer: its
// batch beside the editor's under the owner's lease and incarnation, and
// that nothing but a stale owner or the Submission stops it. A frozen,
// Kicked or expired Participation, a passed deadline and a revoked Session
// leave it writing so the owner can harvest the guest's last changes. The
// seals are covered in the Submission suite.
func testAttemptEnvironmentWriter(t *testing.T, ss store.Store, workspace store.ExamAttemptWorkspaceStore,
	probes ...ExamAttemptWorkspaceSQLProbe,
) {
	t.Helper()
	ctx := context.Background()
	t.Run("environment writer", func(t *testing.T) {
		testAttemptEnvironmentWriterLifecycle(t, ctx, ss, workspace)
	})
	if len(probes) == 0 || probes[0].SetParticipationLeaseExpired == nil || probes[0].PassSittingScheduledEnd == nil {
		return
	}
	probe := probes[0]

	t.Run("environment writer outlasts the deadline and the Participation", func(t *testing.T) {
		fixture := newExecutionAttemptFixture(t, ctx, ss)
		connected, _ := connectEnvironmentFixture(t, ctx, ss, fixture, "environment-deadline-connect")
		_, access := ownTestEnvironment(t, ctx, ss, connected.Attempt.ID, fixture.sitting.ID, connected.Participation.Generation)
		probe.SetParticipationLeaseExpired(t, ctx, connected.Participation.ID)
		requireEnvironmentWrites(t, ctx, ss, fixture, access, "after-lease-expiry", "an expired Participation lease")
		probe.PassSittingScheduledEnd(t, ctx, fixture.sitting.ID)
		requireEnvironmentWrites(t, ctx, ss, fixture, access, "after-deadline", "a passed deadline")
	})
}

func testAttemptEnvironmentWriterLifecycle(t *testing.T, ctx context.Context, ss store.Store, workspace store.ExamAttemptWorkspaceStore) {
	fixture := newExecutionAttemptFixture(t, ctx, ss)
	connected, editor := connectEnvironmentFixture(t, ctx, ss, fixture, "environment-connect")
	attemptID := connected.Attempt.ID

	for name, access := range map[string]store.ExamAttemptWorkspaceEnvironmentAccess{
		"no Attempt":           {Fence: 1},
		"no fence":             {AttemptID: attemptID},
		"negative incarnation": {AttemptID: attemptID, Fence: 1, Incarnation: -1},
	} {
		_, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
		requireInvalidInput(t, err, "ReserveObjects(%s)", name)
	}
	_, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{
		Access: store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: attemptID, Fence: 1}})
	requireConflictConstraint(t, err, "attempt_environment_owner")

	owner, _ := ownTestEnvironment(t, ctx, ss, attemptID, fixture.sitting.ID, connected.Participation.Generation)
	access := owner.incarnate(t, ctx, 1)

	// The environment resolves the candidate's target and reads the same
	// Workspace the candidate does.
	target, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	require.NoError(t, err)
	require.Equal(t, fixture.candidate.ID, target.CandidateUserID)
	require.Equal(t, connected.Workspace.ID, target.WorkspaceID)
	page, err := workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: access, ExpectedCursor: -1, Limit: 200})
	require.NoError(t, err)
	candidatePage, err := workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: journalAccess(editor), ExpectedCursor: -1, Limit: 200})
	require.NoError(t, err)
	require.Equal(t, candidatePage, page)

	// A harvested file is the candidate's change by the environment author.
	content := model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 6, SHA256: strings.Repeat("e", 64)}
	object, harvested := createAttemptWorkspaceTestFile(t, ctx, ss, fixture, access, "harvest.txt", content, "environment-file")
	require.Equal(t, access.WorkspaceAuthor(), harvested.Changes[0].Author)
	require.Equal(t, model.AttemptWorkspaceAuthorEnvironment, harvested.Changes[0].Author.Kind)
	resolved, err := workspace.ResolveFile(ctx, access, harvested.Changes[0].EntryID)
	require.NoError(t, err)
	require.Equal(t, object.ObjectID, resolved.AttemptObjectID)
	require.Equal(t, object.ContentVersion, resolved.ContentVersion)

	directory := store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
		EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "harvested"}
	batch := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, directory)
	command := attemptWorkspaceBatchCommand(fixture.candidate.ID, "environment-directory")
	applied, err := workspace.ApplyBatch(ctx, batch, command)
	require.NoError(t, err)
	require.True(t, applied.ConsistentWith(target, access.WorkspaceAuthor(), batch.Mutations),
		"ApplyBatch returned a result inconsistent with its environment request")
	require.False(t, applied.ConsistentWith(target, editor.WorkspaceAuthor(), batch.Mutations))
	audit, err := ss.Audit().Get(ctx, batch.AuditEventID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, audit.Status)
	require.Equal(t, fixture.candidate.ID, audit.ActorID, "the candidate is the actor of an environment batch")
	recorded := requireAttemptWorkspaceBatchAudit(t, audit, 1, applied)
	require.Equal(t, access.WorkspaceAuthor(), recorded.Author)
	require.Equal(t, access.Fence, recorded.Fence)
	require.NotContains(t, string(audit.Result), "harvested", "environment batch audit exposed a path")

	// An exact retry replays under the candidate; another User's key is not
	// the candidate's outcome.
	retry := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, directory)
	replayed, err := workspace.ApplyBatch(ctx, retry, command)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, applied.Changes, replayed.Changes)
	_, err = workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, store.ExamAttemptWorkspaceMutation{
		Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "foreign"}),
		attemptWorkspaceBatchCommand(fixture.manager.ID, "environment-foreign-user"))
	requireInvalidInput(t, err, "ApplyBatch(environment outcome under another User)")

	edited, err := workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, editor, store.ExamAttemptWorkspaceMutation{
		Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "edited"}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "environment-editor"))
	require.NoError(t, err)
	require.Equal(t, editor.WorkspaceAuthor(), edited.Changes[0].Author)
	journal, err := workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: journalAccess(editor),
		AfterCursor: harvested.Changes[0].Cursor - 1, Limit: 200})
	require.NoError(t, err)
	authors := make([]model.AttemptWorkspaceAuthor, len(journal.Entries))
	for index, entry := range journal.Entries {
		authors[index] = entry.Author
	}
	require.Equal(t, []model.AttemptWorkspaceAuthor{access.WorkspaceAuthor(),
		access.WorkspaceAuthor(), editor.WorkspaceAuthor()}, authors)
	environmentJournal, err := workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: access,
		AfterCursor: harvested.Changes[0].Cursor - 1, Limit: 200})
	require.NoError(t, err)
	require.Equal(t, journal, environmentJournal, "the environment reads the same journal")

	// Only the current incarnation under the current lease is the owner.
	for name, change := range map[string]func(*store.ExamAttemptWorkspaceEnvironmentAccess){
		"an older incarnation": func(a *store.ExamAttemptWorkspaceEnvironmentAccess) { a.Incarnation-- },
		"a newer incarnation":  func(a *store.ExamAttemptWorkspaceEnvironmentAccess) { a.Incarnation++ },
		"another fence":        func(a *store.ExamAttemptWorkspaceEnvironmentAccess) { a.Fence++ },
	} {
		mismatched := access
		change(&mismatched)
		requireEnvironmentRefused(t, ctx, ss, mismatched, "attempt_environment_owner", name)
	}
	restarted := owner.incarnate(t, ctx, 2)
	requireEnvironmentRefused(t, ctx, ss, access, "attempt_environment_owner", "a replaced incarnation")
	requireEnvironmentWrites(t, ctx, ss, fixture, restarted, "restarted", "the new incarnation")

	// A released lease fences its owner out; the next claim owns it.
	owner.release(t, ctx)
	requireEnvironmentRefused(t, ctx, ss, restarted, "attempt_environment_owner", "a released lease")
	owner.claim(t, ctx)
	reclaimed := owner.access(2)
	require.Greater(t, reclaimed.Fence, restarted.Fence)
	requireEnvironmentWrites(t, ctx, ss, fixture, reclaimed, "reclaimed", "the next lease")

	// A Kick freezes the environment but leaves its owner writing, as does
	// a revoked Session.
	kick := &store.ExamAttemptManualSuspension{ExamID: fixture.examID, SittingID: fixture.sitting.ID,
		AttemptID: attemptID, SuspensionID: model.NewAttemptSuspensionID(), ActorUserID: fixture.manager.ID,
		ExpectedRevision: connected.Attempt.Revision, PrivateReason: "candidate left the examination room",
		AuditEventID: saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	_, err = ss.ExamAttempt().SuspendAttempt(ctx, kick,
		examCommand(fixture.manager.ID, store.ExamAttemptSuspendOperation, "environment-kick", "environment-kick"))
	require.NoError(t, err)
	requireEnvironmentWrites(t, ctx, ss, fixture, reclaimed, "after-kick", "a Kick")
	_, err = RevokeAllUserSessions(ctx, ss, fixture.candidate.ID, model.NowUTC())
	require.NoError(t, err)
	requireEnvironmentWrites(t, ctx, ss, fixture, reclaimed, "after-revocation", "a revoked Session")
}
