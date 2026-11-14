// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

type fakeExamSection struct {
	schema json.RawMessage
	panics bool
	hang   bool
	seen   *[]string
}

func (s fakeExamSection) Schema() json.RawMessage { return s.schema }

func (s fakeExamSection) ValidateSection(ctx context.Context, section json.RawMessage) error {
	if s.seen != nil {
		*s.seen = append(*s.seen, string(section))
	}
	if s.panics {
		panic("section validation exploded")
	}
	if s.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	var fields map[string]any
	if err := json.Unmarshal(section, &fields); err != nil || fields["network"] != "none" {
		return errors.New("only network none is offered")
	}
	return nil
}

// sectionStore answers every Revision with one section per plugin.
type sectionStore struct{ asked []string }

func (s *sectionStore) RevisionSection(_ context.Context, pluginID, revisionID string) (json.RawMessage, error) {
	s.asked = append(s.asked, pluginID+"@"+revisionID)
	if revisionID == "missing" {
		return nil, plugin.ErrExamSectionNotFound
	}
	return json.RawMessage(`{"plugin":"` + pluginID + `"}`), nil
}

func TestExamSectionsAreOfferedOncePerActivePluginAndValidatedUnderGuard(t *testing.T) {
	t.Parallel()

	var seen []string
	schema := json.RawMessage(`{"type":"object"}`)
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Logger:            discardLogger(),
		ActivationTimeout: 200 * time.Millisecond,
		Plugins: []plugin.Plugin{
			offeringPlugin{id: "execution", activate: func(h plugin.Host) error {
				return h.AddExamSection(fakeExamSection{schema: schema, seen: &seen})
			}},
			offeringPlugin{id: "twice", activate: func(h plugin.Host) error {
				if err := h.AddExamSection(fakeExamSection{schema: schema}); err != nil {
					return err
				}
				return h.AddExamSection(fakeExamSection{schema: schema})
			}},
			offeringPlugin{id: "array-schema", activate: func(h plugin.Host) error {
				return h.AddExamSection(fakeExamSection{schema: json.RawMessage(`[]`)})
			}},
			offeringPlugin{id: "broken", activate: func(h plugin.Host) error {
				return h.AddExamSection(fakeExamSection{schema: schema, panics: true})
			}},
			offeringPlugin{id: "slow", activate: func(h plugin.Host) error {
				return h.AddExamSection(fakeExamSection{schema: schema, hang: true})
			}},
			offeringPlugin{id: "none", activate: func(plugin.Host) error { return nil }},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	got, ok := host.ExamSectionSchema("execution")
	require.True(t, ok)
	require.JSONEq(t, string(schema), string(got), "the schema is served unchanged")
	_, ok = host.ExamSectionSchema("twice")
	require.False(t, ok, "a second section fails the plugin and discards what it offered")
	_, ok = host.ExamSectionSchema("array-schema")
	require.False(t, ok, "a schema must be a JSON object")
	_, ok = host.ExamSectionSchema("none")
	require.False(t, ok)

	ctx := context.Background()
	require.NoError(t, host.ValidateExamSection(ctx, "execution", json.RawMessage(`{"network":"none"}`)))
	require.Error(t, host.ValidateExamSection(ctx, "execution", json.RawMessage(`{"network":"open"}`)))
	require.Equal(t, []string{`{"network":"none"}`, `{"network":"open"}`}, seen)
	require.ErrorIs(t, host.ValidateExamSection(ctx, "none", json.RawMessage(`{}`)), pluginhost.ErrNoExamSection)
	require.ErrorIs(t, host.ValidateExamSection(ctx, "unknown", json.RawMessage(`{}`)), pluginhost.ErrNoExamSection)
	require.ErrorIs(t, host.ValidateExamSection(ctx, "slow", json.RawMessage(`{}`)), pluginhost.ErrTimedOut)

	for range pluginhost.PanicLimit {
		require.ErrorIs(t, host.ValidateExamSection(ctx, "broken", json.RawMessage(`{}`)), plugin.ErrUnavailable)
	}
	require.ErrorIs(t, host.ValidateExamSection(ctx, "broken", json.RawMessage(`{}`)), pluginhost.ErrNoExamSection,
		"a plugin failed by its panics offers no section")
}

func TestExamSectionsReadOnlyTheCallingPluginsSectionWhileActive(t *testing.T) {
	t.Parallel()

	store := &sectionStore{}
	var sections plugin.ExamSections
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Logger:       discardLogger(),
		ExamSections: store,
		Plugins: []plugin.Plugin{offeringPlugin{id: "execution", activate: func(h plugin.Host) error {
			sections = h.ExamSections()
			return nil
		}}},
	})
	require.NoError(t, err)

	section, err := sections.Get(context.Background(), "rev1")
	require.NoError(t, err)
	require.JSONEq(t, `{"plugin":"execution"}`, string(section))
	_, err = sections.Get(context.Background(), "missing")
	require.ErrorIs(t, err, plugin.ErrExamSectionNotFound)
	require.Equal(t, []string{"execution@rev1", "execution@missing"}, store.asked)

	require.NoError(t, host.Close(context.Background()))
	_, err = sections.Get(context.Background(), "rev1")
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a deactivated plugin reads nothing")
}
