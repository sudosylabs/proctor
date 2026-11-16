// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

const maximumRevisionObjects = 100

// ErrPurgeLimit means an abandoned revision holds more objects than one purge
// removes; it is left untouched for an operator to investigate.
var ErrPurgeLimit = errors.New("file content: abandoned revision exceeds purge limit")

// Content stores and opens immutable file content in VFS.
type Content struct {
	filesystem       vfs.FileSystem
	work             chan struct{}
	recorder         WorkRecorder
	examExportFormat func() plugin.ExportFormat
}

// Policy bounds expensive content processing within one application node.
// It limits concurrent operations, not their aggregate memory consumption.
type Policy struct {
	MaximumConcurrentOperations int
	// ExamExportFormat is looked up on each exam export. When it or its
	// result is nil, the export fails as unavailable.
	ExamExportFormat func() plugin.ExportFormat
}

// WorkRecorder receives bounded node-local processing measurements. Methods
// must be safe for concurrent use and return promptly. A nil recorder is valid.
type WorkRecorder interface {
	Started()
	Finished(time.Duration)
	Rejected()
}

// New returns a Content whose expensive pipelines share one non-queueing limit
// of policy.MaximumConcurrentOperations.
func New(filesystem vfs.FileSystem, policy Policy, recorder WorkRecorder) (*Content, error) {
	if filesystem == nil {
		return nil, errors.New("file content VFS is required")
	}
	if policy.MaximumConcurrentOperations <= 0 {
		return nil, errors.New("file content maximum concurrent operations must be positive")
	}
	return &Content{
		filesystem:       filesystem,
		work:             make(chan struct{}, policy.MaximumConcurrentOperations),
		recorder:         recorder,
		examExportFormat: policy.ExamExportFormat,
	}, nil
}

func (c *Content) storeRendition(
	ctx context.Context,
	revisionID model.FileRevisionID,
	renditionID model.FileRenditionID,
	body io.Reader,
	size int64,
) error {
	if c == nil || c.filesystem == nil || !revisionID.IsValid() || !renditionID.IsValid() || body == nil || size < 0 {
		return errors.New("invalid profile-picture rendition")
	}

	_, err := c.filesystem.Write(ctx, renditionKey(revisionID, renditionID), body, vfs.WriteOptions{
		Size:        &size,
		NoOverwrite: c.filesystem.Capabilities().ConditionalWrite,
	})
	return sanitize("stage rendition", err)
}

// OpenProfilePictureRendition opens the rendition named by the profile
// picture's stored metadata.
func (c *Content) OpenProfilePictureRendition(
	ctx context.Context,
	revisionID model.FileRevisionID,
	renditionID model.FileRenditionID,
) (io.ReadCloser, error) {
	if c == nil || c.filesystem == nil || !revisionID.IsValid() || !renditionID.IsValid() {
		return nil, errors.New("invalid file rendition identity")
	}

	file, err := c.filesystem.Open(ctx, renditionKey(revisionID, renditionID), vfs.OpenOptions{})
	if err != nil {
		return nil, sanitize("open rendition", err)
	}

	return file.Body, nil
}

func (c *Content) removeRenditions(ctx context.Context, revisionID model.FileRevisionID, renditionIDs []model.FileRenditionID) error {
	if c == nil || c.filesystem == nil || !revisionID.IsValid() {
		return errors.New("invalid file revision identity")
	}
	for _, renditionID := range renditionIDs {
		if !renditionID.IsValid() {
			return errors.New("invalid file rendition identity")
		}
	}
	var joined error
	for _, renditionID := range renditionIDs {
		err := c.filesystem.Remove(ctx, renditionKey(revisionID, renditionID), vfs.RemoveOptions{})
		if errors.Is(err, vfs.ErrNotFound) {
			continue
		}
		joined = errors.Join(joined, sanitize("remove rendition", err))
	}
	return joined
}

// RemoveFileRevisionRenditions removes exactly the listed renditions, ignoring
// ones already gone. It never lists the revision for other objects.
func (c *Content) RemoveFileRevisionRenditions(
	ctx context.Context,
	revisionID model.FileRevisionID,
	renditionIDs []model.FileRenditionID,
) error {
	return c.removeRenditions(ctx, revisionID, renditionIDs)
}

// PurgeAbandonedFileRevision deletes everything under one revision whose
// expired upload lease the caller has durably claimed, or returns ErrPurgeLimit.
func (c *Content) PurgeAbandonedFileRevision(ctx context.Context, revisionID model.FileRevisionID) error {
	return c.purgeAbandonedRevision(ctx, revisionID)
}

func (c *Content) purgeAbandonedRevision(ctx context.Context, revisionID model.FileRevisionID) error {
	if c == nil || c.filesystem == nil || !revisionID.IsValid() {
		return errors.New("invalid file revision identity")
	}
	prefix := revisionPrefix(revisionID)
	page, err := c.filesystem.List(ctx, vfs.ListOptions{Prefix: prefix, Limit: maximumRevisionObjects})
	if err != nil {
		return sanitize("list abandoned revision", err)
	}
	if page.NextCursor != "" {
		return ErrPurgeLimit
	}
	for _, entry := range page.Entries {
		if entry.IsDir {
			continue
		}
		if !strings.HasPrefix(entry.Path, prefix) {
			return errors.New("file content: backend returned object outside revision prefix")
		}
		err = c.filesystem.Remove(ctx, entry.Path, vfs.RemoveOptions{})
		if errors.Is(err, vfs.ErrNotFound) {
			continue
		}
		if err != nil {
			return sanitize("purge abandoned revision", err)
		}
	}
	return nil
}

func renditionKey(revisionID model.FileRevisionID, renditionID model.FileRenditionID) string {
	return revisionPrefix(revisionID) + renditionID.String() + ".webp"
}

func revisionPrefix(revisionID model.FileRevisionID) string {
	id := revisionID.String()
	return fmt.Sprintf("files/%s/%s/revisions/%s/renditions/", id[:2], id[2:4], id)
}

type operationError struct {
	operation string
	kind      errorKind
	cause     error
}

func (e *operationError) Error() string { return "file content: " + e.operation + " failed" }
func (e *operationError) Unwrap() error { return e.cause }

type errorKind uint8

const (
	errorUnavailable errorKind = iota
	errorNotFound
	errorConflict
)

func sanitize(operation string, err error) error {
	if err == nil {
		return nil
	}
	kind := errorUnavailable
	if errors.Is(err, vfs.ErrNotFound) {
		kind = errorNotFound
	} else if errors.Is(err, vfs.ErrAlreadyExists) || errors.Is(err, vfs.ErrConflict) {
		kind = errorConflict
	}
	return &operationError{operation: operation, kind: kind, cause: err}
}

// IsNotFound reports whether stored content does not exist.
func IsNotFound(err error) bool {
	return hasKind(err, errorNotFound)
}

// IsConflict reports whether content already exists at the key or the backend
// rejected a concurrent write.
func IsConflict(err error) bool {
	return hasKind(err, errorConflict)
}

// IsUnavailable reports whether the backend failed an otherwise valid
// operation.
func IsUnavailable(err error) bool {
	return hasKind(err, errorUnavailable)
}

func hasKind(err error, kind errorKind) bool {
	var operation *operationError
	return errors.As(err, &operation) && operation.kind == kind
}
