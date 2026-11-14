// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// fakePluginSections stands in for the plugin host: active plugins accept a
// section whose network is "none" and record every section they are asked
// about, in the order the test shares with its other fakes.
type fakePluginSections struct {
	order  *[]string
	active map[model.PluginID]bool
	asked  []string
}

func (f *fakePluginSections) PluginSectionSchema(id model.PluginID) (json.RawMessage, bool) {
	if !f.active[id] {
		return nil, false
	}
	return json.RawMessage(`{"type":"object","properties":{"network":{"enum":["none"]}}}`), true
}

func (f *fakePluginSections) ValidatePluginSection(_ context.Context, id model.PluginID, section json.RawMessage) error {
	*f.order = append(*f.order, "plugin.validate")
	f.asked = append(f.asked, id.String()+"="+string(section))
	if !f.active[id] {
		return ErrPluginSectionUnavailable
	}
	var fields map[string]any
	if err := json.Unmarshal(section, &fields); err != nil || fields["network"] != "none" {
		return errors.New("the plugin's own reason, never shown")
	}
	return nil
}

func (f *authoringStoreFake) UpdateDraftPluginSection(
	_ context.Context,
	input *store.ExamDraftPluginSectionUpdate,
	command *store.CommandIdempotency,
) (*store.ExamAuthoringCommandResult, error) {
	*f.order = append(*f.order, "store.update_plugin_section")
	f.sectionUpdate, f.idempotency = input, command
	if f.err != nil {
		return nil, f.err
	}
	snapshot, err := f.Get(context.Background(), input.ExamID, input.ActorUserID)
	*f.order = (*f.order)[:len(*f.order)-1]
	if err != nil {
		return nil, err
	}
	if _, err = snapshot.Draft.ApplyPluginSection(input.PluginID, input.Section, model.TimeUTC(input.UpdatedAt)); err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: snapshot, Replayed: f.replayed}, nil
}

func newSectionAuthoring(t *testing.T, f authoringFixture, sections PluginSections) *Authoring {
	t.Helper()
	service, err := NewAuthoring(AuthoringDependencies{
		Persistence:    f.persistence,
		Memberships:    f.memberships,
		Users:          f.users,
		Sittings:       f.sittings,
		Mail:           f.mail,
		Authorizer:     f.authorizer,
		Auditor:        f.auditor,
		Publisher:      f.publisher,
		Now:            func() time.Time { return time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC) },
		NewID:          func() model.ExamID { return f.examID },
		PluginSections: sections,
	})
	require.NoError(t, err)
	return service
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, code, fault.Code())
}

func TestReplaceDraftPluginSectionAsksThePluginAfterAuthorizationAndSavesCanonically(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID, UserID: f.userID}}
	sections := &fakePluginSections{order: f.order, active: map[model.PluginID]bool{"execution": true}}
	service := newSectionAuthoring(t, f, sections)

	view, err := service.ReplaceExamDraftPluginSection(context.Background(), f.inv, ReplaceDraftPluginSectionCommand{
		ExamID: f.examID, PluginID: "execution", ExpectedDraftRevision: 1,
		Section: json.RawMessage(` { "network" : "none" } `), IdempotencyKey: "section-key",
	})
	require.NoError(t, err)
	require.Equal(t, `{"network":"none"}`, string(view.Draft.PluginSections["execution"]))
	require.Equal(t, `{"network":"none"}`, string(f.persistence.sectionUpdate.Section), "the Store receives the canonical section")
	require.Equal(t, []string{"execution={\"network\":\"none\"}"}, sections.asked)
	require.Equal(t, []string{"store.access", "membership", "authorize", "plugin.validate", "store.get", "audit.begin", "store.update_plugin_section", "effect.updated"}, *f.order)
	assertStoreIdempotency(t, f.persistence.idempotency, f.userID, idempotencyOperationReplacePluginSection, "section-key",
		fmt.Sprintf(`{"exam_id":%q,"plugin_id":"execution","expected_draft_revision":1,"section":{"network":"none"}}`, f.examID))
}

