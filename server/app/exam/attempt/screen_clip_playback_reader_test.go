// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// chunkStore serves chunks from memory, optionally cutting one short.
type chunkStore struct {
	Content
	chunks  map[int][]byte
	shorten int
	opened  []int
}

func (c *chunkStore) OpenScreenClipChunk(_ context.Context, _ model.ScreenClipID, index int, offset, length int64) (io.ReadCloser, error) {
	c.opened = append(c.opened, index)
	data, ok := c.chunks[index]
	if !ok {
		return nil, errors.New("missing chunk")
	}
	end := offset + length
	if index == c.shorten {
		end--
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func TestScreenClipRangesReadAcrossChunks(t *testing.T) {
	t.Parallel()

	size := 2*model.ScreenClipChunkBytes + 10
	whole := make([]byte, size)
	for i := range whole {
		whole[i] = byte(i % 251)
	}
	chunks := map[int][]byte{
		0: whole[:model.ScreenClipChunkBytes],
		1: whole[model.ScreenClipChunkBytes : 2*model.ScreenClipChunkBytes],
		2: whole[2*model.ScreenClipChunkBytes:],
	}
	upload := &model.ScreenClipUpload{MediaType: "video/mp4", SizeBytes: size, ChunkSHA256: make([]string, 3)}
	read := func(store *chunkStore, start, length int64) ([]byte, error) {
		reader := &screenClipReader{ctx: context.Background(), content: store, clipID: model.NewScreenClipID(),
			upload: upload, offset: start, remaining: length}
		defer reader.Close()
		return io.ReadAll(reader)
	}

	store := &chunkStore{chunks: chunks, shorten: -1}
	start, length := model.ScreenClipChunkBytes-5, model.ScreenClipChunkBytes+12
	got, err := read(store, start, length)
	require.NoError(t, err)
	require.Equal(t, whole[start:start+length], got, "a range spanning three chunks reads exactly")
	require.Equal(t, []int{0, 1, 2}, store.opened)

	store = &chunkStore{chunks: chunks, shorten: -1}
	got, err = read(store, 2*model.ScreenClipChunkBytes+3, 4)
	require.NoError(t, err)
	require.Equal(t, whole[2*model.ScreenClipChunkBytes+3:2*model.ScreenClipChunkBytes+7], got)
	require.Equal(t, []int{2}, store.opened, "only the chunks the range reaches are opened")

	_, err = read(&chunkStore{chunks: chunks, shorten: 1}, 0, size)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF, "a stored chunk shorter than declared fails the read")
}
