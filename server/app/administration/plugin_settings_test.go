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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type pluginCatalogFake struct {
	descriptors []PluginDescriptor
	validateErr error
	validated   []string
	changes     int
}

func (c *pluginCatalogFake) Plugins() []PluginDescriptor { return c.descriptors }

func (c *pluginCatalogFake) ValidatePluginSettings(_ context.Context, id model.PluginID, settings json.RawMessage) error {
	c.validated = append(c.validated, id.String()+":"+string(settings))
	return c.validateErr
}

func (c *pluginCatalogFake) PluginSettingsChanged() { c.changes++ }

type pluginSettingsStoreFake struct {
	store.PluginSettingsStore
	rows         []*model.PluginSettings
	replaceInput *store.PluginSettingsReplacement
	command      *store.CommandIdempotency
	result       *store.PluginSettingsReplacementResult
	replaceErr   error
}

func (s *pluginSettingsStoreFake) List(context.Context) ([]*model.PluginSettings, error) {
	return s.rows, nil
}

func (s *pluginSettingsStoreFake) Replace(_ context.Context, input *store.PluginSettingsReplacement, command *store.CommandIdempotency) (*store.PluginSettingsReplacementResult, error) {
	s.replaceInput, s.command = input, command
	return s.result, s.replaceErr
}

type pluginFixture struct {
	service       *Service
	catalog       *pluginCatalogFake
	saved         *pluginSettingsStoreFake
	authorization *resourceAuthorizerFake
	audit         *accessPolicyAuditFake
	publisher     *realtimetest.Publisher
	institution   *model.Institution
	inv           invocation.Invocation
	at            time.Time
}

func newPluginFixture(t *testing.T) pluginFixture {
	t.Helper()
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	institution := &model.Institution{ID: model.NewInstitutionID()}
	catalog := &pluginCatalogFake{descriptors: []PluginDescriptor{
		{ID: "display-topology", Name: "Display topology", State: "disabled", SettingsSchema: json.RawMessage(`{"type":"object"}`)},
		{ID: "export.zip", Name: "Zip export", State: "active", Required: true, EnabledByDefault: true},
	}}
	saved := &pluginSettingsStoreFake{}
	authorization := &resourceAuthorizerFake{}
	audit := &accessPolicyAuditFake{beginID: model.NewAuditEventID().String()}
	publisher := &realtimetest.Publisher{}
	service := &Service{
		institutions: &institutionStoreFake{institution: institution}, pluginSettings: saved, plugins: catalog,
		authorizer: authorization, auditor: audit, publisher: publisher,
		recentAuthenticationTTL: time.Minute, now: func() time.Time { return at },
	}
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: at, MFACompletedAt: model.OptionalTimeFrom(at), ClientType: model.SessionClientWeb}
	return pluginFixture{service: service, catalog: catalog, saved: saved, authorization: authorization, audit: audit, publisher: publisher,
		institution: institution, inv: invocation.New(principal, model.RequestMetadata{RequestID: "plugins"}), at: at}
}

func pluginCommand() ReplacePluginSettingsCommand {
	return ReplacePluginSettingsCommand{PluginID: "display-topology", ExpectedRevision: 0, Enabled: true,
		Settings: json.RawMessage(` {"mode": "observe"} `), IdempotencyKey: "plugin-edit"}
}

func TestListPluginsJoinsDescriptorsWithSavedSettings(t *testing.T) {
	t.Parallel()

	fixture := newPluginFixture(t)
	saved, err := model.NewPluginSettings("display-topology", model.PluginSettingsChange{Enabled: true, Settings: json.RawMessage(`{"mode":"enforce"}`)}, fixture.at)
	require.NoError(t, err)
	fixture.saved.rows = []*model.PluginSettings{saved}

	views, err := fixture.service.ListPlugins(t.Context(), fixture.inv)
	require.NoError(t, err)
	require.Equal(t, model.ActionPluginManage, fixture.authorization.action)
	require.Equal(t, model.Resource{Type: model.ResourceInstitution, ID: fixture.institution.ID.String()}, fixture.authorization.resource)
	require.Equal(t, []PluginView{
		{PluginDescriptor: fixture.catalog.descriptors[0], Enabled: true, Settings: json.RawMessage(`{"mode":"enforce"}`), Revision: 1},
		{PluginDescriptor: fixture.catalog.descriptors[1], Enabled: true, Settings: json.RawMessage(`{}`)},
	}, views, "saved settings win; a plugin nobody configured shows its default")

	fixture.authorization.err = apperror.New("authorization.denied")
	_, err = fixture.service.ListPlugins(t.Context(), fixture.inv)
	require.True(t, apperror.Is(err, "authorization.denied"))
}

