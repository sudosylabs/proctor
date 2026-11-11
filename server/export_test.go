// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import "context"

// ErrDurableJobRuntimeUnavailable lets external tests match the missing Job
// runtime failure.
var ErrDurableJobRuntimeUnavailable = errDurableJobRuntimeUnavailable

// ReleasedConsumers names the closable consumers a failed composition had
// registered for release when it failed.
type ReleasedConsumers struct {
	Transport bool
	WebSocket bool
}

// NewForTestingObservingRelease is NewForTesting for compositions expected to
// fail while constructing consumers. It also returns one entry per release of
// a failed consumer composition.
func NewForTestingObservingRelease(
	ctx context.Context,
	overrides TestingOverrides,
) (*TestingRuntime, []ReleasedConsumers, error) {
	input := testingCompositionInput(overrides)
	var releases []ReleasedConsumers
	input.observeRelease = func(release compositionRelease) {
		releases = append(releases, ReleasedConsumers{
			Transport: release.transport != nil,
			WebSocket: release.websocket != nil,
		})
	}
	result, err := composeNode(ctx, input)
	if err != nil {
		return nil, releases, err
	}
	return &TestingRuntime{
		Server:      result.server,
		Application: result.application,
		Handler:     result.handler,
	}, releases, nil
}
