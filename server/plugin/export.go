// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"io"
)

// ExportFormat encodes a verified export archive. Proctor chooses the scope
// and bounds, hashes, stores and verifies the output; a format only writes.
//
// A format must read every file exactly once, to its end. Proctor checks each
// file's size and SHA-256 as it is read and fails the export when a file is
// skipped, read twice, truncated or altered, so a format cannot silently drop
// or change submission content.
type ExportFormat interface {
	// MediaType is the media type of the written bytes, for example
	// "application/zip".
	MediaType() string
	// FileExtension is the file name extension, with its dot, for example
	// ".zip".
	FileExtension() string
	// Write encodes archive to w. It stops when ctx is done.
	Write(ctx context.Context, archive ExportArchive, w io.Writer) error
}

// ExportArchive is the verified content of one export.
type ExportArchive struct {
	ExportID string
	Records  []byte
	Files    []ExportFile
}

// ExportFile is one submission file in an export archive.
type ExportFile struct {
	// Path is the slash-separated, relative path Proctor assigns the file in
	// the archive. It is unique within the archive and already validated.
	Path         string
	SubmissionID string
	EntryID      string
	// OriginalPath is the file's path in the candidate's workspace, which
	// may differ from Path.
	OriginalPath string
	MediaType    string
	Size         int64
	// SHA256 is the lowercase hex digest of the file's bytes.
	SHA256 string
	// Open returns a reader of the file's bytes; the caller closes it.
	// Proctor allows each file to be opened once.
	Open func(context.Context) (io.ReadCloser, error)
}
