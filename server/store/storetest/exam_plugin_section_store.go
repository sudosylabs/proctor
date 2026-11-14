// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestExamPluginSectionStore checks plugin sections through authoring and
// publication: a canonical save, its replay, a no-op, a stale revision, a
// removal, and that publication freezes the sections into the Revision, its
// digests and every Revision read, untouched by later Draft edits.
func TestExamPluginSectionStore(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "section-unit")
	creator := saveUser(t, ctx, ss)
	at := model.NowUTC()
	created := createCatalogExam(t, ctx, ss, unit.ID, creator.ID, at, "section-exam")
	examID := created.Value.Exam.ID
	require.Empty(t, created.Value.Draft.PluginSections, "a new Draft has no sections")

	set := newExamDraftPluginSectionUpdate(t, ctx, ss, examID, creator.ID, 1, "execution",
		json.RawMessage(` { "network": "none", "image": "go-1.25" } `), at.Add(time.Second))
	setCommand := examCommand(creator.ID, "exam.draft.plugin_section.replace", "section-set", "section-set-command")
	first, err := ss.ExamAuthoring().UpdateDraftPluginSection(ctx, set, setCommand)
	require.NoError(t, err)
	require.True(t, first.ConsistentWithDraft(), "UpdateDraftPluginSection returned a result inconsistent with its request")
	require.False(t, first.Replayed)
	require.Equal(t, int64(2), first.Value.Draft.Revision)
	require.Equal(t, `{"image":"go-1.25","network":"none"}`, string(first.Value.Draft.PluginSections["execution"]))
	completed, err := ss.Audit().Get(ctx, set.AuditEventID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, completed.Status)
	require.NotContains(t, string(completed.Result), "go-1.25", "the audit names the plugin, not its section")

	replayInput := newExamDraftPluginSectionUpdate(t, ctx, ss, examID, creator.ID, 1, "execution", set.Section, at.Add(2*time.Second))
	replayed, err := ss.ExamAuthoring().UpdateDraftPluginSection(ctx, replayInput, setCommand)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, int64(2), replayed.Value.Draft.Revision)
	require.Equal(t, first.Value.Draft.PluginSections, replayed.Value.Draft.PluginSections)

	noOp := newExamDraftPluginSectionUpdate(t, ctx, ss, examID, creator.ID, 2, "execution",
		json.RawMessage(`{"image":"go-1.25","network":"none"}`), at.Add(3*time.Second))
	_, err = ss.ExamAuthoring().UpdateDraftPluginSection(ctx, noOp,
		examCommand(creator.ID, "exam.draft.plugin_section.replace", "section-no-op", "section-no-op-command"))
	requireConflictConstraint(t, err, "exam_draft_no_changes")

	stale := newExamDraftPluginSectionUpdate(t, ctx, ss, examID, creator.ID, 1, "display", json.RawMessage(`{"screens":1}`), at.Add(3*time.Second))
	_, err = ss.ExamAuthoring().UpdateDraftPluginSection(ctx, stale,
		examCommand(creator.ID, "exam.draft.plugin_section.replace", "section-stale", "section-stale-command"))
	requireConflictConstraint(t, err, "exam_draft_revision")

	second := newExamDraftPluginSectionUpdate(t, ctx, ss, examID, creator.ID, 2, "display", json.RawMessage(`{"screens":1}`), at.Add(4*time.Second))
	_, err = ss.ExamAuthoring().UpdateDraftPluginSection(ctx, second,
		examCommand(creator.ID, "exam.draft.plugin_section.replace", "section-second", "section-second-command"))
	require.NoError(t, err)
	reloaded, err := ss.ExamAuthoring().Get(ctx, examID, creator.ID)
	require.NoError(t, err)
	require.Equal(t, []model.PluginID{"display", "execution"}, reloaded.Draft.PluginSections.PluginIDs(), "sections survive a reload")
	require.Equal(t, `{"image":"go-1.25","network":"none"}`, string(reloaded.Draft.PluginSections["execution"]), "a reload restores the canonical spelling")

	publication := examRevisionPublication(t, ctx, ss, examID, creator.ID, unit.ID, 3, at.Add(5*time.Second))
	published, err := ss.ExamRevision().Publish(ctx, publication,
		examCommand(creator.ID, "exam.revision.publish", "section-publish", "section-publish-command"))
	require.NoError(t, err)
	revisionID := published.Revision.ID
	require.Equal(t, reloaded.Draft.PluginSections, published.Revision.PluginSections, "publication freezes the Draft's sections")
	digest, err := model.ExamPluginSectionsDigest(reloaded.Draft.PluginSections)
	require.NoError(t, err)
	require.Equal(t, digest, published.Revision.PluginSectionsDigest)

	snapshot, err := ss.ExamRevision().GetSnapshot(ctx, examID, revisionID)
	require.NoError(t, err)
	require.Equal(t, reloaded.Draft.PluginSections, snapshot.PluginSections)
	require.Equal(t, digest, snapshot.PluginSectionsDigest)
	listed, err := ss.ExamRevision().List(ctx, store.ExamRevisionListOptions{ExamID: examID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, reloaded.Draft.PluginSections, listed[0].PluginSections)
	found, ok, err := ss.ExamRevision().FindPluginSections(ctx, revisionID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, reloaded.Draft.PluginSections, found)
	_, ok, err = ss.ExamRevision().FindPluginSections(ctx, model.NewExamRevisionID())
	require.NoError(t, err)
	require.False(t, ok, "an unknown Revision has no sections")

	// Removing a section from the Draft afterwards changes nothing frozen,
	// and republishing with a section removed changes the content digest.
	removal := newExamDraftPluginSectionUpdate(t, ctx, ss, examID, creator.ID, 4, "display", nil, at.Add(6*time.Second))
	removed, err := ss.ExamAuthoring().UpdateDraftPluginSection(ctx, removal,
		examCommand(creator.ID, "exam.draft.plugin_section.replace", "section-remove", "section-remove-command"))
	require.NoError(t, err)
	require.Equal(t, []model.PluginID{"execution"}, removed.Value.Draft.PluginSections.PluginIDs())
	frozen, err := ss.ExamRevision().GetSnapshot(ctx, examID, revisionID)
	require.NoError(t, err)
	require.Equal(t, snapshot.PluginSections, frozen.PluginSections)
	republished, err := ss.ExamRevision().Publish(ctx, examRevisionPublication(t, ctx, ss, examID, creator.ID, unit.ID, 5, at.Add(7*time.Second)),
		examCommand(creator.ID, "exam.revision.publish", "section-republish", "section-republish-command"))
	require.NoError(t, err, "removing a section is a change worth publishing")
	require.NotEqual(t, published.Revision.ContentDigest, republished.Revision.ContentDigest)
	require.Equal(t, []model.PluginID{"execution"}, republished.Revision.PluginSections.PluginIDs())
}

