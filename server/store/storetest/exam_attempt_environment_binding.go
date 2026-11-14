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

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// environmentIdentity is a test execution environment identity.
func environmentIdentity(grant, epoch string, incarnation int64) model.AttemptEnvironmentIdentity {
	return model.AttemptEnvironmentIdentity{GrantID: grant, Epoch: epoch, Incarnation: incarnation}
}

// bindTestEnvironment binds identity to the Attempt's generation and returns
// the binding and the access an environment writes and reads through.
func bindTestEnvironment(t *testing.T, ctx context.Context, ss store.Store, attemptID model.ExamAttemptID,
	generation int64, identity model.AttemptEnvironmentIdentity,
) (*store.ExamAttemptEnvironmentBindResult, store.ExamAttemptWorkspaceEnvironmentAccess) {
	t.Helper()
	input := &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(), AttemptID: attemptID,
		Generation: generation, Identity: identity}
	bound, err := ss.ExamAttemptWorkspace().BindEnvironment(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, bound)
	require.NoError(t, bound.Binding.Validate())
	require.True(t, bound.Binding.Live())
	require.False(t, bound.Repeated)
	require.Equal(t, input.BindingID, bound.Binding.ID)
	require.Equal(t, attemptID, bound.Binding.AttemptID)
	require.Equal(t, generation, bound.Binding.Generation)
	require.Equal(t, identity, bound.Binding.Identity)
	return bound, environmentAccess(bound.Binding)
}

func environmentAccess(binding model.AttemptEnvironmentBinding) store.ExamAttemptWorkspaceEnvironmentAccess {
	return store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: binding.AttemptID, BindingID: binding.ID,
		Identity: binding.Identity, Generation: binding.Generation}
}

// requireEnvironmentBindingEnded asserts that the binding has already ended,
// so ending it again changes nothing, and that it no longer writes or reads.
func requireEnvironmentBindingEnded(t *testing.T, ctx context.Context, ss store.Store,
	access store.ExamAttemptWorkspaceEnvironmentAccess, why string,
) {
	t.Helper()
	ended, err := ss.ExamAttemptWorkspace().EndEnvironmentBinding(ctx, &store.ExamAttemptEnvironmentBindingEnd{
		AttemptID: access.AttemptID, BindingID: access.BindingID})
	require.NoError(t, err, why)
	require.False(t, ended.Changed, "%s: the binding was still live", why)
	require.False(t, ended.Binding.Live(), why)
	_, err = ss.ExamAttemptWorkspace().ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	requireConflictConstraint(t, err, "attempt_environment_binding_current")
	_, err = ss.ExamAttemptWorkspace().List(ctx, store.CandidateWorkspaceListOptions{Access: access, ExpectedCursor: -1, Limit: 10})
	require.True(t, store.IsNotFound(err), "%s: an ended binding still reads: %v", why, err)
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

// testAttemptEnvironmentBinding covers the execution environment writer: the
// binding's lifecycle, the environment batch beside the editor's, and every
// way a Participation generation ends taking its binding with it. Voluntary
// and automatic seal, manager end, policy suspension and pending corrections
// are covered in the Submission and correction suites.
func testAttemptEnvironmentBinding(t *testing.T, ss store.Store, workspace store.ExamAttemptWorkspaceStore,
	probes ...ExamAttemptWorkspaceSQLProbe,
) {
	t.Helper()
	ctx := context.Background()
	t.Run("environment binding lifecycle and writer", func(t *testing.T) {
		testAttemptEnvironmentBindingLifecycle(t, ctx, ss, workspace)
	})
	if len(probes) == 0 || probes[0].SetParticipationLeaseExpired == nil || probes[0].PassSittingScheduledEnd == nil {
		return
	}
	probe := probes[0]

	t.Run("environment writer stops at the deadline", func(t *testing.T) {
		fixture := newExamAttemptFixture(t, ctx, ss)
		connected, _ := connectEnvironmentFixture(t, ctx, ss, fixture, "environment-deadline-connect")
		_, access := bindTestEnvironment(t, ctx, ss, connected.Attempt.ID, connected.Participation.Generation,
			environmentIdentity("grant-deadline", "epoch-1", 0))
		probe.PassSittingScheduledEnd(t, ctx, fixture.sitting.ID)
		_, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
		requireConflictConstraint(t, err, "exam_sitting_state")
		_, err = workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: access, ExpectedCursor: -1, Limit: 10})
		require.True(t, store.IsNotFound(err), "an environment reads past the deadline: %v", err)
	})

	t.Run("lease expiry ends the binding", func(t *testing.T) {
		fixture := newExamAttemptFixture(t, ctx, ss)
		connected, _ := connectEnvironmentFixture(t, ctx, ss, fixture, "environment-expiry-connect")
		_, access := bindTestEnvironment(t, ctx, ss, connected.Attempt.ID, connected.Participation.Generation,
			environmentIdentity("grant-expiry", "epoch-1", 0))
		probe.SetParticipationLeaseExpired(t, ctx, connected.Participation.ID)
		_, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
		requireConflictConstraint(t, err, "attempt_participation_expired")
		_, err = workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
			AttemptID: connected.Attempt.ID, Generation: connected.Participation.Generation,
			Identity: environmentIdentity("grant-expiry", "epoch-2", 1)})
		requireConflictConstraint(t, err, "attempt_participation_expired")
		_, err = ss.ExamAttempt().ExpireParticipation(ctx, &store.ExamAttemptParticipationExpiry{
			AttemptID: connected.Attempt.ID, ParticipationID: connected.Participation.ID,
			Generation: connected.Participation.Generation, EvidenceID: model.NewIntegrityEvidenceID(),
			FlagID: model.NewIntegrityFlagID(), SuspensionID: model.NewAttemptSuspensionID(),
			AuditEventID: saveExamAttemptSystemAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
		})
		require.NoError(t, err)
		requireEnvironmentBindingEnded(t, ctx, ss, access, "lease expiry")
	})
}