func TestReplaceDraftPluginSectionRefusesWhatThePluginDoesNotAccept(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		plugin  model.PluginID
		section string
		code    string
		asked   bool
		order   []string
	}{
		"plugin rejects":        {plugin: "execution", section: `{"network":"open"}`, code: "exam.plugin_section.invalid", asked: true, order: []string{"store.access", "membership", "authorize", "plugin.validate"}},
		"plugin is off":         {plugin: "recording", section: `{"network":"none"}`, code: "exam.plugin_section.plugin_inactive", asked: true, order: []string{"store.access", "membership", "authorize", "plugin.validate"}},
		"not an object":         {plugin: "execution", section: `["network"]`, code: "exam.invalid"},
		"fractional number":     {plugin: "execution", section: `{"cpus":1.5}`, code: "exam.invalid"},
		"invalid plugin ID":     {plugin: "Execution", section: `{}`, code: "exam.invalid"},
		"duplicate member name": {plugin: "execution", section: `{"network":"none","network":"none"}`, code: "exam.invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newAuthoringFixture(t)
			f.persistence.actorIsManager = true
			f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID, UserID: f.userID}}
			sections := &fakePluginSections{order: f.order, active: map[model.PluginID]bool{"execution": true}}
			service := newSectionAuthoring(t, f, sections)
			_, err := service.ReplaceExamDraftPluginSection(context.Background(), f.inv, ReplaceDraftPluginSectionCommand{
				ExamID: f.examID, PluginID: test.plugin, ExpectedDraftRevision: 1,
				Section: json.RawMessage(test.section), IdempotencyKey: "section-key",
			})
			requireCode(t, err, test.code)
			require.Equal(t, test.asked, len(sections.asked) == 1)
			require.Nil(t, f.persistence.sectionUpdate, "a refused section is never saved")
			want := test.order
			if want == nil {
				want = []string{}
			}
			require.Equal(t, want, *f.order, "a refused section opens no audit")
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault)
			for _, value := range fault.Fields() {
				require.NotContains(t, value, "the plugin's own reason", "the plugin's reason stays in the operator-only cause")
			}
		})
	}
}

func TestReplaceDraftPluginSectionRemovesWithoutAskingThePlugin(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.persistence.pluginSections = model.ExamPluginSections{"execution": json.RawMessage(`{"network":"none"}`)}
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID, UserID: f.userID}}
	sections := &fakePluginSections{order: f.order}
	service := newSectionAuthoring(t, f, sections)

	view, err := service.ReplaceExamDraftPluginSection(context.Background(), f.inv, ReplaceDraftPluginSectionCommand{
		ExamID: f.examID, PluginID: "execution", ExpectedDraftRevision: 1, IdempotencyKey: "section-key",
	})
	require.NoError(t, err)
	require.Empty(t, view.Draft.PluginSections)
	require.Empty(t, sections.asked, "removing a section works while its plugin is off")
	require.Nil(t, f.persistence.sectionUpdate.Section)

	_, err = service.ReplaceExamDraftPluginSection(context.Background(), f.inv, ReplaceDraftPluginSectionCommand{
		ExamID: f.examID, PluginID: "absent", ExpectedDraftRevision: 1, IdempotencyKey: "section-key-2",
	})
	requireCode(t, err, "exam.draft.no_changes")
}

