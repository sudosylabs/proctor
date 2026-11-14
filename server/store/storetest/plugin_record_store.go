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
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestPluginRecordStore checks the plugin record store: compare-and-swap
// creation, replacement and deletion, key-ordered paging by prefix, the
// bounds on kinds, keys and documents, racing creators, and that no plugin
// reaches another's records.
func TestPluginRecordStore(t *testing.T, stores store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	records := stores.PluginRecord()
	const alpha, beta = model.PluginID("alpha"), model.PluginID("beta")

	t.Run("create replace delete", func(t *testing.T) {
		_, err := records.Get(ctx, alpha, "grant", "attempt/one")
		require.True(t, store.IsNotFound(err), "a missing record is not found: %v", err)

		created, err := records.Put(ctx, &store.PluginRecordPut{
			PluginID: alpha, Kind: "grant", Key: "attempt/one", Document: json.RawMessage(" {\"epoch\": 1,\n \"host\":\"h1\"} "),
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), created.Revision)
		require.JSONEq(t, `{"epoch":1,"host":"h1"}`, string(created.Document))
		require.NotContains(t, string(created.Document), " ", "the document comes back compact")
		require.Equal(t, created.CreatedAt, created.UpdatedAt)
		require.NoError(t, created.Validate())

		read, err := records.Get(ctx, alpha, "grant", "attempt/one")
		require.NoError(t, err)
		require.Equal(t, created, read, "Get returns the record as Put stored it")

		requireRecordConflict(t, 1, putRecord(ctx, records, alpha, "grant", "attempt/one", `{"epoch":9}`, 0),
			"creating an existing record conflicts")

		replaced, err := records.Put(ctx, &store.PluginRecordPut{
			PluginID: alpha, Kind: "grant", Key: "attempt/one", Document: json.RawMessage(`{"epoch":2}`), ExpectedRevision: 1,
		})
		require.NoError(t, err)
		require.Equal(t, int64(2), replaced.Revision)
		require.JSONEq(t, `{"epoch":2}`, string(replaced.Document), "Put replaces the whole document")
		require.Equal(t, created.CreatedAt, replaced.CreatedAt)
		require.False(t, replaced.UpdatedAt.Before(created.UpdatedAt))

		requireRecordConflict(t, 2, putRecord(ctx, records, alpha, "grant", "attempt/one", `{"epoch":3}`, 1),
			"a stale revision conflicts")
		requireRecordConflict(t, 0, putRecord(ctx, records, alpha, "grant", "attempt/none", `{}`, 4),
			"replacing a missing record conflicts with revision 0")

		requireRecordConflict(t, 2, records.Delete(ctx, &store.PluginRecordDeletion{
			PluginID: alpha, Kind: "grant", Key: "attempt/one", ExpectedRevision: 1,
		}), "a stale delete conflicts")
		require.NoError(t, records.Delete(ctx, &store.PluginRecordDeletion{
			PluginID: alpha, Kind: "grant", Key: "attempt/one", ExpectedRevision: 2,
		}))
		_, err = records.Get(ctx, alpha, "grant", "attempt/one")
		require.True(t, store.IsNotFound(err), "a deleted record is gone: %v", err)
		requireRecordConflict(t, 0, records.Delete(ctx, &store.PluginRecordDeletion{
			PluginID: alpha, Kind: "grant", Key: "attempt/one", ExpectedRevision: 2,
		}), "deleting a missing record conflicts with revision 0")

		again, err := records.Put(ctx, &store.PluginRecordPut{
			PluginID: alpha, Kind: "grant", Key: "attempt/one", Document: json.RawMessage(`{}`),
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), again.Revision, "a record created again starts over")
	})

	t.Run("list pages by prefix in byte order", func(t *testing.T) {
		for _, key := range []string{"page/b", "page/a", "page/C", "page_x", "page/c/1", "other/a"} {
			require.NoError(t, putRecord(ctx, records, alpha, "page", key, `{"key":"`+key+`"}`, 0))
		}
		require.NoError(t, putRecord(ctx, records, alpha, "other", "page/a", `{}`, 0))

		first, err := records.List(ctx, store.PluginRecordListOptions{PluginID: alpha, Kind: "page", Prefix: "page/", Limit: 2})
		require.NoError(t, err)
		require.Equal(t, []string{"page/C", "page/a"}, recordKeys(first), "keys compare bytewise and the limit holds")
		rest, err := records.List(ctx, store.PluginRecordListOptions{
			PluginID: alpha, Kind: "page", Prefix: "page/", After: "page/a", Limit: 10,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"page/b", "page/c/1"}, recordKeys(rest),
			"the next page starts after the last key, and an underscore is not a wildcard")

		all, err := records.List(ctx, store.PluginRecordListOptions{PluginID: alpha, Kind: "page", Limit: 100})
		require.NoError(t, err)
		require.Equal(t, []string{"other/a", "page/C", "page/a", "page/b", "page/c/1", "page_x"}, recordKeys(all),
			"an empty prefix lists the whole kind and nothing of another kind")
		require.JSONEq(t, `{"key":"other/a"}`, string(all[0].Document))

		none, err := records.List(ctx, store.PluginRecordListOptions{PluginID: alpha, Kind: "page", Prefix: "zzz", Limit: 5})
		require.NoError(t, err)
		require.NotNil(t, none)
		require.Empty(t, none)
	})

	t.Run("plugins are isolated", func(t *testing.T) {
		require.NoError(t, putRecord(ctx, records, alpha, "shared", "same/key", `{"owner":"alpha"}`, 0))
		_, err := records.Get(ctx, beta, "shared", "same/key")
		require.True(t, store.IsNotFound(err), "one plugin never reads another's record: %v", err)
		listed, err := records.List(ctx, store.PluginRecordListOptions{PluginID: beta, Kind: "shared", Limit: 10})
		require.NoError(t, err)
		require.Empty(t, listed, "one plugin never lists another's records")
		requireRecordConflict(t, 0, records.Delete(ctx, &store.PluginRecordDeletion{
			PluginID: beta, Kind: "shared", Key: "same/key", ExpectedRevision: 1,
		}), "one plugin never deletes another's record")
		requireRecordConflict(t, 0, putRecord(ctx, records, beta, "shared", "same/key", `{"owner":"beta"}`, 1),
			"one plugin never replaces another's record")

		require.NoError(t, putRecord(ctx, records, beta, "shared", "same/key", `{"owner":"beta"}`, 0),
			"the same kind and key are free in another plugin")
		mine, err := records.Get(ctx, alpha, "shared", "same/key")
		require.NoError(t, err)
		require.JSONEq(t, `{"owner":"alpha"}`, string(mine.Document))
	})

	t.Run("racing creators", func(t *testing.T) {
		const racers = 8
		var wg sync.WaitGroup
		results := make(chan error, racers)
		for range racers {
			wg.Go(func() { results <- putRecord(ctx, records, alpha, "race", "one", `{}`, 0) })
		}
		wg.Wait()
		close(results)
		won := 0
		for err := range results {
			if err == nil {
				won++
				continue
			}
			requireRecordConflict(t, 1, err, "every losing creator conflicts")
		}
		require.Equal(t, 1, won, "exactly one creator wins")
	})

	t.Run("bounds", func(t *testing.T) {
		limit := `{"v":"` + strings.Repeat("x", store.PluginRecordDocumentMaxBytes-8) + `"}`
		require.Len(t, limit, store.PluginRecordDocumentMaxBytes)
		require.NoError(t, putRecord(ctx, records, alpha, "bounds", "largest", limit, 0), "a document at the limit is kept")
		largest, err := records.Get(ctx, alpha, "bounds", "largest")
		require.NoError(t, err)
		require.Equal(t, limit, string(largest.Document))

		longest := strings.Repeat("k", store.PluginRecordKeyMaxBytes)
		require.NoError(t, putRecord(ctx, records, alpha, strings.Repeat("k", store.PluginRecordKindMaxBytes), longest, `{}`, 0),
			"the longest kind and key are kept")
		require.NoError(t, putRecord(ctx, records, alpha, "bounds", "A-z.0_~:@=+/x", `{}`, 0), "every key character is kept")

		require.ErrorIs(t, putRecord(ctx, records, alpha, "bounds", "big",
			`{"v":"`+strings.Repeat("x", store.PluginRecordDocumentMaxBytes-7)+`"}`, 0), store.ErrPluginRecordTooLarge,
			"a document one byte over the limit is too large")
		spaced := `{"v": "` + strings.Repeat("x", store.PluginRecordDocumentMaxBytes-8) + `"}`
		require.NoError(t, putRecord(ctx, records, alpha, "bounds", "spaced", spaced, 0), "the limit counts the compact form")

		invalid := []struct {
			name, kind, key, document string
		}{
			{"array document", "bounds", "array", `[]`},
			{"null document", "bounds", "null", `null`},
			{"number document", "bounds", "number", `1`},
			{"malformed document", "bounds", "malformed", `{"a":`},
			{"trailing document", "bounds", "trailing", `{} {}`},
			{"empty kind", "", "k", `{}`},
			{"uppercase kind", "Grant", "k", `{}`},
			{"kind starting with a digit", "1grant", "k", `{}`},
			{"long kind", strings.Repeat("k", store.PluginRecordKindMaxBytes+1), "k", `{}`},
			{"empty key", "bounds", "", `{}`},
			{"leading slash", "bounds", "/a", `{}`},
			{"trailing slash", "bounds", "a/", `{}`},
			{"empty segment", "bounds", "a//b", `{}`},
			{"space in key", "bounds", "a b", `{}`},
			{"percent in key", "bounds", "a%b", `{}`},
			{"non-ASCII key", "bounds", "é", `{}`},
			{"long key", "bounds", longest + "k", `{}`},
		}
		for _, test := range invalid {
			err := putRecord(ctx, records, alpha, test.kind, test.key, test.document, 0)
			var target *store.ErrInvalidInput
			require.ErrorAs(t, err, &target, test.name)
		}
		var target *store.ErrInvalidInput
		_, err = records.Get(ctx, "Not-A-Plugin", "bounds", "k")
		require.ErrorAs(t, err, &target, "an invalid plugin ID")
		require.ErrorAs(t, putRecord(ctx, records, alpha, "bounds", "k", `{}`, -1), &target, "a negative revision")
		require.ErrorAs(t, records.Delete(ctx, &store.PluginRecordDeletion{PluginID: alpha, Kind: "bounds", Key: "k"}), &target,
			"deleting needs a revision")
		for _, options := range []store.PluginRecordListOptions{
			{PluginID: alpha, Kind: "bounds", Limit: 0},
			{PluginID: alpha, Kind: "bounds", Limit: store.PluginRecordListMaxLimit + 1},
			{PluginID: alpha, Kind: "bounds", Prefix: "/", Limit: 1},
			{PluginID: alpha, Kind: "bounds", After: "a/", Limit: 1},
		} {
			_, err := records.List(ctx, options)
			require.ErrorAs(t, err, &target, "invalid list options %+v", options)
		}
	})
}

func putRecord(ctx context.Context, records store.PluginRecordStore, pluginID model.PluginID, kind, key, document string, expected int64) error {
	_, err := records.Put(ctx, &store.PluginRecordPut{
		PluginID: pluginID, Kind: kind, Key: key, Document: json.RawMessage(document), ExpectedRevision: expected,
	})
	return err
}

func requireRecordConflict(t *testing.T, current int64, err error, message string) {
	t.Helper()
	var conflict *store.ErrPluginRecordRevisionConflict
	require.True(t, errors.As(err, &conflict), "%s: %v", message, err)
	require.Equal(t, current, conflict.CurrentRevision, message)
}

func recordKeys(records []*store.PluginRecord) []string {
	keys := make([]string, 0, len(records))
	for _, record := range records {
		keys = append(keys, record.Key)
	}
	return keys
}