// TestPluginSectionInUse checks that turning a plugin off is refused while a
// Sitting that is not Closed or Canceled delivers a Revision with its
// section, and allowed again once that Sitting is canceled. It needs a
// pristine store, because it saves plugin settings as the bootstrap System
// Administrator.
func TestPluginSectionInUse(t *testing.T, ss store.Store) {
	ctx := context.Background()
	bootstrap, err := ss.Installation().Bootstrap(ctx, testInstallationBootstrap(813))
	require.NoError(t, err)
	administrator := bootstrap.Administrator.ID
	fixture := newExamSittingFixture(t, ctx, ss)

	set := newExamDraftPluginSectionUpdate(t, ctx, ss, fixture.examID, fixture.actor.ID, 2, "execution",
		json.RawMessage(`{"network":"none"}`), model.NowUTC())
	_, err = ss.ExamAuthoring().UpdateDraftPluginSection(ctx, set,
		examCommand(fixture.actor.ID, "exam.draft.plugin_section.replace", "in-use-section", "in-use-section-command"))
	require.NoError(t, err)
	published, err := ss.ExamRevision().Publish(ctx,
		examRevisionPublication(t, ctx, ss, fixture.examID, fixture.actor.ID, fixture.unitID, 3, model.NowUTC()),
		examCommand(fixture.actor.ID, "exam.revision.publish", "in-use-publish", "in-use-publish-command"))
	require.NoError(t, err)

	disable := func(id model.PluginID, identity string) error {
		replacement := &store.PluginSettingsReplacement{
			ActorID: administrator, PluginID: id, ExpectedRevision: 0,
			Change: model.PluginSettingsChange{Enabled: false, Settings: json.RawMessage(`{}`)},
		}
		preparePluginSettingsAttempt(t, ctx, ss, bootstrap.Institution.ID, administrator, replacement)
		_, replaceErr := ss.PluginSettings().Replace(ctx, replacement, pluginSettingsCommand(administrator, identity))
		return replaceErr
	}

	// A Revision with the section that no Sitting delivers holds nothing.
	require.NoError(t, disable("unrelated", "in-use-unrelated"))

	start := fixture.period.StartsAt.Add(time.Hour)
	sitting, err := model.NewExamSitting(model.NewExamSittingID(), fixture.examID, published.Revision.ID, fixture.class.ID,
		start, start.Add(2*time.Hour), model.NowUTC())
	require.NoError(t, err)
	openJob, deadlineJob := newExamSittingLifecycleJobs(t, sitting.ID, 1, sitting.ScheduledStartAt, sitting.ScheduledEndAt)
	_, err = ss.ExamSitting().Schedule(ctx, &store.ExamSittingSchedule{Sitting: sitting, OpenJob: openJob, DeadlineJob: deadlineJob,
		ActorUserID: fixture.actor.ID, AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.actor.ID, fixture.examID, fixture.unitID).ID,
		AuditAt: model.NowUTC(), Mail: newExamSittingMailFanout(t, fixture.actor.ID, store.ExamSittingMailScheduled, model.MailTemplateExamSittingScheduled)},
		examCommand(fixture.actor.ID, "exam.sitting.schedule", "in-use-schedule", "in-use-schedule-command"))
	require.NoError(t, err)

	err = disable("execution", "in-use-refused")
	requireConflictConstraint(t, err, "plugin_in_use")
	saved, err := ss.PluginSettings().List(ctx)
	require.NoError(t, err)
	for _, row := range saved {
		require.NotEqual(t, model.PluginID("execution"), row.PluginID, "a refused change saves nothing")
	}
	require.NoError(t, disable("display", "in-use-other-plugin"), "another plugin's section is not in use")

	_, err = ss.ExamSitting().Cancel(ctx, &store.ExamSittingCancellation{ExamID: fixture.examID, SittingID: sitting.ID,
		ActorUserID: fixture.actor.ID, ExpectedRevision: 1, PrivateReason: "release",
		AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.actor.ID, fixture.examID, fixture.unitID).ID, AuditAt: model.NowUTC(),
		Mail: newExamSittingMailFanout(t, fixture.actor.ID, store.ExamSittingMailCanceled, model.MailTemplateExamSittingCanceled)},
		examCommand(fixture.actor.ID, "exam.sitting.cancel", "in-use-cancel", "in-use-cancel-command"))
	require.NoError(t, err)
	require.NoError(t, disable("execution", "in-use-released"), "a canceled Sitting no longer holds the plugin")
}

