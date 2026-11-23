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
	"io"
	"os"

	"github.com/sudosylabs/proctor/packages/vfs"
)

// An export file is stored under a key naming its export and the Job attempt
// that wrote it. Every kind of export writes, verifies, opens and removes its
// files the same way; only the key and the label in errors differ.

// uncertainExportWrite reports a write whose backend outcome is unknown, so
// cleanup must keep the attempt's key and check it again. It answers both the
// exam export's contract and the shared one.
type uncertainExportWrite struct {
	label string
	cause error
}

func (e *uncertainExportWrite) Error() string           { return e.label + " write outcome is uncertain" }
func (e *uncertainExportWrite) Unwrap() error           { return e.cause }
func (*uncertainExportWrite) ExportWriteUncertain()     {}
func (*uncertainExportWrite) ExamExportWriteUncertain() {}

// storeVerifiedExport writes spool's size bytes under key and re-reads them.
// A write error can still mean the write committed, and a success is not
// proof; only the re-read decides.
func (c *Content) storeVerifiedExport(ctx context.Context, label, key string, spool *os.File, size int64, digest string) error {
	conditional := c.filesystem.Capabilities().ConditionalWrite
	if !conditional {
		matching, verifyErr := c.verifyExport(ctx, key, size, digest)
		if verifyErr == nil {
			if matching {
				return nil
			}
			return errors.New(label + " object conflict")
		}

		if !errors.Is(verifyErr, vfs.ErrNotFound) {
			return sanitize("inspect "+label+" object", verifyErr)
		}
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return sanitize("rewind "+label+" spool", err)
	}

	_, writeErr := c.filesystem.Write(ctx, key, workReader{ctx: ctx, reader: spool}, vfs.WriteOptions{Size: &size, NoOverwrite: conditional})
	matching, verifyErr := c.verifyExport(ctx, key, size, digest)
	if verifyErr != nil || !matching {
		if writeErr != nil {
			return &uncertainExportWrite{label: label, cause: sanitize("store "+label+" archive", writeErr)}
		}
		if verifyErr != nil {
			return sanitize("verify "+label+" archive", verifyErr)
		}

		return errors.New(label + " archive verification failed")
	}

	return nil
}

func (c *Content) verifyExport(ctx context.Context, key string, size int64, digest string) (bool, error) {
	file, err := c.filesystem.Open(ctx, key, vfs.OpenOptions{})
	if err != nil {
		return false, err
	}
	defer file.Body.Close()
	if file.Info.Size != size {
		return false, nil
	}
	hash := sha256.New()
	read, err := io.CopyBuffer(hash, io.LimitReader(workReader{ctx: ctx, reader: file.Body}, size+1), make([]byte, 32<<10))
	return err == nil && read == size && hex.EncodeToString(hash.Sum(nil)) == digest, err
}

// hashSpool rewinds spool and returns its size and SHA-256.
func hashSpool(ctx context.Context, label string, spool *os.File) (int64, string, error) {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return 0, "", sanitize("rewind "+label+" spool", err)
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(hash, workReader{ctx: ctx, reader: spool}, make([]byte, 32<<10))
	if err != nil {
		return 0, "", sanitize("hash "+label+" archive", err)
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func (c *Content) openExport(ctx context.Context, label, key string) (io.ReadCloser, error) {
	file, err := c.filesystem.Open(ctx, key, vfs.OpenOptions{})
	if err != nil {
		return nil, sanitize("open "+label+" archive", err)
	}
	return file.Body, nil
}

// purgeExport removes key and succeeds only once its absence is observed.
func (c *Content) purgeExport(ctx context.Context, label, key string) error {
	if err := c.filesystem.Remove(ctx, key, vfs.RemoveOptions{}); err != nil && !errors.Is(err, vfs.ErrNotFound) {
		return sanitize("remove "+label+" archive", err)
	}
	_, err := c.filesystem.Stat(ctx, key)
	if errors.Is(err, vfs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return sanitize("observe "+label+" absence", err)
	}
	return errors.New(label + " archive remains present")
}
