// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

type handlerFunc func(context.Context, Execution) Outcome

func (f handlerFunc) Run(ctx context.Context, execution Execution) Outcome {
	return f(ctx, execution)
}

func TestRegistryIsClosedAndImmutable(t *testing.T) {
	t.Parallel()
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome {
		return Outcome{Kind: OutcomeSucceeded, Result: json.RawMessage(`{}`)}
	}))
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	resolved, err := registry.Descriptor(model.JobTypeProfilePictureGenerateDefault)
	require.NoError(t, err, "Descriptor() = %#v", resolved)
	require.Equal(t, descriptor.Type, resolved.Type, "Descriptor() = %#v", resolved)
	_, err = registry.Descriptor(model.JobTypeMailCleanup)
	require.Error(t, err, "Descriptor() accepted an unregistered type")
}

func TestRegistryRejectsDuplicateTypesAndMissingHandlers(t *testing.T) {
	t.Parallel()
	valid := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	_, err := NewRegistry([]Descriptor{valid, valid})
	require.Error(t, err, "NewRegistry() accepted duplicate type")
	valid.Handler = nil
	_, err = NewRegistry([]Descriptor{valid})
	require.Error(t, err, "NewRegistry() accepted missing handler")
}

func TestNewDescriptorDefaultsAreAValidOperatorJob(t *testing.T) {
	t.Parallel()
	handler := handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() })
	descriptor := NewDescriptor(model.JobTypeMailCleanup, handler)
	require.Equal(t, model.JobTypeMailCleanup, descriptor.Type)
	require.NotNil(t, descriptor.Handler)
	descriptor.Handler = nil
	require.Equal(t, Descriptor{
		Type:              model.JobTypeMailCleanup,
		Timeout:           10 * time.Minute,
		Concurrency:       1,
		MaximumAttempts:   5,
		LeaseDuration:     time.Minute,
		HeartbeatInterval: 15 * time.Second,
		BaseRetryDelay:    time.Second,
		MaximumRetryDelay: time.Minute,
		Visibility:        VisibilityOperator,
		SuccessRetention:  30 * 24 * time.Hour,
		FailureRetention:  90 * 24 * time.Hour,
	}, descriptor)
	_, err := NewRegistry([]Descriptor{NewDescriptor(model.JobTypeMailCleanup, handler)})
	require.NoError(t, err)
}

func TestSplitRegistrationsKeepsRegistrationOrder(t *testing.T) {
	t.Parallel()
	descriptors, recurrences := SplitRegistrations([]Registration{
		{Descriptor: Descriptor{Type: model.JobTypeMailCleanup}, Recurrence: &Recurrence{Name: "mail-cleanup"}},
		{Descriptor: Descriptor{Type: model.JobTypeMailDeliver}},
		{Descriptor: Descriptor{Type: model.JobTypeCleanup}, Recurrence: &Recurrence{Name: "job-history-cleanup", Interval: time.Hour}},
	})
	require.Equal(t, []Descriptor{{Type: model.JobTypeMailCleanup}, {Type: model.JobTypeMailDeliver}, {Type: model.JobTypeCleanup}}, descriptors)
	require.Equal(t, []Recurrence{{Name: "mail-cleanup"}, {Name: "job-history-cleanup", Interval: time.Hour}}, recurrences)
}
