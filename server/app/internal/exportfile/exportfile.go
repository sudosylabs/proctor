// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package exportfile holds the two steps every export's use cases share
// around its stored file: telling the store a build's writer has returned,
// and opening a ready file without racing its expiry or replacement.
package exportfile

import (
	"context"
	"errors"
	"io"
	"time"
)

// FinishWriter records that a build's writer has returned, unless buildErr
// says the write's outcome is unknown: then the attempt's key must stay
// reserved for cleanup to check again. It returns the finish error, which the
// caller reports only when the build itself succeeded.
func FinishWriter(ctx context.Context, buildErr error, uncertain func(error) bool, finish func(context.Context) error) error {
	if uncertain(buildErr) {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return finish(cleanup)
}

// OpenStable opens the file read names, then reads again before returning
// it, because opening can block across expiry, an authority change or a
// republication. read returns the record and its file identity, failing when
// the file is not ready; unavailable wraps a failure to open.
func OpenStable[R any, A comparable](
	ctx context.Context,
	read func(context.Context) (R, A, error),
	open func(context.Context, A) (io.ReadCloser, error),
	unavailable func(error) error,
) (R, io.ReadCloser, error) {
	var zero R
	_, artifact, err := read(ctx)
	if err != nil {
		return zero, nil, err
	}

	body, err := open(ctx, artifact)
	if err != nil {
		return zero, nil, unavailable(err)
	}
	if body == nil {
		return zero, nil, unavailable(errors.New("missing export body"))
	}

	current, currentArtifact, err := read(ctx)
	if err == nil && currentArtifact != artifact {
		err = unavailable(errors.New("export artifact changed during open"))
	}
	if err != nil {
		_ = body.Close()
		return zero, nil, err
	}

	return current, body, nil
}
