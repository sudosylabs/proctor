// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"cmp"
	"context"
	"slices"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// DesktopRelease is one build of the catalogue compiled into this server.
// Newest marks the highest release for its platform and architecture.
type DesktopRelease struct {
	Build  model.DesktopBuildTuple
	Newest bool
}

// DesktopCompatibilityPolicyView is the policy with the catalogue entry of
// each revoked build this server knows; a revoked build it doesn't know has
// no entry.
type DesktopCompatibilityPolicyView struct {
	Policy        *model.DesktopCompatibilityPolicy
	RevokedBuilds map[string]model.DesktopBuildTuple
}

// ListDesktopReleases needs access_policy.view at the Institution, as reading
// the policy does. The catalogue is compiled in, so the list is short and
// complete: newest release first, then by platform, architecture and build.
func (s *Service) ListDesktopReleases(ctx context.Context, inv invocation.Invocation) ([]DesktopRelease, error) {
	if _, _, err := s.authorizeInstitutionAction(ctx, inv, model.ActionAccessPolicyView); err != nil {
		return nil, err
	}
	var entries []model.DesktopBuildTuple
	if s.desktopBuilds != nil {
		entries = s.desktopBuilds.entries
	}

	newest := map[[2]string]string{}
	for _, entry := range entries {
		target := [2]string{string(entry.Platform), string(entry.Architecture)}
		if current, seen := newest[target]; !seen || compareReleases(entry.DesktopRelease, current) > 0 {
			newest[target] = entry.DesktopRelease
		}
	}

	releases := make([]DesktopRelease, 0, len(entries))
	for _, entry := range entries {
		target := [2]string{string(entry.Platform), string(entry.Architecture)}
		releases = append(releases, DesktopRelease{Build: entry, Newest: newest[target] == entry.DesktopRelease})
	}
	slices.SortStableFunc(releases, func(left, right DesktopRelease) int {
		if order := compareReleases(right.Build.DesktopRelease, left.Build.DesktopRelease); order != 0 {
			return order
		}
		return cmp.Or(
			cmp.Compare(left.Build.Platform, right.Build.Platform),
			cmp.Compare(left.Build.Architecture, right.Build.Architecture),
			cmp.Compare(left.Build.DesktopBuildID, right.Build.DesktopBuildID),
		)
	})
	return releases, nil
}

// describePolicy looks up each revoked build in the compiled catalogue.
func (s *Service) describePolicy(policy *model.DesktopCompatibilityPolicy) *DesktopCompatibilityPolicyView {
	view := &DesktopCompatibilityPolicyView{Policy: policy, RevokedBuilds: map[string]model.DesktopBuildTuple{}}
	if s.desktopBuilds == nil {
		return view
	}
	for _, revocation := range policy.RevokedDesktopBuilds {
		for _, entry := range s.desktopBuilds.entries {
			if entry.DesktopBuildID == revocation.BuildID {
				view.RevokedBuilds[revocation.BuildID] = entry
				break
			}
		}
	}
	return view
}

// compareReleases orders catalogue releases, which the catalogue already
// validated, so a comparison error cannot occur.
func compareReleases(left, right string) int {
	order, _ := model.CompareDesktopReleases(left, right)
	return order
}

// CountOpenSittings needs Institution-wide desktop_compatibility_policy.manage:
// it tells the person who could start maintenance how many Sittings it would
// interrupt.
func (s *Service) CountOpenSittings(ctx context.Context, inv invocation.Invocation, maximum int) (int, error) {
	if _, _, err := s.authorizeInstitutionAction(ctx, inv, model.ActionDesktopCompatibilityPolicyManage); err != nil {
		return 0, err
	}
	count, err := s.desktopCompatibilityPolicies.CountOpenSittings(ctx, maximum)
	if err != nil {
		return 0, desktopCompatibilityPolicyStoreErrors.Translate(err)
	}
	return count, nil
}
