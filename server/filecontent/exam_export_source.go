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
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"sync"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
)

// writeExamExport hands format the archive and fails unless format read
// every file exactly once, to its end, with the recorded size and digest.
// A failure a source recorded wins over format's own error, so the export
// fails with the error the source reported.
func (c *Content) writeExamExport(ctx context.Context, input exam.ExportArchiveInput, format plugin.ExportFormat, w io.Writer) error {
	sources := make([]*examExportSource, len(input.Files))
	archive := plugin.ExportArchive{ExportID: input.ExportID.String(), Records: input.Records, Files: make([]plugin.ExportFile, len(input.Files))}
	for i, f := range input.Files {
		sources[i] = &examExportSource{content: c, work: ctx, file: f}
		archive.Files[i] = plugin.ExportFile{
			Path: examExportFileMember(f), SubmissionID: f.SubmissionID.String(), EntryID: f.Entry.EntryID.String(),
			OriginalPath: f.Entry.Path, MediaType: f.Entry.MediaType, Size: f.Entry.SizeBytes, SHA256: f.Entry.SHA256,
			Open: sources[i].open,
		}
	}
	writeErr := format.Write(ctx, archive, w)
	var failure error
	verified := true
	for _, source := range sources {
		sourceFailure, sourceVerified := source.seal()
		if failure == nil {
			failure = sourceFailure
		}
		verified = verified && sourceVerified
	}
	switch {
	case failure != nil:
		return failure
	case writeErr != nil:
		return writeErr
	case !verified:
		return errExamExportSourceVerification()
	}
	return nil
}

func errExamExportSourceVerification() error {
	return errors.New("Exam export source verification failed")
}

// examExportSource opens one file's object at most once and verifies the
// bytes a format reads from it. Reads are limited to Size+1 bytes and
// observe the build's cancellation. At EOF the source records whether the
// size and SHA-256 matched; Close reports a digest mismatch so a format
// stops at the altered file. After the format returns, the source is sealed:
// it closes a body the format left open and refuses further use.
type examExportSource struct {
	content *Content
	work    context.Context
	file    exam.ExportArchiveFile

	mu       sync.Mutex
	opened   bool
	sealed   bool
	closed   bool
	body     io.ReadCloser
	limited  io.Reader
	hash     hash.Hash
	read     int64
	eof      bool
	verified bool
	// failure is the first failure this source reported to the format.
	failure error
}

func (s *examExportSource) open(ctx context.Context) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened || s.sealed {
		return nil, s.fail(errExamExportSourceVerification())
	}
	s.opened = true
	var body io.ReadCloser
	var err error
	switch s.file.Entry.StorageOrigin {
	case model.AttemptWorkspaceStorageStarter:
		body, err = s.content.OpenStarterWorkspaceObject(ctx, s.file.Entry.StarterObjectID)
	case model.AttemptWorkspaceStorageAttempt:
		body, err = s.content.OpenAttemptWorkspaceObject(ctx, s.file.Entry.AttemptObjectID)
	default:
		return nil, s.fail(errors.New("invalid Exam export storage origin"))
	}
	if err != nil {
		return nil, s.fail(sanitize("open Exam export source", err))
	}
	s.body = body
	s.limited = io.LimitReader(workReader{ctx: s.work, reader: body}, s.file.Entry.SizeBytes+1)
	s.hash = sha256.New()
	return examExportSourceReader{source: s}, nil
}

// fail records err as the source's failure unless one is already recorded,
// and returns the recorded failure.
func (s *examExportSource) fail(err error) error {
	if s.failure == nil {
		s.failure = err
	}
	return s.failure
}

// seal ends the format's use of the source and reports its first failure
// and whether the format read every byte with a match.
func (s *examExportSource) seal() (failure error, verified bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = true
	if s.body != nil && !s.closed {
		s.closed = true
		if err := s.body.Close(); err != nil {
			_ = s.fail(sanitize("copy Exam export source", err))
		}
	}
	return s.failure, s.verified
}

type examExportSourceReader struct{ source *examExportSource }

var errExamExportSourceClosed = errors.New("Exam export source is closed")

func (r examExportSourceReader) Read(p []byte) (int, error) {
	s := r.source
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sealed {
		return 0, errExamExportSourceClosed
	}
	n, err := s.limited.Read(p)
	s.hash.Write(p[:n])
	s.read += int64(n)
	if errors.Is(err, io.EOF) && !s.eof {
		s.eof = true
		s.verified = s.read == s.file.Entry.SizeBytes && hex.EncodeToString(s.hash.Sum(nil)) == s.file.Entry.SHA256
	}
	return n, err
}

func (r examExportSourceReader) Close() error {
	s := r.source
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.body.Close(); err != nil {
		return err
	}
	if s.eof && s.read == s.file.Entry.SizeBytes && !s.verified {
		return s.fail(errExamExportSourceVerification())
	}
	return nil
}