// TestExamPluginSectionAdmission checks that a Revision with a frozen plugin
// section admits a candidate with no plugin anywhere: the plugin is not
// compiled into a store test, has no saved settings, and cannot be turned
// off while the Open Sitting delivers the Revision. Admission reads the
// frozen section and nothing else. It needs a pristine store, because it
// saves plugin settings as the bootstrap System Administrator.
func TestExamPluginSectionAdmission(t *testing.T, ss store.Store) {
	bootstrap, err := ss.Installation().Bootstrap(context.Background(), testInstallationBootstrap(814))
	require.NoError(t, err)
	sections := model.ExamPluginSections{"execution": json.RawMessage(`{"network":"none"}`)}
	ctx := context.WithValue(context.Background(), fixturePluginSectionsKey{}, sections)
	fixture := newExamAttemptFixture(t, ctx, ss)
	frozen, found, err := ss.ExamRevision().FindPluginSections(ctx, fixture.revisionID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, sections, frozen)

	// An Open Sitting holds the plugin as a Scheduled one does.
	replacement := &store.PluginSettingsReplacement{
		ActorID: bootstrap.Administrator.ID, PluginID: "execution", ExpectedRevision: 0,
		Change: model.PluginSettingsChange{Enabled: false, Settings: json.RawMessage(`{}`)},
	}
	preparePluginSettingsAttempt(t, ctx, ss, bootstrap.Institution.ID, bootstrap.Administrator.ID, replacement)
	_, err = ss.PluginSettings().Replace(ctx, replacement, pluginSettingsCommand(bootstrap.Administrator.ID, "admission-off"))
	requireConflictConstraint(t, err, "plugin_in_use")

	credential := model.HashToken(model.NewCredentialToken())
	input := &store.ExamAttemptConnect{
		SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(),
		WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(),
		ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: credential,
		AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
	}
	prepareExamAttemptConnect(t, ctx, ss, input)
	connected, err := ss.ExamAttempt().Connect(ctx, input,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "section-admission", "section-admission"))
	require.NoError(t, err, "admission reads the frozen section and asks no plugin")
	require.True(t, connected.FirstAdmission)
	require.Equal(t, fixture.revisionID, connected.Attempt.AdmissionRevisionID)
}

