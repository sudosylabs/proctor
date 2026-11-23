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

// MaximumCount bounds each administration count; a count at the bound is a
// lower bound.
const MaximumCount = 1000

// failedJobWindow keeps old failures an operator has already seen from
// drawing attention.
const failedJobWindow = 7 * 24 * time.Hour

// Count is one bounded count. IsLowerBound reports that counting stopped at
// MaximumCount.
type Count struct {
	Count        int
	IsLowerBound bool
}

// CountsResult summarizes the administration work waiting on the caller. A
// count is nil when the caller may not list that resource.
type CountsResult struct {
	// PendingInvitations are unexpired pending invitations within the
	// caller's invitation.view scopes.
	PendingInvitations *Count
	// FailedJobs are operator-visible jobs that failed in the last seven
	// days.
	FailedJobs           *Count
	FailedMailDeliveries *Count
	// OpenOnboardingImports are unexpired imports not yet finished, within
	// the caller's onboarding_batch.view scopes.
	OpenOnboardingImports *Count
	// OpenSittings are the Institution's Sittings under way, for a holder of
	// Institution-wide desktop_compatibility_policy.manage.
	OpenSittings *Count
}

// CountsDependencies are one count per resource, each answered by the service
// that owns it, so every count is authorized, audited and scoped exactly as
// its list is. A nil count is left out of the summary.
type CountsDependencies struct {
	PendingInvitations    func(ctx context.Context, inv invocation.Invocation, maximum int) (int, error)
	RecentJobFailures     func(ctx context.Context, inv invocation.Invocation, within time.Duration, maximum int) (int, error)
	FailedMailDeliveries  func(ctx context.Context, inv invocation.Invocation, maximum int) (int, error)
	OpenOnboardingImports func(ctx context.Context, inv invocation.Invocation, maximum int) (int, error)
	OpenSittings          func(ctx context.Context, inv invocation.Invocation, maximum int) (int, error)
}

// Counts answers the bounded counts behind the administration navigation.
type Counts struct {
	deps CountsDependencies
}

// NewCounts builds Counts over deps; nil counters are left out.
func NewCounts(deps CountsDependencies) *Counts {
	return &Counts{deps: deps}
}

// AdministrationCounts requires an interactive session, like the other
// administration reads. Each count runs its own list authorization; a count
// the caller may not list is left out rather than failing the summary.
func (s *Counts) AdministrationCounts(
	ctx context.Context,
	inv invocation.Invocation,
) (CountsResult, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return CountsResult{}, access.InvalidTokenAppError()
	}

	var counts CountsResult
	var err error
	if counts.PendingInvitations, err = boundedCount(s.deps.PendingInvitations != nil, func() (int, error) {
		return s.deps.PendingInvitations(ctx, inv, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	if counts.FailedJobs, err = boundedCount(s.deps.RecentJobFailures != nil, func() (int, error) {
		return s.deps.RecentJobFailures(ctx, inv, failedJobWindow, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	if counts.FailedMailDeliveries, err = boundedCount(s.deps.FailedMailDeliveries != nil, func() (int, error) {
		return s.deps.FailedMailDeliveries(ctx, inv, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	if counts.OpenOnboardingImports, err = boundedCount(s.deps.OpenOnboardingImports != nil, func() (int, error) {
		return s.deps.OpenOnboardingImports(ctx, inv, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	if counts.OpenSittings, err = boundedCount(s.deps.OpenSittings != nil, func() (int, error) {
		return s.deps.OpenSittings(ctx, inv, MaximumCount)
	}); err != nil {
		return CountsResult{}, err
	}

	return counts, nil
}

// boundedCount omits a denied count; any other failure fails the summary, so
// an outage is never shown as an empty queue.
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
