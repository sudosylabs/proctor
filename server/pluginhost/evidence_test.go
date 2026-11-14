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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

type recordedEvidence struct {
	kind        string
	environment plugin.BoundEnvironment
	record      plugin.EvidenceRecord
}

type evidenceStore struct{ appended []recordedEvidence }

func (s *evidenceStore) Append(_ context.Context, policyKind string, environment plugin.BoundEnvironment,
	record plugin.EvidenceRecord,
) (plugin.EvidenceReceipt, error) {
	s.appended = append(s.appended, recordedEvidence{kind: policyKind, environment: environment, record: record})
	return plugin.EvidenceReceipt{ID: "stored", RecordedAt: time.Unix(1, 0).UTC()}, nil
}

func TestEvidenceIsAttributedToThePluginsOwnKind(t *testing.T) {
	t.Parallel()

	store := &evidenceStore{}
	var granted, other plugin.Evidence
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Logger:   discardLogger(),
		Evidence: store,
		Plugins: []plugin.Plugin{
			offeringPlugin{id: "execution", activate: func(h plugin.Host) error { granted = h.Evidence(); return nil }},
			offeringPlugin{id: "display", activate: func(h plugin.Host) error { other = h.Evidence(); return nil }},
		},
	})
	require.NoError(t, err)
	ctx := context.Background()
	environment := plugin.BoundEnvironment{AttemptID: "a", BindingID: "b", Generation: 1,
		Identity: plugin.EnvironmentIdentity{GrantID: "g", Epoch: "e"}}
	detail := json.RawMessage(`{"argv":["make"]}`)
	record := plugin.EvidenceRecord{Kind: "process_log", Sequence: 1, ObservedAt: time.Unix(1, 0), Detail: detail}

	receipt, err := granted.Append(ctx, environment, record)
	require.NoError(t, err)
	require.Equal(t, "stored", receipt.ID)
	require.Len(t, store.appended, 1)
	require.Equal(t, "execution.process_log", store.appended[0].kind, "the host names the plugin, never the plugin")
	require.Equal(t, environment, store.appended[0].environment)
	detail[2] = 'X'
	require.JSONEq(t, `{"argv":["make"]}`, string(store.appended[0].record.Detail), "the store keeps its own copy")

	for _, kind := range []string{"", "Process", "execution.process_log", "native.display", "a-b", "x234567890123456789012345678901234"} {
		bad := record
		bad.Kind = kind
		_, err = granted.Append(ctx, environment, bad)
		require.ErrorIs(t, err, plugin.ErrEvidenceInvalid, "kind %q", kind)
	}

	_, err = other.Append(ctx, environment, record)
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a plugin the core grants no binding cannot append")

	require.NoError(t, host.Close(ctx))
	_, err = granted.Append(ctx, environment, record)
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a deactivated plugin cannot append")
	require.Len(t, store.appended, 1)
}

func TestEvidenceWithoutAStoreIsUnavailable(t *testing.T) {
	t.Parallel()

	var evidence plugin.Evidence
	_, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Logger: discardLogger(),
		Plugins: []plugin.Plugin{offeringPlugin{id: "execution", activate: func(h plugin.Host) error {
			evidence = h.Evidence()
			return nil
		}}},
	})
	require.NoError(t, err)
	_, err = evidence.Append(context.Background(), plugin.BoundEnvironment{}, plugin.EvidenceRecord{Kind: "process_log"})
	require.ErrorIs(t, err, plugin.ErrUnavailable)
}
