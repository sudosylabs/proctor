// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// MaximumAdministrationCount bounds each administration summary count; a
// count at the bound is a lower bound.
const MaximumAdministrationCount = 1000

// administrationFailedJobWindow is how far back the summary counts failed
// Jobs, so old failures an operator has already seen stop drawing attention.
const administrationFailedJobWindow = 7 * 24 * time.Hour

// AdministrationCount is one bounded summary count. IsLowerBound reports
// that counting stopped at MaximumAdministrationCount.
type AdministrationCount struct {
	Count        int
	IsLowerBound bool
}

// AdministrationCountsResult summarises the administration work waiting on the
// caller. A count is nil when the caller may not list that resource.
type AdministrationCountsResult struct {
	// PendingInvitations are pending Invitations that have not expired,
	// within the caller's invitation.view scopes.
	PendingInvitations *AdministrationCount
	// FailedJobs are operator-visible Jobs that failed in the last seven
	// days.
	FailedJobs *AdministrationCount
	// FailedMailDeliveries are mail deliveries in the failed terminal state.
	FailedMailDeliveries *AdministrationCount
	// OpenOnboardingImports are unexpired administrative imports still
	// uploading, parsing, awaiting commit, or executing, within the caller's
	// onboarding_batch.view scopes.
	OpenOnboardingImports *AdministrationCount
}

type administrationInvitationCounter interface {
	countPendingInvitations(context.Context, invocation.Invocation, int) (int, error)
}

// pendingInvitationCounter and openImportCounter answer the summary's
// onboarding counts through the onboarding area's Ports.
type pendingInvitationCounter func(context.Context, invocation.Invocation, int) (int, error)

func (f pendingInvitationCounter) countPendingInvitations(ctx context.Context, invocation invocation.Invocation, maximum int) (int, error) {
	return f(ctx, invocation, maximum)
}

type openImportCounter func(context.Context, invocation.Invocation, int) (int, error)

func (f openImportCounter) countOpenOnboardingImports(ctx context.Context, invocation invocation.Invocation, maximum int) (int, error) {
	return f(ctx, invocation, maximum)
}

type administrationJobCounter interface {
	countRecentJobFailures(context.Context, invocation.Invocation, time.Duration, int) (int, error)
}

type administrationMailCounter interface {
	countFailedMailDeliveries(context.Context, invocation.Invocation, int) (int, error)
}

type administrationImportCounter interface {
	countOpenOnboardingImports(context.Context, invocation.Invocation, int) (int, error)
}

// administrationCountsServiceDependencies are the collaborators of
// AdministrationCounts: the services that own each listed resource,
// so every count is authorized, audited, and scoped exactly as its list is.
type administrationCountsServiceDependencies struct {
	Invitations administrationInvitationCounter
	Jobs        administrationJobCounter
	Mail        administrationMailCounter
	Imports     administrationImportCounter
}

// AdministrationCounts answers the bounded counts behind the administration
// navigation, each through the service that owns the counted resource.
type AdministrationCounts struct {
	deps administrationCountsServiceDependencies
}

func newAdministrationCountsService(deps administrationCountsServiceDependencies) *AdministrationCounts {
	return &AdministrationCounts{deps: deps}
}

// AdministrationCounts returns the bounded counts behind the administration
// navigation. It requires an interactive Session, like the other
// administration reads. Each count runs its resource's own list
// authorization, which records the decision as that list does; a count the
// caller may not list is left out rather than failing the summary.
func (s *AdministrationCounts) AdministrationCounts(ctx context.Context, invocation invocation.Invocation) (AdministrationCountsResult, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return AdministrationCountsResult{}, access.InvalidTokenAppError()
	}
	var counts AdministrationCountsResult
	var err error
	if counts.PendingInvitations, err = administrationCount(s.deps.Invitations != nil, func() (int, error) {
		return s.deps.Invitations.countPendingInvitations(ctx, invocation, MaximumAdministrationCount)
	}); err != nil {
		return AdministrationCountsResult{}, err
	}
	if counts.FailedJobs, err = administrationCount(s.deps.Jobs != nil, func() (int, error) {
		return s.deps.Jobs.countRecentJobFailures(ctx, invocation, administrationFailedJobWindow, MaximumAdministrationCount)
	}); err != nil {
		return AdministrationCountsResult{}, err
	}
	if counts.FailedMailDeliveries, err = administrationCount(s.deps.Mail != nil, func() (int, error) {
		return s.deps.Mail.countFailedMailDeliveries(ctx, invocation, MaximumAdministrationCount)
	}); err != nil {
		return AdministrationCountsResult{}, err
	}
	if counts.OpenOnboardingImports, err = administrationCount(s.deps.Imports != nil, func() (int, error) {
		return s.deps.Imports.countOpenOnboardingImports(ctx, invocation, MaximumAdministrationCount)
	}); err != nil {
		return AdministrationCountsResult{}, err
	}
	return counts, nil
}

// administrationCount runs one count. A denial omits it; any other failure
// fails the summary, so an outage is never shown as an empty queue.
func administrationCount(available bool, count func() (int, error)) (*AdministrationCount, error) {
	if !available {
		return nil, nil
	}
	total, err := count()
	if apperror.Is(err, "authorization.denied") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if total < 0 || total > MaximumAdministrationCount {
		return nil, apperror.New("administration.unavailable").WithField("resource", "administration_counts")
	}
	return &AdministrationCount{Count: total, IsLowerBound: total == MaximumAdministrationCount}, nil
}
