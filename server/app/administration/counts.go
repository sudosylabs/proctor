// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// MaximumCount bounds each administration summary count; a
// count at the bound is a lower bound.
const MaximumCount = 1000

// failedJobWindow is how far back the summary counts failed
// Jobs, so old failures an operator has already seen stop drawing attention.
const failedJobWindow = 7 * 24 * time.Hour

// Count is one bounded summary count. IsLowerBound reports
// that counting stopped at MaximumCount.
type Count struct {
	Count        int
	IsLowerBound bool
}

// CountsResult summarises the administration work waiting on the
// caller. A count is nil when the caller may not list that resource.
type CountsResult struct {
	// PendingInvitations are pending Invitations that have not expired,
	// within the caller's invitation.view scopes.
	PendingInvitations *Count
	// FailedJobs are operator-visible Jobs that failed in the last seven
	// days.
	FailedJobs *Count
	// FailedMailDeliveries are mail deliveries in the failed terminal state.
	FailedMailDeliveries *Count
	// OpenOnboardingImports are unexpired administrative imports still
	// uploading, parsing, awaiting commit, or executing, within the caller's
	// onboarding_batch.view scopes.
	OpenOnboardingImports *Count
}

// CountsDependencies are the collaborators of Counts: one count per resource,
// each answered by the service that owns the resource, so every count is
// authorized, audited and scoped exactly as its list is. A nil count is left
// out of the summary; a reduced test graph may lack some.
type CountsDependencies struct {
	// PendingInvitations counts pending Invitations that have not expired.
	PendingInvitations func(ctx context.Context, invocation invocation.Invocation, maximum int) (int, error)
	// RecentJobFailures counts operator-visible Jobs that failed within the
	// span.
	RecentJobFailures func(ctx context.Context, invocation invocation.Invocation, within time.Duration, maximum int) (int, error)
	// FailedMailDeliveries counts mail deliveries in the failed terminal
	// state.
	FailedMailDeliveries func(ctx context.Context, invocation invocation.Invocation, maximum int) (int, error)
	// OpenOnboardingImports counts administrative imports still in progress.
	OpenOnboardingImports func(ctx context.Context, invocation invocation.Invocation, maximum int) (int, error)
}

// Counts answers the bounded counts behind the administration navigation,
// each through the service that owns the counted resource.
type Counts struct {
	deps CountsDependencies
}

// NewCounts builds the administration counts over the given counters.
func NewCounts(deps CountsDependencies) *Counts {
	return &Counts{deps: deps}
}

// AdministrationCounts returns the bounded counts behind the administration
// navigation. It requires an interactive Session, like the other
// administration reads. Each count runs its resource's own list
// authorization, which records the decision as that list does; a count the
// caller may not list is left out rather than failing the summary.
func (s *Counts) AdministrationCounts(
	ctx context.Context,
	invocation invocation.Invocation,
) (CountsResult, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return CountsResult{}, access.InvalidTokenAppError()
	}

	var counts CountsResult
	var err error
	if counts.PendingInvitations, err = boundedCount(s.deps.PendingInvitations != nil, func() (int, error) {
		return s.deps.PendingInvitations(ctx, invocation, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	if counts.FailedJobs, err = boundedCount(s.deps.RecentJobFailures != nil, func() (int, error) {
		return s.deps.RecentJobFailures(ctx, invocation, failedJobWindow, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	if counts.FailedMailDeliveries, err = boundedCount(s.deps.FailedMailDeliveries != nil, func() (int, error) {
		return s.deps.FailedMailDeliveries(ctx, invocation, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	if counts.OpenOnboardingImports, err = boundedCount(s.deps.OpenOnboardingImports != nil, func() (int, error) {
		return s.deps.OpenOnboardingImports(ctx, invocation, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	return counts, nil
}

// boundedCount runs one count. A denial omits it; any other failure
// fails the summary, so an outage is never shown as an empty queue.
func boundedCount(available bool, count func() (int, error)) (*Count, error) {
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
	if total < 0 || total > MaximumCount {
		return nil, apperror.New("administration.unavailable").WithField("resource", "administration_counts")
	}
	return &Count{Count: total, IsLowerBound: total == MaximumCount}, nil
}
