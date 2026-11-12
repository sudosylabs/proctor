// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/server/model"
)

func chunkSHA256(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

func TestScreenClipChunksAreKeptOnlyWhenTheyMatchTheirHash(t *testing.T) {
	t.Parallel()

	for _, backend := range contentTestBackends() {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			filesystem := backend.open(t)
			content, err := New(filesystem, Policy{MaximumConcurrentOperations: 2}, nil)
			require.NoError(t, err)
			clipID := model.NewScreenClipID()
			const body = "screen clip chunk bytes"

			require.NoError(t, content.StageScreenClipChunk(ctx, clipID, 0, strings.NewReader(body), int64(len(body)), chunkSHA256(body)))
			opened, err := filesystem.Open(ctx, screenClipChunkKey(clipID, 0), vfs.OpenOptions{Offset: 7, Length: 4})
			require.NoError(t, err)
			ranged, err := io.ReadAll(opened.Body)
			require.NoError(t, opened.Body.Close())
			require.NoError(t, err)
			require.Equal(t, "clip", string(ranged), "a stored chunk reads back by range")

			require.NoError(t, content.StageScreenClipChunk(ctx, clipID, 0, strings.NewReader(body), int64(len(body)), chunkSHA256(body)),
				"sending a stored chunk again succeeds")

			err = content.StageScreenClipChunk(ctx, clipID, 1, strings.NewReader("tampered"), 8, chunkSHA256("expected"))
			require.ErrorIs(t, err, model.ErrScreenClipChunkInvalid)
			_, statErr := filesystem.Stat(ctx, screenClipChunkKey(clipID, 1))
			require.ErrorIs(t, statErr, vfs.ErrNotFound, "a chunk that misses its hash is not kept")

			err = content.StageScreenClipChunk(ctx, clipID, 2, strings.NewReader("short"), 8, chunkSHA256("short"))
			require.ErrorIs(t, err, model.ErrScreenClipChunkInvalid, "a chunk shorter than declared is invalid")
			_, statErr = filesystem.Stat(ctx, screenClipChunkKey(clipID, 2))
			require.ErrorIs(t, statErr, vfs.ErrNotFound)
		})
	}
}

func TestARetiredScreenClipIsPurgedChunkByChunk(t *testing.T) {
	t.Parallel()

	for _, backend := range contentTestBackends() {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			filesystem := backend.open(t)
			content, err := New(filesystem, Policy{MaximumConcurrentOperations: 2}, nil)
			require.NoError(t, err)
			clipID := model.NewScreenClipID()
			for index, body := range []string{"first chunk", "second chunk"} {
				require.NoError(t, content.StageScreenClipChunk(ctx, clipID, index, strings.NewReader(body), int64(len(body)), chunkSHA256(body)))
			}

			// Three declared chunks, of which only two arrived.
			require.NoError(t, content.PurgeRetiredScreenClip(ctx, clipID, 3))
			for index := range 3 {
				_, statErr := filesystem.Stat(ctx, screenClipChunkKey(clipID, index))
				require.ErrorIs(t, statErr, vfs.ErrNotFound, "chunk %d", index)
			}
			require.NoError(t, content.PurgeRetiredScreenClip(ctx, clipID, 3), "a purge repeats safely")
			require.Error(t, content.PurgeRetiredScreenClip(ctx, clipID, 0))
		})
	}
}