// fixturePluginSectionsKey carries, in a fixture's context, the plugin
// sections newExamAttemptFixture sets on the Draft before it publishes.
type fixturePluginSectionsKey struct{}

// setFixturePluginSections sets the sections the context carries on the
// Draft and returns the Draft's new revision; without any it changes
// nothing.
func setFixturePluginSections(
	t *testing.T,
	ctx context.Context,
	ss store.Store,
	examID model.ExamID,
	actorID model.UserID,
	draftRevision int64,
	at time.Time,
) int64 {
	t.Helper()
	sections, _ := ctx.Value(fixturePluginSectionsKey{}).(model.ExamPluginSections)
	for _, id := range sections.PluginIDs() {
		update := newExamDraftPluginSectionUpdate(t, ctx, ss, examID, actorID, draftRevision, id, sections[id], at)
		updated, err := ss.ExamAuthoring().UpdateDraftPluginSection(ctx, update,
			examCommand(actorID, "exam.draft.plugin_section.replace", "fixture-section-"+id.String(), "fixture-section-"+id.String()))
		require.NoError(t, err)
		draftRevision = updated.Value.Draft.Revision
	}
	return draftRevision
}

func newExamDraftPluginSectionUpdate(
	t *testing.T,
	ctx context.Context,
	ss store.Store,
	examID model.ExamID,
	actorID model.UserID,
	expectedRevision int64,
	pluginID model.PluginID,
	section json.RawMessage,
	at time.Time,
) *store.ExamDraftPluginSectionUpdate {
	t.Helper()
	base := newExamDraftTextUpdate(t, ctx, ss, examID, actorID, expectedRevision, nil, nil, at)
	return &store.ExamDraftPluginSectionUpdate{
		ExamID: examID, ActorUserID: actorID, ExpectedRevision: expectedRevision, PluginID: pluginID, Section: section,
		UpdatedAt: at, AuditEventID: base.AuditEventID, AuditAt: at,
	}
}
