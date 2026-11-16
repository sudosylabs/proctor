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
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/server/model"
)

// StarterWorkspaceInvalidContentError marks caller content that failed
// validation, as distinct from a storage failure.
type StarterWorkspaceInvalidContentError struct{ cause error }

func (err *StarterWorkspaceInvalidContentError) Error() string {
	return "invalid Starter Workspace content"
}
func (err *StarterWorkspaceInvalidContentError) Unwrap() error { return err.cause }
func (err *StarterWorkspaceInvalidContentError) InvalidStarterWorkspaceContent() bool {
	return true
}

func invalidStarterWorkspaceContent(cause error) error {
	return &StarterWorkspaceInvalidContentError{cause: cause}
}

// StageStarterWorkspaceObject streams one bounded file to a new object, which
// stays invisible until the caller publishes it in PostgreSQL.
func (c *Content) StageStarterWorkspaceObject(
	ctx context.Context,
	objectID model.StarterWorkspaceObjectID,
	body io.Reader,
	declaredSize int64,
	mediaType string,
) (*model.StarterWorkspaceContent, error) {
	mediaType = strings.TrimSpace(mediaType)
	if c == nil || c.filesystem == nil {
		return nil, errors.New("Starter Workspace content dependency is unavailable")
	}

	if !objectID.IsValid() ||
		body == nil ||
		declaredSize < -1 ||
		declaredSize > model.StarterWorkspaceMaximumFileBytes ||
		mediaType == "" ||
		len(mediaType) > 255 {
		return nil, invalidStarterWorkspaceContent(errors.New("invalid metadata"))
	}

	hash := sha256.New()
	limited := &io.LimitedReader{R: body, N: model.StarterWorkspaceMaximumFileBytes + 1}
	reader := io.TeeReader(limited, hash)
	options := vfs.WriteOptions{NoOverwrite: c.filesystem.Capabilities().ConditionalWrite}
	info, err := c.filesystem.Write(ctx, starterWorkspaceObjectKey(objectID), reader, options)
	if err != nil {
		return nil, sanitize("stage Starter Workspace object", err)
	}

	if info.Size > model.StarterWorkspaceMaximumFileBytes || limited.N == 0 || declaredSize >= 0 && info.Size != declaredSize {
		_ = c.filesystem.Remove(ctx, starterWorkspaceObjectKey(objectID), vfs.RemoveOptions{})
		return nil, invalidStarterWorkspaceContent(errors.New("content size mismatch"))
	}

	return &model.StarterWorkspaceContent{MediaType: mediaType, SizeBytes: info.Size, SHA256: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

// OpenStarterWorkspaceObject opens the object named by authorized stored
// metadata; it checks no access itself.
func (c *Content) OpenStarterWorkspaceObject(ctx context.Context, objectID model.StarterWorkspaceObjectID) (io.ReadCloser, error) {
	if c == nil || c.filesystem == nil || !objectID.IsValid() {
		return nil, fmt.Errorf("invalid Starter Workspace object identity")
	}
	file, err := c.filesystem.Open(ctx, starterWorkspaceObjectKey(objectID), vfs.OpenOptions{})
	if err != nil {
		return nil, sanitize("open Starter Workspace object", err)
	}
	return file.Body, nil
}

// RemoveStarterWorkspaceObject removes an object the caller has durably
// claimed for cleanup. A missing object is not an error.
func (c *Content) RemoveStarterWorkspaceObject(ctx context.Context, objectID model.StarterWorkspaceObjectID) error {
	if c == nil || c.filesystem == nil || !objectID.IsValid() {
		return fmt.Errorf("invalid Starter Workspace object identity")
	}
	err := c.filesystem.Remove(ctx, starterWorkspaceObjectKey(objectID), vfs.RemoveOptions{})
	if errors.Is(err, vfs.ErrNotFound) {
		return nil
	}
	return sanitize("remove Starter Workspace object", err)
}

func starterWorkspaceObjectKey(objectID model.StarterWorkspaceObjectID) string {
	id := objectID.String()
	return fmt.Sprintf("exam-starter-workspace/%s/%s/objects/%s", id[:2], id[2:4], id)
}
