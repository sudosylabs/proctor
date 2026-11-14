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

// ExportFormat turns a verified export archive into bytes.
//
// Proctor still owns everything around the write: it chooses the scope,
// assembles the archive, spools the output, bounds its size, hashes it,
// stores it and verifies the written result. A format only writes bytes; it
// never chooses what is exported and reads nothing except the archive it is
// given.
//
// A format must read every file in the archive exactly once, to its end.
// Proctor checks each file's size and SHA-256 as it is read and fails the
// export when a file is skipped, read twice, truncated or altered, so a
// format cannot silently drop or change Submission content.
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
	// ExportID identifies the export the archive belongs to.
	ExportID string
	// Records is the canonical records document.
	Records []byte
	Files   []ExportFile
}

// ExportFile is one Submission file in an export archive.
type ExportFile struct {
	// Path is the slash-separated, relative path Proctor assigns the file in
	// the archive. It is unique within the archive and already validated.
	Path string
	// SubmissionID and EntryID identify the sealed Submission and the
	// Workspace entry the file came from.
	SubmissionID string
	EntryID      string
	// OriginalPath is the file's path inside the candidate's Workspace.
	OriginalPath string
	// MediaType is the file's recorded media type.
	MediaType string
	Size      int64
	// SHA256 is the lowercase hex digest of the file's bytes.
	SHA256 string
	// Open returns a reader of the file's bytes; the caller closes it.
	// Proctor allows each file to be opened once.
	Open func(context.Context) (io.ReadCloser, error)
}
