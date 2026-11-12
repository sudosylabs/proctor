// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

type examExportManifest struct {
	ExportID model.ExamExportID        `json:"export_id"`
	Entries  []examExportManifestEntry `json:"entries"`
}

type examExportManifestEntry struct {
	ArchivePath  string                        `json:"archive_path"`
	SizeBytes    int64                         `json:"size_bytes"`
	SHA256       string                        `json:"sha256"`
	SubmissionID model.SubmissionID            `json:"submission_id,omitempty"`
	EntryID      model.AttemptWorkspaceEntryID `json:"entry_id,omitempty"`
	OriginalPath string                        `json:"original_path,omitempty"`
	MediaType    string                        `json:"media_type,omitempty"`
}

const examExportReadme = `Proctor examination export

records.json contains the selected structured records as captured when requested.
manifest.json lists every data member with its exact byte size and SHA-256 hash.
The manifest itself is excluded from that list to avoid a circular checksum.

Original files are unchanged. Their ZIP member names use opaque identifiers;
original workspace paths and empty directories are recorded in records.json.
Do not execute exported files or render private review text as trusted HTML.

To verify independently, read manifest.json, reject duplicate/unlisted data
members, and compute the size and SHA-256 of each uncompressed member. Compare
the complete ZIP SHA-256 with the value supplied by the authenticated export API.
Hashes detect changes; they are not a digital signature or proof of authorship.
This format is a portable record archive, not an installation backup or importer.
`

// zipExportFormat is the built-in Examination Export encoding: a stored
// (uncompressed) ZIP of records.json, README.txt, every file and a closing
// manifest.json. Its bytes are deterministic for a given archive.
//
// The manifest records the size and digest the format computed while
// writing each member. For files these equal ExportFile.Size and SHA256
// whenever the export succeeds, because the core verifies every file read.
type zipExportFormat struct{}

var _ plugin.ExportFormat = zipExportFormat{}

func (zipExportFormat) MediaType() string     { return "application/zip" }
func (zipExportFormat) FileExtension() string { return ".zip" }

// Write writes the records, README, files and manifest, then closes the ZIP.
func (zipExportFormat) Write(ctx context.Context, archive plugin.ExportArchive, w io.Writer) error {
	writer := zip.NewWriter(w)
	manifest := examExportManifest{ExportID: model.ExamExportID(archive.ExportID), Entries: make([]examExportManifestEntry, 0, len(archive.Files)+2)}
	for _, item := range []struct {
		name string
		body []byte
	}{{"records.json", archive.Records}, {"README.txt", []byte(examExportReadme)}} {
		size, digest, err := writeExamExportMember(ctx, writer, item.name, bytes.NewReader(item.body), int64(len(item.body)))
		if err != nil {
			return sanitize("write Exam export records", err)
		}
		manifest.Entries = append(manifest.Entries, examExportManifestEntry{ArchivePath: item.name, SizeBytes: size, SHA256: digest})
	}
	for _, f := range archive.Files {
		entry, err := writeExamExportFile(ctx, writer, f)
		if err != nil {
			return err
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return sanitize("encode Exam export manifest", err)
	}
	if _, _, err = writeExamExportMember(ctx, writer, "manifest.json", bytes.NewReader(encoded), int64(len(encoded))); err != nil {
		return sanitize("write Exam export manifest", err)
	}
	if err = writer.Close(); err != nil {
		return sanitize("close Exam export archive", err)
	}
	return nil
}

// writeExamExportFile opens one file once, copies it to its member and
// returns its manifest entry. An open failure is returned as the core
// reported it.
func writeExamExportFile(ctx context.Context, writer *zip.Writer, f plugin.ExportFile) (examExportManifestEntry, error) {
	body, err := f.Open(ctx)
	if err != nil {
		return examExportManifestEntry{}, err
	}
	size, digest, copyErr := writeExamExportMember(ctx, writer, f.Path, body, f.Size)
	closeErr := body.Close()
	if copyErr != nil || closeErr != nil {
		return examExportManifestEntry{}, sanitize("copy Exam export source", errors.Join(copyErr, closeErr))
	}
	return examExportManifestEntry{ArchivePath: f.Path, SizeBytes: size, SHA256: digest, SubmissionID: model.SubmissionID(f.SubmissionID), EntryID: model.AttemptWorkspaceEntryID(f.EntryID), OriginalPath: f.OriginalPath, MediaType: f.MediaType}, nil
}

func writeExamExportMember(ctx context.Context, archive *zip.Writer, name string, body io.Reader, expected int64) (int64, string, error) {
	h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
	h.SetMode(0600)
	member, err := archive.CreateHeader(h)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(io.MultiWriter(member, hash), io.LimitReader(workReader{ctx: ctx, reader: body}, expected+1), make([]byte, 32<<10))
	if err != nil {
		return 0, "", err
	}
	if size != expected {
		return 0, "", errors.New("Exam export member size mismatch")
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}
