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
	"strconv"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// PluginCatalog is what the plugin administration use cases need from the
// plugins compiled into the server. The composition root adapts the plugin
// host to it.
type PluginCatalog interface {
	// Plugins describes every compiled-in plugin, in activation order.
	Plugins() []PluginDescriptor
	// ValidatePluginSettings asks the plugin whether settings are
	// acceptable. An error refuses them.
	ValidatePluginSettings(ctx context.Context, id model.PluginID, settings json.RawMessage) error
	// PluginSettingsChanged asks this node to apply saved settings now
	// rather than at its next check. It does not wait.
	PluginSettingsChanged()
}

// PluginDescriptor describes one compiled-in plugin.
type PluginDescriptor struct {
	ID               model.PluginID
	Name             string
	State            string
	Required         bool
	EnabledByDefault bool
	// SettingsSchema is the plugin's JSON Schema for its settings, or nil.
	SettingsSchema json.RawMessage
}

// PluginView is one plugin as an administrator sees it: what it is, its
// state on this node, and the settings saved for it.
type PluginView struct {
	PluginDescriptor
	// Enabled is whether the plugin should run: the saved choice, or its
	// default when nothing is saved. A required plugin is always enabled.
	Enabled bool
	// Settings is the saved settings document, or an empty object.
	Settings json.RawMessage
	// Revision is the saved settings revision, 0 when nothing is saved.
	Revision int64
}

// ReplacePluginSettingsCommand saves one plugin's settings behind
// optimistic revision and idempotency fences. ExpectedRevision is 0 when
// nothing is saved yet.
type ReplacePluginSettingsCommand struct {
	PluginID         model.PluginID
	ExpectedRevision int64
	Enabled          bool
	Settings         json.RawMessage
	IdempotencyKey   string
}

// ListPlugins returns every compiled-in plugin with its saved settings.
func (s *Service) ListPlugins(ctx context.Context, inv invocation.Invocation) ([]PluginView, error) {
	if _, _, err := s.authorizePlugins(ctx, inv); err != nil {
		return nil, err
	}
	saved, err := s.savedPluginSettings(ctx)
	if err != nil {
		return nil, err
	}
	descriptors := s.plugins.Plugins()
	views := make([]PluginView, 0, len(descriptors))
	for _, descriptor := range descriptors {
		views = append(views, pluginView(descriptor, saved[descriptor.ID]))
	}
	return views, nil
}

// ReplacePluginSettings saves one plugin's settings and has them applied on
// every node.
func (s *Service) ReplacePluginSettings(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplacePluginSettingsCommand,
) (PluginView, error) {
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return PluginView{}, err
	}
	institution, resource, err := s.authorizePlugins(ctx, inv)
	if err != nil {
		return PluginView{}, err
	}
	descriptor, known := s.pluginDescriptor(command.PluginID)
	if !known {
		return PluginView{}, apperror.New("plugin.not_found")
	}
	if descriptor.Required && !command.Enabled {
		return PluginView{}, apperror.New("plugin.required")
	}
	if command.ExpectedRevision < 0 {
		return PluginView{}, apperror.New("request.invalid").WithField("field", "expected_revision")
	}
	settings, err := model.NormalizePluginSettingsDocument(command.Settings)
	if err != nil {
		return PluginView{}, apperror.Invalid("plugin.invalid", "settings")
	}
	if err = s.plugins.ValidatePluginSettings(ctx, command.PluginID, settings); err != nil {
		// The plugin's reason is its own; the administrator learns only that
		// the settings were refused.
		return PluginView{}, apperror.Invalid("plugin.invalid", "settings").Wrap(err)
	}

	change := model.PluginSettingsChange{Enabled: command.Enabled, Settings: settings}
	prepared, err := idempotency.Require(inv, store.PluginSettingsReplaceOperation, command.IdempotencyKey, struct {
		PluginID         model.PluginID  `json:"plugin_id"`
		ExpectedRevision int64           `json:"expected_revision"`
		Enabled          bool            `json:"enabled"`
		Settings         json.RawMessage `json:"settings"`
	}{command.PluginID, command.ExpectedRevision, command.Enabled, settings})
	if err != nil {
		return PluginView{}, err
	}

	stored, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionPluginManage,
			Resource:   resource,
			ScopeType:  model.RoleScopeInstitution,
			ScopeID:    institution.ID.String(),
			Operation:  "replace",
			Value: map[string]any{
				"plugin_id":         command.PluginID.String(),
				"expected_revision": command.ExpectedRevision,
				"enabled":           command.Enabled,
			},
			At: s.now(),
		},
		func(ctx context.Context, reference access.MutationReference) (*store.PluginSettingsReplacementResult, error) {
			return s.pluginSettings.Replace(ctx, &store.PluginSettingsReplacement{
				ActorID:          inv.Principal().UserID,
				PluginID:         command.PluginID,
				ExpectedRevision: command.ExpectedRevision,
				Change:           change,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			}, prepared)
		},
		pluginSettingsError,
	)
	if err != nil {
		return PluginView{}, err
	}
	if stored.Changed && !stored.Replayed {
		s.plugins.PluginSettingsChanged()
		s.publishPluginSettingsChanged(ctx, institution.ID, stored.Settings)
	}
	return pluginView(descriptor, stored.Settings), nil
}

