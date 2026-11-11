// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/packages/vfs"
)

// WrapVFS returns next with every operation timed and its outcome, object
// size, stream completion and transferred bytes recorded under the configured
// backend name. It adds measurement only; Close closes next when next can be
// closed.
func WrapVFS(next vfs.FileSystem, backend string, module *Module) vfs.FileSystem {
	return &measuredVFS{next: next, backend: backend, metrics: module}
}

type measuredVFS struct {
	next    vfs.FileSystem
	backend string
	metrics *Module
}

func (m *measuredVFS) Capabilities() vfs.Capabilities { return m.next.Capabilities() }
func (m *measuredVFS) CheckRoot(ctx context.Context) error {
	started := time.Now()
	err := m.next.CheckRoot(ctx)
	m.metrics.ObserveVFSResult(m.backend, "check_root", vfsOutcome(err), time.Since(started))
	return err
}
func (m *measuredVFS) Open(ctx context.Context, path string, options vfs.OpenOptions) (*vfs.File, error) {
	started := time.Now()
	value, err := m.next.Open(ctx, path, options)
	m.metrics.ObserveVFSResult(m.backend, "open", vfsOutcome(err), time.Since(started))
	if err == nil && value != nil {
		m.metrics.ObserveVFSObject(m.backend, "open", value.Info.Size)
		value.Body = newMeasuredReadCloser(value.Body, func(bytes int) {
			m.metrics.AddVFSBytes(m.backend, "read", int64(bytes))
		}, func(result string) {
			m.metrics.ObserveVFSStream(m.backend, result)
		})
	}
	return value, err
}
func (m *measuredVFS) Write(ctx context.Context, path string, body io.Reader, options vfs.WriteOptions) (vfs.Info, error) {
	started := time.Now()
	if body == nil {
		value, err := m.next.Write(ctx, path, nil, options)
		m.metrics.ObserveVFSResult(m.backend, "write", vfsOutcome(err), time.Since(started))
		return value, err
	}
	counter := &countingReader{reader: body}
	value, err := m.next.Write(ctx, path, counter, options)
	m.metrics.ObserveVFSResult(m.backend, "write", vfsOutcome(err), time.Since(started))
	m.metrics.AddVFSBytes(m.backend, "write", counter.bytes)
	if err == nil {
		m.metrics.ObserveVFSObject(m.backend, "write", value.Size)
	}
	return value, err
}
func (m *measuredVFS) Stat(ctx context.Context, path string) (vfs.Info, error) {
	started := time.Now()
	value, err := m.next.Stat(ctx, path)
	m.metrics.ObserveVFSResult(m.backend, "stat", vfsOutcome(err), time.Since(started))
	if err == nil {
		m.metrics.ObserveVFSObject(m.backend, "stat", value.Size)
	}
	return value, err
}
func (m *measuredVFS) Remove(ctx context.Context, path string, options vfs.RemoveOptions) error {
	started := time.Now()
	err := m.next.Remove(ctx, path, options)
	m.metrics.ObserveVFSResult(m.backend, "remove", vfsOutcome(err), time.Since(started))
	return err
}
func (m *measuredVFS) List(ctx context.Context, options vfs.ListOptions) (vfs.Page, error) {
	started := time.Now()
	value, err := m.next.List(ctx, options)
	m.metrics.ObserveVFSResult(m.backend, "list", vfsOutcome(err), time.Since(started))
	if err == nil {
		m.metrics.ObserveVFSList(m.backend, len(value.Entries))
	}
	return value, err
}
func (m *measuredVFS) Copy(ctx context.Context, source, destination string, options vfs.TransferOptions) (vfs.Info, error) {
	started := time.Now()
	value, err := m.next.Copy(ctx, source, destination, options)
	m.metrics.ObserveVFSResult(m.backend, "copy", vfsOutcome(err), time.Since(started))
	if err == nil {
		m.metrics.ObserveVFSObject(m.backend, "copy", value.Size)
	}
	return value, err
}
func (m *measuredVFS) Move(ctx context.Context, source, destination string, options vfs.TransferOptions) (vfs.Info, error) {
	started := time.Now()
	value, err := m.next.Move(ctx, source, destination, options)
	m.metrics.ObserveVFSResult(m.backend, "move", vfsOutcome(err), time.Since(started))
	if err == nil {
		m.metrics.ObserveVFSObject(m.backend, "move", value.Size)
	}
	return value, err
}

func (m *measuredVFS) Close() error {
	if closer, ok := m.next.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func vfsOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, vfs.ErrNotFound):
		return "not_found"
	case errors.Is(err, vfs.ErrAlreadyExists):
		return "already_exists"
	case errors.Is(err, vfs.ErrConflict):
		return "conflict"
	case errors.Is(err, vfs.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, vfs.ErrInvalidPath), errors.Is(err, vfs.ErrInvalidRange):
		return "invalid"
	case errors.Is(err, vfs.ErrIsDirectory):
		return "is_directory"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.bytes += int64(count)
	return count, err
}

type measuredReadCloser struct {
	next     io.ReadCloser
	addBytes func(int)
	finish   func(string)
	once     sync.Once
}

func newMeasuredReadCloser(next io.ReadCloser, addBytes func(int), finish func(string)) io.ReadCloser {
	if next == nil {
		return nil
	}
	return &measuredReadCloser{next: next, addBytes: addBytes, finish: finish}
}

func (r *measuredReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.next.Read(buffer)
	if count > 0 && r.addBytes != nil {
		r.addBytes(count)
	}
	if err != nil {
		result := "error"
		if errors.Is(err, io.EOF) {
			result = "complete"
		}
		r.complete(result)
	}
	return count, err
}

func (r *measuredReadCloser) Close() error {
	err := r.next.Close()
	if err != nil {
		r.complete("error")
	} else {
		r.complete("closed")
	}
	return err
}

func (r *measuredReadCloser) complete(result string) {
	r.once.Do(func() {
		if r.finish != nil {
			r.finish(result)
		}
	})
}

var _ vfs.FileSystem = (*measuredVFS)(nil)