func testAttemptEnvironmentBindingLifecycle(t *testing.T, ctx context.Context, ss store.Store, workspace store.ExamAttemptWorkspaceStore) {
	fixture := newExamAttemptFixture(t, ctx, ss)
	connected, editor := connectEnvironmentFixture(t, ctx, ss, fixture, "environment-connect")
	attemptID, generation := connected.Attempt.ID, connected.Participation.Generation

	for name, input := range map[string]*store.ExamAttemptEnvironmentBind{
		"no input":     nil,
		"no binding":   {AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("g", "e", 0)},
		"no Attempt":   {BindingID: model.NewAttemptEnvironmentBindingID(), Generation: generation, Identity: environmentIdentity("g", "e", 0)},
		"no grant":     {BindingID: model.NewAttemptEnvironmentBindingID(), AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("", "e", 0)},
		"unsafe epoch": {BindingID: model.NewAttemptEnvironmentBindingID(), AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("g", "e/1", 0)},
		"long grant": {BindingID: model.NewAttemptEnvironmentBindingID(), AttemptID: attemptID, Generation: generation,
			Identity: environmentIdentity(strings.Repeat("g", model.AttemptEnvironmentGrantIDMaximumBytes+1), "e", 0)},
	} {
		_, err := workspace.BindEnvironment(ctx, input)
		requireInvalidInput(t, err, "BindEnvironment(%s)", name)
	}
	_, err := workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
		AttemptID: attemptID, Generation: generation + 1, Identity: environmentIdentity("grant-1", "epoch-0", 0)})
	requireConflictConstraint(t, err, "attempt_participation_generation")

	first, access := bindTestEnvironment(t, ctx, ss, attemptID, generation, environmentIdentity("grant-1", "epoch-1", 1))
	require.Equal(t, connected.Participation.ID, first.Binding.ParticipationID)
	require.Nil(t, first.Replaced)
	repeated, err := workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
		AttemptID: attemptID, Generation: generation, Identity: first.Binding.Identity})
	require.NoError(t, err)
	require.True(t, repeated.Repeated, "binding the live identity again repeats it")
	require.Equal(t, first.Binding, repeated.Binding)

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

	// A harvested file is the candidate's change with origin environment.
	content := model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 6, SHA256: strings.Repeat("e", 64)}
	object, harvested := createAttemptWorkspaceTestFile(t, ctx, ss, fixture, access, "harvest.txt", content, "environment-file")
	require.Equal(t, model.AttemptWorkspaceOriginEnvironment, harvested.Changes[0].Origin)
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
	require.True(t, applied.ConsistentWith(target, model.AttemptWorkspaceOriginEnvironment, batch.Mutations),
		"ApplyBatch returned a result inconsistent with its environment request")
	require.False(t, applied.ConsistentWith(target, model.AttemptWorkspaceOriginEditor, batch.Mutations))
	audit, err := ss.Audit().Get(ctx, batch.AuditEventID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, audit.Status)
	require.Equal(t, fixture.candidate.ID, audit.ActorID, "the candidate is the actor of an environment batch")
	recorded := requireAttemptWorkspaceBatchAudit(t, audit, 1, applied)
	require.Equal(t, model.AttemptWorkspaceOriginEnvironment, recorded.Origin)
	require.Equal(t, access.BindingID.String(), recorded.BindingID)
	for _, secret := range []string{"harvested", access.Identity.GrantID, access.Identity.Epoch} {
		require.NotContains(t, string(audit.Result), secret, "environment batch audit exposed %q", secret)
	}

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
	require.Equal(t, model.AttemptWorkspaceOriginEditor, edited.Changes[0].Origin)
	journal, err := workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: journalAccess(editor),
		AfterCursor: harvested.Changes[0].Cursor - 1, Limit: 200})
	require.NoError(t, err)
	origins := make([]model.AttemptWorkspaceOrigin, len(journal.Entries))
	for index, entry := range journal.Entries {
		origins[index] = entry.Origin
	}
	require.Equal(t, []model.AttemptWorkspaceOrigin{model.AttemptWorkspaceOriginEnvironment,
		model.AttemptWorkspaceOriginEnvironment, model.AttemptWorkspaceOriginEditor}, origins)
	environmentJournal, err := workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: access,
		AfterCursor: harvested.Changes[0].Cursor - 1, Limit: 200})
	require.NoError(t, err)
	require.Equal(t, journal, environmentJournal, "the environment reads the same journal")

	// The access must match the live binding exactly.
	for name, change := range map[string]func(*store.ExamAttemptWorkspaceEnvironmentAccess){
		"another epoch":       func(a *store.ExamAttemptWorkspaceEnvironmentAccess) { a.Identity.Epoch = "epoch-x" },
		"another grant":       func(a *store.ExamAttemptWorkspaceEnvironmentAccess) { a.Identity.GrantID = "grant-x" },
		"another incarnation": func(a *store.ExamAttemptWorkspaceEnvironmentAccess) { a.Identity.Incarnation++ },
	} {
		mismatched := access
		change(&mismatched)
		_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: mismatched})
		requireConflictConstraint(t, err, "attempt_environment_binding_current")
		_, err = workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: mismatched, ExpectedCursor: -1, Limit: 10})
		require.True(t, store.IsNotFound(err), "ListEnvironment(%s) error = %v", name, err)
	}
	wrongGeneration := access
	wrongGeneration.Generation++
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: wrongGeneration})
	requireConflictConstraint(t, err, "attempt_participation_generation")
	unknown := access
	unknown.BindingID = model.NewAttemptEnvironmentBindingID()
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: unknown})
	require.True(t, store.IsNotFound(err), "ReserveObjects(unknown binding) error = %v", err)

	// A newer incarnation replaces the binding; a stale or reused identity
	// does not.
	_, err = workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
		AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("grant-1", "epoch-2", 1)})
	requireConflictConstraint(t, err, "attempt_environment_binding_identity")
	replacement, replacementAccess := bindTestEnvironment(t, ctx, ss, attemptID, generation, environmentIdentity("grant-1", "epoch-2", 2))
	require.NotNil(t, replacement.Replaced)
	require.Equal(t, first.Binding.ID, replacement.Replaced.ID)
	require.False(t, replacement.Replaced.Live())
	requireEnvironmentBindingEnded(t, ctx, ss, access, "replaced by a newer incarnation")
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: replacementAccess})
	require.NoError(t, err)
	_, err = workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
		AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("grant-1", "epoch-1", 3)})
	requireConflictConstraint(t, err, "attempt_environment_binding_identity")

	// The owner ends a binding when it destroys the grant.
	ended, err := workspace.EndEnvironmentBinding(ctx, &store.ExamAttemptEnvironmentBindingEnd{AttemptID: attemptID,
		BindingID: replacementAccess.BindingID})
	require.NoError(t, err)
	require.True(t, ended.Changed)
	require.False(t, ended.Binding.Live())
	requireEnvironmentBindingEnded(t, ctx, ss, replacementAccess, "ended by the owner")
	_, err = workspace.EndEnvironmentBinding(ctx, &store.ExamAttemptEnvironmentBindingEnd{AttemptID: attemptID,
		BindingID: model.NewAttemptEnvironmentBindingID()})
	require.True(t, store.IsNotFound(err), "EndEnvironmentBinding(unknown) error = %v", err)

	// A Kick ends the generation and its binding.
	_, kickedAccess := bindTestEnvironment(t, ctx, ss, attemptID, generation, environmentIdentity("grant-1", "epoch-3", 3))
	kick := &store.ExamAttemptManualSuspension{ExamID: fixture.examID, SittingID: fixture.sitting.ID,
		AttemptID: attemptID, SuspensionID: model.NewAttemptSuspensionID(), ActorUserID: fixture.manager.ID,
		ExpectedRevision: connected.Attempt.Revision, PrivateReason: "candidate left the examination room",
		AuditEventID: saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	kicked, err := ss.ExamAttempt().SuspendAttempt(ctx, kick,
		examCommand(fixture.manager.ID, store.ExamAttemptSuspendOperation, "environment-kick", "environment-kick"))
	require.NoError(t, err)
	requireEnvironmentBindingEnded(t, ctx, ss, kickedAccess, "Kick")
	_, err = workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
		AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("grant-1", "epoch-4", 4)})
	requireConflictConstraint(t, err, "exam_attempt_state")

	// Re-allow creates no Participation, so nothing can be bound until the
	// candidate reconnects, and then only the new generation.
	reallowed, err := ss.ExamAttempt().ReallowAttempt(ctx, &store.ExamAttemptReallow{ExamID: fixture.examID,
		SittingID: fixture.sitting.ID, AttemptID: attemptID, SuspensionID: kick.SuspensionID,
		ActorUserID: fixture.manager.ID, ExpectedRevision: kicked.Attempt.Revision, PrivateReason: "candidate returned",
		AuditEventID: saveExamAttemptReallowAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()},
		examCommand(fixture.manager.ID, store.ExamAttemptReallowOperation, "environment-reallow", "environment-reallow"))
	require.NoError(t, err)
	require.Equal(t, model.ExamAttemptReady, reallowed.Attempt.State)
	_, err = workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
		AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("grant-1", "epoch-4", 4)})
	requireConflictConstraint(t, err, "exam_attempt_state")
	requireEnvironmentBindingEnded(t, ctx, ss, kickedAccess, "re-allow")
	reconnected, _ := connectEnvironmentFixture(t, ctx, ss, fixture, "environment-reconnect")
	require.Equal(t, attemptID, reconnected.Attempt.ID)
	require.Equal(t, generation+1, reconnected.Participation.Generation)
	_, err = workspace.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{BindingID: model.NewAttemptEnvironmentBindingID(),
		AttemptID: attemptID, Generation: generation, Identity: environmentIdentity("grant-1", "epoch-4", 4)})
	requireConflictConstraint(t, err, "attempt_participation_generation")
	_, current := bindTestEnvironment(t, ctx, ss, attemptID, reconnected.Participation.Generation,
		environmentIdentity("grant-1", "epoch-5", 5))
	_, err = workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, current, store.ExamAttemptWorkspaceMutation{
		Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "second-generation"}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "environment-second-generation"))
	require.NoError(t, err)

	// Revoking the candidate's Session fences the generation in PostgreSQL,
	// and the binding ends with it.
	_, err = RevokeAllUserSessions(ctx, ss, fixture.candidate.ID, model.NowUTC())
	require.NoError(t, err)
	requireEnvironmentBindingEnded(t, ctx, ss, current, "Session revocation")
}