// publishPluginSettingsChanged tells administrators' clients to refetch. A
// failure is reported, not returned: the change is already saved.
func (s *Service) publishPluginSettingsChanged(ctx context.Context, institutionID model.InstitutionID, settings *model.PluginSettings) {
	event, err := realtime.NewPluginSettingsChangedEvent(institutionID, settings.PluginID, settings.Revision)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, "plugin_settings_changed", err)
	}
}

// authorizePlugins authorizes plugin administration on the Institution.
func (s *Service) authorizePlugins(ctx context.Context, inv invocation.Invocation) (*model.Institution, model.Resource, error) {
	if s.plugins == nil || s.pluginSettings == nil {
		return nil, model.Resource{}, apperror.New("plugin.unavailable")
	}
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return nil, model.Resource{}, pluginSettingsStoreErrors.Translate(err)
	}
	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err = s.authorizer.Authorize(ctx, inv, model.ActionPluginManage, resource); err != nil {
		return nil, model.Resource{}, err
	}
	return institution, resource, nil
}

func (s *Service) savedPluginSettings(ctx context.Context) (map[model.PluginID]*model.PluginSettings, error) {
	rows, err := s.pluginSettings.List(ctx)
	if err != nil {
		return nil, pluginSettingsStoreErrors.Translate(err)
	}
	saved := make(map[model.PluginID]*model.PluginSettings, len(rows))
	for _, row := range rows {
		saved[row.PluginID] = row
	}
	return saved, nil
}

func (s *Service) pluginDescriptor(id model.PluginID) (PluginDescriptor, bool) {
	for _, descriptor := range s.plugins.Plugins() {
		if descriptor.ID == id {
			return descriptor, true
		}
	}
	return PluginDescriptor{}, false
}

func pluginView(descriptor PluginDescriptor, saved *model.PluginSettings) PluginView {
	view := PluginView{PluginDescriptor: descriptor, Enabled: descriptor.EnabledByDefault, Settings: json.RawMessage(`{}`)}
	if saved != nil {
		view.Enabled = saved.Enabled
		view.Settings = saved.Settings
		view.Revision = saved.Revision
	}
	if descriptor.Required {
		view.Enabled = true
	}
	return view
}

func pluginSettingsError(err error) error {
	var revision *store.ErrPluginSettingsRevisionConflict
	if errors.As(err, &revision) {
		return apperror.New("plugin.revision_conflict").WithField(
			"current_revision",
			strconv.FormatInt(revision.CurrentRevision, 10),
		).Wrap(err)
	}
	return pluginSettingsStoreErrors.Translate(err)
}

var pluginSettingsStoreErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{"actor_not_system_administrator": "authorization.denied"},
	Unavailable: "plugin.unavailable",
}
