// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestEnvironmentProfileStore checks creating, replaying, refusing and
// retiring environment profiles, and that a Draft and a publication accept
// only an execution policy naming a usable profile.
func TestEnvironmentProfileStore(t *testing.T, stores store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	profiles := stores.EnvironmentProfile()

	bootstrap, err := stores.Installation().Bootstrap(ctx, testInstallationBootstrap(813))
	require.NoError(t, err)
	administrator := bootstrap.Administrator.ID
	institutionID := bootstrap.Institution.ID

	spec := model.EnvironmentProfileSpecification{
		Name: "python-small", Image: "python-3.12",
		Resources:  model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 2 << 30},
		Network:    model.EnvironmentNetworkNone,
		IdleWindow: 10 * time.Minute, MaxTerminals: 2,
	}
	create := func(t *testing.T, spec model.EnvironmentProfileSpecification, actor model.UserID, key string) (*store.EnvironmentProfileCommandResult, error) {
		t.Helper()
		proposed, newErr := model.NewEnvironmentProfile(model.NewEnvironmentProfileID(), spec, actor, model.NowUTC())
		require.NoError(t, newErr)
		input := &store.EnvironmentProfileCreation{Profile: proposed}
		input.AuditEventID, input.AuditAt = saveEnvironmentProfileAudit(t, ctx, stores, institutionID, actor)
		return profiles.Create(ctx, input, environmentProfileCommand(actor, store.EnvironmentProfileCreateOperation, key))
	}

	created, err := create(t, spec, administrator, "first")
	require.NoError(t, err)
	require.True(t, created.Changed)
	require.False(t, created.Replayed)
	require.Equal(t, "python-small", created.Profile.Name)
	require.False(t, created.Profile.Retired())
	require.NoError(t, created.Profile.Validate())

	replayed, err := create(t, spec, administrator, "first")
	require.NoError(t, err)
	require.True(t, replayed.Replayed, "the same key replays the creation")
	require.Equal(t, created.Profile, replayed.Profile)

	_, err = create(t, spec, administrator, "same-name")
	requireConflictConstraint(t, err, "environment_profile_name")

	ordinary, err := stores.User().Create(ctx, testUserCreation(newUser(), nil))
	require.NoError(t, err)
	other := spec
	other.Name = "python-large"
	_, err = create(t, other, ordinary.User.ID, "ordinary")
	requireConflictConstraint(t, err, "actor_not_system_administrator")

	got, err := profiles.Get(ctx, created.Profile.ID)
	require.NoError(t, err)
	require.Equal(t, created.Profile, got)
	_, err = profiles.Get(ctx, model.NewEnvironmentProfileID())
	require.True(t, store.IsNotFound(err))

	// A Draft takes only a profile that exists, is live and allows the
	// terminals.
	unit := saveAcademicUnit(t, ctx, stores, institutionID.String(), "", "environment-profile-unit")
	creator := saveUser(t, ctx, stores)
	exam, err := stores.ExamAuthoring().Create(ctx, newExamAuthoringCreation(t, ctx, stores, unit.ID, creator.ID, model.NowUTC()),
		examCommand(creator.ID, "exam.create", "execution-create", "execution-create"))
	require.NoError(t, err)
	updateExecution := func(t *testing.T, execution model.ExecutionPolicy, revision int64, key string) (*store.ExamAuthoringCommandResult, error) {
		t.Helper()
		focus := newExamDraftFocusLossUpdate(t, ctx, stores, exam.Value.Exam.ID, creator.ID, revision, model.FocusLossPolicy{}, model.NowUTC())
		return stores.ExamAuthoring().UpdateDraftExecution(ctx, &store.ExamDraftExecutionUpdate{
			ExamID: exam.Value.Exam.ID, ActorUserID: creator.ID, ExpectedRevision: revision, Execution: execution,
			UpdatedAt: model.NowUTC(), AuditEventID: focus.AuditEventID, AuditAt: focus.AuditAt,
		}, examCommand(creator.ID, "exam.draft.execution.configure", key, key))
	}
	_, err = updateExecution(t, model.ExecutionPolicy{Enabled: true, Profile: model.NewEnvironmentProfileID(), Terminals: 1}, 1, "missing")
	requireConflictConstraint(t, err, "environment_profile_unusable")
	_, err = updateExecution(t, model.ExecutionPolicy{Enabled: true, Profile: created.Profile.ID, Terminals: 3}, 1, "too-many")
	requireConflictConstraint(t, err, "environment_profile_unusable")
	policy := model.ExecutionPolicy{Enabled: true, Profile: created.Profile.ID, RecordInput: true, Terminals: 2}
	updated, err := updateExecution(t, policy, 1, "usable")
	require.NoError(t, err)
	require.Equal(t, policy, updated.Value.Draft.Policy.Execution)
	require.Equal(t, int64(2), updated.Value.Draft.Revision)

	// Publication refuses while the installation does not allow execution.
	publication := examRevisionPublication(t, ctx, stores, exam.Value.Exam.ID, creator.ID, unit.ID, 2, model.NowUTC())
	_, err = stores.ExamRevision().Publish(ctx, publication, examCommand(creator.ID, "exam.revision.publish", "execution-off", "execution-off"))
	requireConflictConstraint(t, err, "execution_disabled")

	// Retiring is idempotent and keeps the profile readable, and a retired
	// profile cannot be published.
	retire := &store.EnvironmentProfileRetirement{ActorID: administrator, ProfileID: created.Profile.ID}
	retire.AuditEventID, retire.AuditAt = saveEnvironmentProfileAudit(t, ctx, stores, institutionID, administrator)
	retired, err := profiles.Retire(ctx, retire, environmentProfileCommand(administrator, store.EnvironmentProfileRetireOperation, "retire"))
	require.NoError(t, err)
	require.True(t, retired.Changed)
	require.True(t, retired.Profile.Retired())
	again := &store.EnvironmentProfileRetirement{ActorID: administrator, ProfileID: created.Profile.ID}
	again.AuditEventID, again.AuditAt = saveEnvironmentProfileAudit(t, ctx, stores, institutionID, administrator)
	repeated, err := profiles.Retire(ctx, again, environmentProfileCommand(administrator, store.EnvironmentProfileRetireOperation, "retire-again"))
	require.NoError(t, err)
	require.False(t, repeated.Changed, "retiring a retired profile changes nothing")
	require.True(t, repeated.Profile.RetiredAt.Equal(retired.Profile.RetiredAt))

	publication = examRevisionPublication(t, ctx, stores, exam.Value.Exam.ID, creator.ID, unit.ID, 2, model.NowUTC())
	publication.ExecutionEnabled = true
	_, err = stores.ExamRevision().Publish(ctx, publication, examCommand(creator.ID, "exam.revision.publish", "execution-retired", "execution-retired"))
	requireConflictConstraint(t, err, "environment_profile_unusable")

	// The name is free again once its profile is retired.
	successor, err := create(t, spec, administrator, "successor")
	require.NoError(t, err)
	live, err := profiles.List(ctx, false)
	require.NoError(t, err)
	require.Equal(t, []*model.EnvironmentProfile{successor.Profile}, live)
	all, err := profiles.List(ctx, true)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, successor.Profile.ID, all[0].ID, "profiles that are not retired come first")

	_, err = updateExecution(t, model.ExecutionPolicy{Enabled: true, Profile: successor.Profile.ID, Terminals: 1}, 2, "successor")
	require.NoError(t, err)
	publication = examRevisionPublication(t, ctx, stores, exam.Value.Exam.ID, creator.ID, unit.ID, 3, model.NowUTC())
	publication.ExecutionEnabled = true
	published, err := stores.ExamRevision().Publish(ctx, publication, examCommand(creator.ID, "exam.revision.publish", "execution-on", "execution-on"))
	require.NoError(t, err)
	snapshot, err := stores.ExamRevision().GetSnapshot(ctx, exam.Value.Exam.ID, published.Revision.ID)
	require.NoError(t, err)
	frozen, err := model.DecodeExamPolicySet(snapshot.Policy.Bytes)
	require.NoError(t, err)
	require.Equal(t, successor.Profile.ID, frozen.Execution.Profile, "the Revision freezes the profile it names")
}

func environmentProfileCommand(userID model.UserID, operation, identity string) *store.CommandIdempotency {
	return &store.CommandIdempotency{
		UserID:      userID,
		Operation:   operation,
		KeyDigest:   sha256.Sum256([]byte("environment-profile-key:" + identity)),
		Fingerprint: sha256.Sum256([]byte("environment-profile-command:" + identity)),
		Retention:   time.Hour,
		Wait:        time.Second,
	}
}

func saveEnvironmentProfileAudit(
	t *testing.T,
	ctx context.Context,
	stores store.Store,
	institutionID model.InstitutionID,
	actorID model.UserID,
) (model.AuditEventID, time.Time) {
	t.Helper()
	event, err := stores.Audit().Save(ctx, &model.AuditEvent{
		ActorID:   actorID,
		Action:    string(model.ActionEnvironmentProfileManage),
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String(),
		Status: model.AuditStatusAttempt, NodeID: "store-test",
		ClientType: string(model.SessionClientWeb), AuthMethod: "password",
	})
	require.NoError(t, err)
	return event.ID, event.CreatedAt
}
