// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// administrationCounterFake answers one count, or an error, and records the
// bound and window it was asked for.
type administrationCounterFake struct {
	total   int
	err     error
	maximum int
	within  time.Duration
}

func (f *administrationCounterFake) count(maximum int) (int, error) {
	f.maximum = maximum
	return f.total, f.err
}

func (f *administrationCounterFake) countPendingInvitations(_ context.Context, _ invocation.Invocation, maximum int) (int, error) {
	return f.count(maximum)
}

func (f *administrationCounterFake) countRecentJobFailures(_ context.Context, _ invocation.Invocation, within time.Duration, maximum int) (int, error) {
	f.within = within
	return f.count(maximum)
}

func (f *administrationCounterFake) countFailedMailDeliveries(_ context.Context, _ invocation.Invocation, maximum int) (int, error) {
	return f.count(maximum)
}

func (f *administrationCounterFake) countOpenOnboardingImports(_ context.Context, _ invocation.Invocation, maximum int) (int, error) {
	return f.count(maximum)
}

func administrationCountsSession() invocation.Invocation {
	return invocation.New(mailTestPrincipal(time.Now()), model.RequestMetadata{})
}

func TestAdministrationCountsOmitsWhatTheCallerMayNotList(t *testing.T) {
	t.Parallel()
	invitations := &administrationCounterFake{total: 12}
	jobs := &administrationCounterFake{err: access.DeniedError()}
	mail := &administrationCounterFake{total: MaximumAdministrationCount}
	imports := &administrationCounterFake{total: 0}
	service := newAdministrationCountsService(administrationCountsServiceDependencies{Invitations: invitations, Jobs: jobs, Mail: mail, Imports: imports})

	counts, err := service.AdministrationCounts(context.Background(), administrationCountsSession())
	require.NoError(t, err)
	require.Equal(t, AdministrationCountsResult{
		PendingInvitations:    &AdministrationCount{Count: 12},
		FailedMailDeliveries:  &AdministrationCount{Count: MaximumAdministrationCount, IsLowerBound: true},
		OpenOnboardingImports: &AdministrationCount{},
	}, counts, "a denied count is omitted and a count at the bound is a lower bound")
	for _, counter := range []*administrationCounterFake{invitations, jobs, mail, imports} {
		require.Equal(t, MaximumAdministrationCount, counter.maximum)
	}
	require.Equal(t, 7*24*time.Hour, jobs.within, "failed Jobs are counted over the last week")
}

func TestAdministrationCountsFailsWhenACountIsUnavailable(t *testing.T) {
	t.Parallel()
	unavailable := &administrationCounterFake{err: apperror.New("mail.unavailable")}
	service := newAdministrationCountsService(administrationCountsServiceDependencies{
		Invitations: &administrationCounterFake{total: 1}, Jobs: &administrationCounterFake{}, Mail: unavailable, Imports: &administrationCounterFake{},
	})
	_, err := service.AdministrationCounts(context.Background(), administrationCountsSession())
	require.True(t, apperror.Is(err, "mail.unavailable"), "an outage never reads as an empty queue: %v", err)

	overflow := newAdministrationCountsService(administrationCountsServiceDependencies{Jobs: &administrationCounterFake{total: MaximumAdministrationCount + 1}})
	_, err = overflow.AdministrationCounts(context.Background(), administrationCountsSession())
	require.True(t, apperror.Is(err, "administration.unavailable"), "a count past the bound is refused: %v", err)
}

func TestAdministrationCountsRequiresAnInteractiveSession(t *testing.T) {
	t.Parallel()
	counter := &administrationCounterFake{total: 1}
	service := newAdministrationCountsService(administrationCountsServiceDependencies{Invitations: counter})
	principal := mailTestPrincipal(time.Now())
	principal.CredentialType = model.CredentialPersonalAccessToken
	_, err := service.AdministrationCounts(context.Background(), invocation.New(principal, model.RequestMetadata{}))
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "a Personal Access Token is refused: %v", err)
	require.Zero(t, counter.maximum, "no count runs for a refused credential")

	counts, err := newAdministrationCountsService(administrationCountsServiceDependencies{}).AdministrationCounts(context.Background(), administrationCountsSession())
	require.NoError(t, err)
	require.Equal(t, AdministrationCountsResult{}, counts, "a graph without the services counts nothing")
}