func TestReplacePluginSettingsSavesAuditedAndAppliesTheChange(t *testing.T) {
	t.Parallel()

	fixture := newPluginFixture(t)
	saved, err := model.NewPluginSettings("display-topology", model.PluginSettingsChange{Enabled: true, Settings: json.RawMessage(`{"mode":"observe"}`)}, fixture.at)
	require.NoError(t, err)
	fixture.saved.result = &store.PluginSettingsReplacementResult{Settings: saved, Changed: true}

	view, err := fixture.service.ReplacePluginSettings(t.Context(), fixture.inv, pluginCommand())
	require.NoError(t, err)
	require.Equal(t, int64(1), view.Revision)
	require.True(t, view.Enabled)
	require.Equal(t, []string{`display-topology:{"mode":"observe"}`}, fixture.catalog.validated, "the plugin checks the normalized document")
	require.NotNil(t, fixture.saved.replaceInput)
	require.Equal(t, model.PluginSettingsChange{Enabled: true, Settings: json.RawMessage(`{"mode":"observe"}`)}, fixture.saved.replaceInput.Change)
	require.Equal(t, model.PluginID("display-topology"), fixture.saved.replaceInput.PluginID)
	require.Equal(t, fixture.inv.Principal().UserID, fixture.saved.replaceInput.ActorID)
	require.Equal(t, store.PluginSettingsReplaceOperation, fixture.saved.command.Operation)
	require.Equal(t, model.ActionPluginManage, fixture.audit.attempt.Action)
	require.Equal(t, map[string]any{"plugin_id": "display-topology", "expected_revision": int64(0), "enabled": true}, fixture.audit.attempt.Value,
		"the audit names the plugin, never setting values")
	require.Equal(t, 1, fixture.catalog.changes, "this node applies the change at once")
	changed, err := realtime.NewPluginSettingsChangedEvent(fixture.institution.ID, "display-topology", 1)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: changed}}, fixture.publisher.Calls(),
		"administrators' clients are told to refetch")

	fixture.saved.result = &store.PluginSettingsReplacementResult{Settings: saved, Changed: true, Replayed: true}
	_, err = fixture.service.ReplacePluginSettings(t.Context(), fixture.inv, pluginCommand())
	require.NoError(t, err)
	require.Equal(t, 1, fixture.catalog.changes, "a replay applies nothing again")
	require.Len(t, fixture.publisher.Calls(), 1, "a replay publishes nothing again")
}

func TestReplacePluginSettingsRejectsWhatItCannotSave(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		alter func(*pluginFixture, *ReplacePluginSettingsCommand)
		code  string
	}{
		{"unknown plugin", func(_ *pluginFixture, c *ReplacePluginSettingsCommand) { c.PluginID = "missing" }, "plugin.not_found"},
		{"required plugin turned off", func(_ *pluginFixture, c *ReplacePluginSettingsCommand) {
			c.PluginID, c.Enabled = "export.zip", false
		}, "plugin.required"},
		{"settings not an object", func(_ *pluginFixture, c *ReplacePluginSettingsCommand) { c.Settings = json.RawMessage(`[]`) }, "plugin.invalid"},
		{"negative revision", func(_ *pluginFixture, c *ReplacePluginSettingsCommand) { c.ExpectedRevision = -1 }, "request.invalid"},
		{"missing idempotency", func(_ *pluginFixture, c *ReplacePluginSettingsCommand) { c.IdempotencyKey = "" }, "idempotency.key_required"},
		{"refused by the plugin", func(f *pluginFixture, _ *ReplacePluginSettingsCommand) {
			f.catalog.validateErr = errors.New("mode must be observe or enforce")
		}, "plugin.invalid"},
		{"authorization denied", func(f *pluginFixture, _ *ReplacePluginSettingsCommand) {
			f.authorization.err = apperror.New("authorization.denied")
		}, "authorization.denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPluginFixture(t)
			command := pluginCommand()
			test.alter(&fixture, &command)
			_, err := fixture.service.ReplacePluginSettings(t.Context(), fixture.inv, command)
			require.True(t, apperror.Is(err, test.code), "err = %v", err)
			require.Nil(t, fixture.saved.replaceInput, "nothing is saved")
			require.Empty(t, fixture.audit.attempt.Operation, "nothing is audited")
			require.Equal(t, 0, fixture.catalog.changes)
		})
	}
}

func TestReplacePluginSettingsReportsTheCurrentRevisionOnConflict(t *testing.T) {
	t.Parallel()

	fixture := newPluginFixture(t)
	fixture.saved.replaceErr = &store.ErrPluginSettingsRevisionConflict{CurrentRevision: 4}
	_, err := fixture.service.ReplacePluginSettings(t.Context(), fixture.inv, pluginCommand())
	require.True(t, apperror.Is(err, "plugin.revision_conflict"), "err = %v", err)
	var applicationErr *apperror.Error
	require.ErrorAs(t, err, &applicationErr)
	require.Equal(t, "4", applicationErr.Fields()["current_revision"])
	require.Equal(t, 0, fixture.catalog.changes)
}

func TestPluginAdministrationIsUnavailableWithoutPlugins(t *testing.T) {
	t.Parallel()

	fixture := newPluginFixture(t)
	fixture.service.plugins = nil
	_, err := fixture.service.ListPlugins(t.Context(), fixture.inv)
	require.True(t, apperror.Is(err, "plugin.unavailable"))
}
