// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

type sectionPlugin struct{ id string }

func (p sectionPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: "Section " + p.id, EnabledByDefault: true}
}
func (p sectionPlugin) Activate(_ context.Context, host plugin.Host) error {
	return host.AddExamSection(p)
}
func (sectionPlugin) Deactivate(context.Context) error { return nil }
func (sectionPlugin) Schema() json.RawMessage          { return json.RawMessage(`{"type":"object"}`) }
func (sectionPlugin) ValidateSection(_ context.Context, section json.RawMessage) error {
	if string(section) != `{"network":"none"}` {
		return errors.New("refused")
	}
	return nil
}

func TestPluginSectionsAdaptTheHostForExamAuthoring(t *testing.T) {
	t.Parallel()
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{sectionPlugin{id: "execution"}},
		Logger:  slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	sections := pluginSections{host: host}
	ctx := context.Background()

	schema, ok := sections.PluginSectionSchema("execution")
	require.True(t, ok)
	require.JSONEq(t, `{"type":"object"}`, string(schema))
	require.NoError(t, sections.ValidatePluginSection(ctx, "execution", json.RawMessage(`{"network":"none"}`)))
	err = sections.ValidatePluginSection(ctx, "execution", json.RawMessage(`{"network":"open"}`))
	require.Error(t, err)
	require.NotErrorIs(t, err, exam.ErrPluginSectionUnavailable, "a refusal is not an inactive plugin")
	require.ErrorIs(t, sections.ValidatePluginSection(ctx, "recording", json.RawMessage(`{}`)), exam.ErrPluginSectionUnavailable)

	host.Reconcile(ctx, map[string]pluginhost.Settings{"execution": {Enable: new(bool)}})
	require.ErrorIs(t, sections.ValidatePluginSection(ctx, "execution", json.RawMessage(`{"network":"none"}`)), exam.ErrPluginSectionUnavailable,
		"a plugin turned off offers no section")
	_, ok = sections.PluginSectionSchema("execution")
	require.False(t, ok)
	require.ErrorIs(t, pluginSections{}.ValidatePluginSection(ctx, "execution", nil), exam.ErrPluginSectionUnavailable)
}

type sectionRevisionStore struct {
	store.ExamRevisionStore
	sections model.ExamPluginSections
}

func (s sectionRevisionStore) FindPluginSections(_ context.Context, id model.ExamRevisionID) (model.ExamPluginSections, bool, error) {
	if s.sections == nil {
		return nil, false, nil
	}
	return s.sections.Clone(), true, nil
}

func TestStoreExamSectionsAnswerOnlyTheCallingPluginsSection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	revisionID := model.NewExamRevisionID().String()
	adapter := newStoreExamSections(sectionRevisionStore{sections: model.ExamPluginSections{
		"execution": json.RawMessage(`{"network":"none"}`),
		"display":   json.RawMessage(`{"screens":1}`),
	}})

	section, err := adapter.RevisionSection(ctx, "execution", revisionID)
	require.NoError(t, err)
	require.Equal(t, `{"network":"none"}`, string(section))
	_, err = adapter.RevisionSection(ctx, "recording", revisionID)
	require.ErrorIs(t, err, plugin.ErrExamSectionNotFound)
	_, err = adapter.RevisionSection(ctx, "execution", "not-an-id")
	require.ErrorIs(t, err, plugin.ErrExamSectionNotFound)
	_, err = newStoreExamSections(sectionRevisionStore{}).RevisionSection(ctx, "execution", revisionID)
	require.ErrorIs(t, err, plugin.ErrExamSectionNotFound, "an unknown Revision has no section")
	require.Nil(t, newStoreExamSections(nil))
}
