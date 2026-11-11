// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type compositionCloser struct {
	name   string
	events *[]string
	err    error
}

func (c compositionCloser) close() error {
	*c.events = append(*c.events, c.name)
	return c.err
}

type compositionPlatform struct{ closer compositionCloser }

func (p compositionPlatform) Start(context.Context) error { return nil }
func (p compositionPlatform) Close(context.Context) error { return p.closer.close() }

type compositionMetrics struct{ closer compositionCloser }

func (m compositionMetrics) Start(context.Context) error { return nil }
func (m compositionMetrics) Close(context.Context) error { return m.closer.close() }
func (m compositionMetrics) Failures() <-chan error      { return nil }
func (m compositionMetrics) SetReady(bool)               {}

// TestCompositionReleaseClosesRegisteredOwnersInOrder covers the release step
// every failed consumer composition goes through. Which consumers each failing
// phase registers is proven on the real graph by
// TestConsumerConstructionFailuresUnwindExactlyOnce.
func TestCompositionReleaseClosesRegisteredOwnersInOrder(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		transport bool
		websocket bool
		want      []string
	}{
		{name: "before any consumer", want: []string{"metrics", "platform"}},
		{name: "after the WebSocket hub", websocket: true, want: []string{"websocket", "metrics", "platform"}},
		{
			name: "after the HTTP transport", transport: true, websocket: true,
			want: []string{"transport", "websocket", "metrics", "platform"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := []string{}
			owner := func(name string) compositionCloser {
				return compositionCloser{name: name, events: &events, err: errors.New(name + " cleanup failed")}
			}
			platformCloser, metricsCloser := owner("platform"), owner("metrics")
			transportCloser, websocketCloser := owner("transport"), owner("websocket")
			release := compositionRelease{
				platform:        compositionPlatform{closer: platformCloser},
				shutdownTimeout: time.Minute,
				metrics:         compositionMetrics{closer: metricsCloser},
			}
			if test.websocket {
				release.websocket = func(context.Context) error { return websocketCloser.close() }
			}
			if test.transport {
				release.transport = transportCloser.close
			}
			cause := errors.New("construction failed")

			err := release.fail(cause)

			// Ordering contract: the transport closes before the WebSocket hub
			// it borrows, then metrics, then Platform, each exactly once.
			require.Equal(t, test.want, events, "release order")
			require.ErrorIs(t, err, cause, "construction failure")
			for _, closer := range []compositionCloser{platformCloser, metricsCloser, transportCloser, websocketCloser} {
				if slices.Contains(test.want, closer.name) {
					require.ErrorIs(t, err, closer.err, "%s cleanup error", closer.name)
				} else {
					require.NotErrorIs(t, err, closer.err, "%s cleanup error", closer.name)
				}
			}
		})
	}
}
