// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/packages/vfs/memory"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/plugin/zipexport"
)

// goldenExportFixture builds an export input whose identities and bytes are
// fixed, so the archive it produces is byte-for-byte reproducible. It covers
// an Attempt-origin file, a Starter-origin file and an empty file.
func goldenExportFixture(t *testing.T) (*Content, exam.ExportArchiveInput) {
	t.Helper()
	content, err := New(memory.New(), Policy{MaximumConcurrentOperations: 1, ExamExportFormat: zipFormat(t)}, nil)
	require.NoError(t, err)
	ctx := context.Background()
	attemptBody := []byte("original answer bytes\x00\n")
	attempt, err := content.StageAttemptWorkspaceObject(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaa1", bytes.NewReader(attemptBody), int64(len(attemptBody)), "text/plain")
	require.NoError(t, err)
	starterBody := []byte("starter template\r\nline two\n")
	starter, err := content.StageStarterWorkspaceObject(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaa3", bytes.NewReader(starterBody), int64(len(starterBody)), "application/octet-stream")
	require.NoError(t, err)
	empty, err := content.StageAttemptWorkspaceObject(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaa4", bytes.NewReader(nil), 0, "application/octet-stream")
	require.NoError(t, err)
	input := exam.ExportArchiveInput{ExportID: "eeeeeeeeeeeeeeeeeeeeeeeee1", AttemptID: "jjjjjjjjjjjjjjjjjjjjjjjjj1"}
	input.Records = []byte(`{"export_id":"eeeeeeeeeeeeeeeeeeeeeeeee1","categories":["work"],"submissions":[]}`)
	input.Files = []exam.ExportArchiveFile{
		{SubmissionID: "sssssssssssssssssssssssss1", Entry: model.ExamSubmissionManifestEntry{EntryID: "nnnnnnnnnnnnnnnnnnnnnnnnn1", Kind: model.StarterWorkspaceEntryFile, Path: "answers/answer.txt", ContentVersion: "cccccccccccccccccccccccccc", MediaType: attempt.MediaType, SizeBytes: attempt.SizeBytes, SHA256: attempt.SHA256, StorageOrigin: model.AttemptWorkspaceStorageAttempt, AttemptObjectID: "aaaaaaaaaaaaaaaaaaaaaaaaa1"}},
		{SubmissionID: "sssssssssssssssssssssssss1", Entry: model.ExamSubmissionManifestEntry{EntryID: "nnnnnnnnnnnnnnnnnnnnnnnnn3", Kind: model.StarterWorkspaceEntryFile, Path: "starter/template.txt", ContentVersion: "cccccccccccccccccccccccccd", MediaType: starter.MediaType, SizeBytes: starter.SizeBytes, SHA256: starter.SHA256, StorageOrigin: model.AttemptWorkspaceStorageStarter, StarterObjectID: "aaaaaaaaaaaaaaaaaaaaaaaaa3"}},
		{SubmissionID: "sssssssssssssssssssssssss3", Entry: model.ExamSubmissionManifestEntry{EntryID: "nnnnnnnnnnnnnnnnnnnnnnnnn4", Kind: model.StarterWorkspaceEntryFile, Path: "empty", ContentVersion: "ccccccccccccccccccccccccce", MediaType: empty.MediaType, SizeBytes: empty.SizeBytes, SHA256: empty.SHA256, StorageOrigin: model.AttemptWorkspaceStorageAttempt, AttemptObjectID: "aaaaaaaaaaaaaaaaaaaaaaaaa4"}},
	}
	return content, input
}

// The golden values were produced by the archive writer before it became a
// plugin.ExportFormat; the zip format must keep every byte.
func TestExamExportArchiveBytesAreStable(t *testing.T) {
	content, input := goldenExportFixture(t)
	result, err := content.BuildExamExport(context.Background(), input)
	require.NoError(t, err)
	body, err := content.OpenExamExport(context.Background(), input.ExportID, input.AttemptID)
	require.NoError(t, err)
	data, err := io.ReadAll(body)
	require.NoError(t, body.Close())
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	require.Equal(t, int64(3411), result.SizeBytes)
	require.Equal(t, "9d47ff82fcf977974be10ad80828b11fc838e72a04c46d04c8a2546222ae94f6", result.SHA256)
	require.Equal(t, result.SizeBytes, int64(len(data)))
	require.Equal(t, result.SHA256, hex.EncodeToString(digest[:]))
}

// fakeExportFormat writes the raw bytes of whatever its write function
// reads, ignoring every read and close failure, so only the core's own
// verification can fail the export.
type fakeExportFormat struct {
	write func(ctx context.Context, archive plugin.ExportArchive, w io.Writer)
}

func (fakeExportFormat) MediaType() string     { return "application/octet-stream" }
func (fakeExportFormat) FileExtension() string { return ".bin" }

func (f fakeExportFormat) Write(ctx context.Context, archive plugin.ExportArchive, w io.Writer) error {
	_, _ = w.Write(archive.Records)
	f.write(ctx, archive, w)
	return nil
}

// copyExportFile opens file and copies at most limit bytes of it to w,
// ignoring failures; a negative limit copies everything.
func copyExportFile(ctx context.Context, file plugin.ExportFile, w io.Writer, limit int64) {
	body, err := file.Open(ctx)
	if err != nil {
		return
	}
	var reader io.Reader = body
	if limit >= 0 {
		reader = io.LimitReader(body, limit)
	}
	_, _ = io.Copy(w, reader)
	_ = body.Close()
}

func copyEveryExportFile(ctx context.Context, archive plugin.ExportArchive, w io.Writer) {
	for _, file := range archive.Files {
		copyExportFile(ctx, file, w, -1)
	}
}

func TestExamExportFailsWhenAFormatMisreadsAFile(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter []byte
		write func(ctx context.Context, archive plugin.ExportArchive, w io.Writer)
	}{
		{name: "skips a file", write: func(ctx context.Context, archive plugin.ExportArchive, w io.Writer) {
			copyEveryExportFile(ctx, plugin.ExportArchive{Files: archive.Files[:len(archive.Files)-1]}, w)
		}},
		{name: "opens a file twice", write: func(ctx context.Context, archive plugin.ExportArchive, w io.Writer) {
			copyEveryExportFile(ctx, archive, w)
			copyExportFile(ctx, archive.Files[0], w, -1)
		}},
		{name: "reads only part of a file", write: func(ctx context.Context, archive plugin.ExportArchive, w io.Writer) {
			copyExportFile(ctx, archive.Files[0], w, 1)
			copyEveryExportFile(ctx, plugin.ExportArchive{Files: archive.Files[1:]}, w)
		}},
		{name: "is handed altered bytes", alter: []byte("original answer bytes\x00\r"), write: copyEveryExportFile},
		{name: "is handed extra bytes", alter: []byte("original answer bytes\x00\n!"), write: copyEveryExportFile},
		{name: "is handed missing bytes", alter: []byte("original answer bytes"), write: copyEveryExportFile},
	} {
		t.Run(test.name, func(t *testing.T) {
			content, input := goldenExportFixture(t)
			if test.alter != nil {
				_, err := content.filesystem.Write(context.Background(), attemptWorkspaceObjectKey(input.Files[0].Entry.AttemptObjectID), bytes.NewReader(test.alter), vfs.WriteOptions{})
				require.NoError(t, err)
			}
			result, err := content.buildExamExport(context.Background(), input, fakeExportFormat{write: test.write})
			require.EqualError(t, err, "Exam export source verification failed")
			require.Nil(t, result)
			_, err = content.filesystem.Stat(context.Background(), examExportKey(input.ExportID, input.AttemptID))
			require.ErrorIs(t, err, vfs.ErrNotFound)
		})
	}
}

func TestExamExportAcceptsAFormatThatReadsEveryFileOnce(t *testing.T) {
	content, input := goldenExportFixture(t)
	result, err := content.buildExamExport(context.Background(), input, fakeExportFormat{write: copyEveryExportFile})
	require.NoError(t, err)
	expected := append(append(append([]byte(nil), input.Records...), "original answer bytes\x00\n"...), "starter template\r\nline two\n"...)
	digest := sha256.Sum256(expected)
	require.Equal(t, int64(len(expected)), result.SizeBytes)
	require.Equal(t, hex.EncodeToString(digest[:]), result.SHA256)
}

func TestZipExportFormatStopsAtAnAlteredFile(t *testing.T) {
	content, input := goldenExportFixture(t)
	_, err := content.filesystem.Write(context.Background(), attemptWorkspaceObjectKey(input.Files[0].Entry.AttemptObjectID), bytes.NewReader([]byte("original answer bytes\x00\r")), vfs.WriteOptions{})
	require.NoError(t, err)
	result, err := content.BuildExamExport(context.Background(), input)
	require.EqualError(t, err, "Exam export source verification failed")
	require.Nil(t, result)
}

func TestBuildExamExportWithoutAFormatIsUnavailable(t *testing.T) {
	t.Parallel()

	content, err := New(memory.New(), Policy{MaximumConcurrentOperations: 1}, nil)
	require.NoError(t, err)
	_, err = content.BuildExamExport(context.Background(), exam.ExportArchiveInput{})
	require.ErrorIs(t, err, errExamExportFormatUnavailable)
}

// formatRecordingHost keeps the export formats a plugin offers.
type formatRecordingHost struct {
	formats []plugin.ExportFormat
}

func (*formatRecordingHost) Logger() *slog.Logger      { return slog.New(slog.DiscardHandler) }
func (*formatRecordingHost) Settings() json.RawMessage { return nil }
func (*formatRecordingHost) AddIdentityProviderType(plugin.IdentityProviderType) error {
	return errors.New("not offered")
}
func (h *formatRecordingHost) AddExportFormat(format plugin.ExportFormat) error {
	h.formats = append(h.formats, format)
	return nil
}
func (*formatRecordingHost) AddEventHandler(plugin.EventTopic, plugin.EventHandler) error {
	return errors.New("not offered")
}

// zipFormat activates the built-in zip export plugin, as the server does, and
// returns the format it offers.
func zipFormat(t *testing.T) plugin.ExportFormat {
	t.Helper()
	host := &formatRecordingHost{}
	require.NoError(t, zipexport.NewPlugin().Activate(context.Background(), host))
	require.Len(t, host.formats, 1)
	return host.formats[0]
}