func TestGetDraftPluginSectionServesTheSectionAndThePluginsSchema(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.persistence.pluginSections = model.ExamPluginSections{"execution": json.RawMessage(`{"network":"none"}`)}
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID, UserID: f.userID}}
	sections := &fakePluginSections{order: f.order, active: map[model.PluginID]bool{"execution": true}}
	service := newSectionAuthoring(t, f, sections)

	got, err := service.GetExamDraftPluginSection(context.Background(), f.inv, f.examID, "execution")
	require.NoError(t, err)
	require.Equal(t, `{"network":"none"}`, string(got.Section))
	require.Contains(t, string(got.Schema), `"enum":["none"]`)
	require.Empty(t, sections.asked, "reading never asks the plugin to validate")

	sections.active = nil
	got, err = service.GetExamDraftPluginSection(context.Background(), f.inv, f.examID, "execution")
	require.NoError(t, err)
	require.Nil(t, got.Schema, "a plugin that is off serves no schema")
	require.Equal(t, `{"network":"none"}`, string(got.Section), "the saved section is still shown")
}

func newSectionPublication(t *testing.T, f authoringFixture, revisions *revisionStoreFake, sections PluginSections) *Publication {
	t.Helper()
	service, err := NewPublication(PublicationDependencies{
		Persistence:    revisions,
		Access:         f.persistence,
		Memberships:    f.memberships,
		Authorizer:     f.authorizer,
		Auditor:        f.auditor,
		Publisher:      &realtimetest.Publisher{},
		Now:            fixedPublicationTime,
		NewID:          model.NewExamRevisionID,
		PluginSections: sections,
	})
	require.NoError(t, err)
	return service
}

func TestPublicationAsksEverySectionsPluginAgainAndRefusesWhileOneIsOffOrRejects(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		active  map[model.PluginID]bool
		section string
		code    string
	}{
		"accepted":        {active: map[model.PluginID]bool{"execution": true}, section: `{"network":"none"}`},
		"plugin is off":   {active: nil, section: `{"network":"none"}`, code: "exam.plugin_section.plugin_inactive"},
		"plugin rejects":  {active: map[model.PluginID]bool{"execution": true}, section: `{"network":"open"}`, code: "exam.plugin_section.invalid"},
		"no plugin wired": {section: `{"network":"none"}`, code: "exam.plugin_section.plugin_inactive"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newAuthoringFixture(t)
			f.persistence.actorIsManager = true
			f.persistence.draftRevision = 3
			f.persistence.pluginSections = model.ExamPluginSections{"execution": json.RawMessage(test.section)}
			f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
			revisions := &revisionStoreFake{}
			var sections PluginSections
			fake := &fakePluginSections{order: f.order, active: test.active}
			if name != "no plugin wired" {
				sections = fake
			}
			service := newSectionPublication(t, f, revisions, sections)
			_, err := service.PublishExamRevision(context.Background(), f.inv,
				PublishRevisionCommand{ExamID: f.examID, ExpectedDraftRevision: 3, IdempotencyKey: "publish-key"})
			if test.code == "" {
				require.NoError(t, err)
				require.NotNil(t, revisions.input)
				require.Equal(t, []string{`execution={"network":"none"}`}, fake.asked)
				return
			}
			requireCode(t, err, test.code)
			require.Nil(t, revisions.input, "a refused publication freezes nothing")
			want := []string{"store.access", "membership", "authorize", "store.get", "plugin.validate"}
			if sections == nil {
				want = want[:4]
			}
			require.Equal(t, want, *f.order, "a refused publication opens no audit")
		})
	}
}

func TestPublicationOfAStaleDraftLeavesTheDecisionToTheStore(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.persistence.draftRevision = 4
	f.persistence.pluginSections = model.ExamPluginSections{"execution": json.RawMessage(`{"network":"none"}`)}
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	revisions := &revisionStoreFake{replayed: true}
	sections := &fakePluginSections{order: f.order}
	service := newSectionPublication(t, f, revisions, sections)

	// The Draft moved on because this publication already committed; the
	// replay returns the recorded Revision although the plugin is now off.
	_, err := service.PublishExamRevision(context.Background(), f.inv,
		PublishRevisionCommand{ExamID: f.examID, ExpectedDraftRevision: 3, IdempotencyKey: "publish-key"})
	require.NoError(t, err)
	require.Empty(t, sections.asked)
	require.NotNil(t, revisions.input)
}
