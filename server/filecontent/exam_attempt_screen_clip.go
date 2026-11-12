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
	"crypto/subtle"
	"errors"
	"fmt"
	"io"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/server/model"
)

// StageScreenClipChunk streams one chunk of exactly size bytes to its own
// object while hashing it, and keeps it only if it matches expectedSHA256.
// The bytes never touch a local spool. A chunk already stored with the same
// bytes is left as it is, so Desktop can send a chunk again after a lost
// response.
func (c *Content) StageScreenClipChunk(
	ctx context.Context,
	clipID model.ScreenClipID,
	index int,
	body io.Reader,
	size int64,
	expectedSHA256 string,
) error {
	if c == nil || c.filesystem == nil {
		return errors.New("screen clip content dependency is unavailable")
	}
	if !clipID.IsValid() || index < 0 || body == nil || size < 1 || size > model.ScreenClipChunkBytes || len(expectedSHA256) != 64 {
		return model.ErrScreenClipChunkInvalid
	}

	key := screenClipChunkKey(clipID, index)
	if matching, err := c.screenClipChunkMatches(ctx, key, size, expectedSHA256); err != nil {
		return err
	} else if matching {
		_, err = io.Copy(io.Discard, io.LimitReader(body, size))
		return err
	}

	stream := newExactSizeReader(body, size)
	written := size
	info, writeErr := c.filesystem.Write(ctx, key, stream, vfs.WriteOptions{Size: &written})
	finishErr := stream.finish()
	mismatch := finishErr == nil && subtle.ConstantTimeCompare([]byte(stream.checksum()), []byte(expectedSHA256)) != 1
	if writeErr != nil || finishErr != nil || mismatch || info.Size != size {
		if removeErr := c.filesystem.Remove(ctx, key, vfs.RemoveOptions{}); removeErr != nil && !errors.Is(removeErr, vfs.ErrNotFound) {
			return errors.Join(sanitize("stage screen clip chunk", writeErr), sanitize("remove rejected screen clip chunk", removeErr))
		}
		switch {
		case finishErr != nil:
			return errors.Join(model.ErrScreenClipChunkInvalid, finishErr)
		case mismatch:
			return model.ErrScreenClipChunkInvalid
		case writeErr != nil:
			return sanitize("stage screen clip chunk", writeErr)
		default:
			return sanitize("stage screen clip chunk", errors.New("backend size mismatch"))
		}
	}
	return nil
}

func (c *Content) screenClipChunkMatches(ctx context.Context, key string, size int64, expectedSHA256 string) (bool, error) {
	file, err := c.filesystem.Open(ctx, key, vfs.OpenOptions{})
	if errors.Is(err, vfs.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, sanitize("open screen clip chunk", err)
	}
	defer file.Body.Close()
	if file.Info.Size != size {
		return false, nil
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, io.LimitReader(file.Body, size+1)); err != nil {
		return false, sanitize("read screen clip chunk", err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)) == expectedSHA256, nil
}

func screenClipChunkKey(clipID model.ScreenClipID, index int) string {
	id := clipID.String()
	return fmt.Sprintf("exam-screen-clips/%s/%s/%s/chunks/%03d", id[:2], id[2:4], id, index)
}
