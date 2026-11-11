// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/user.go access-token lifecycle.
// Proctor requires finite lifetimes, explicit known-action scopes, recent
// interactive authentication for creation, optional academic-unit ceilings,
// hashed persistence, and durable security auditing.

package identity

import (
	"context"
	"sort"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// PersonalAccessTokenGrantableScopes is the caller's current PAT scope
// catalogue. AcademicUnits holds at most 50 delegable roots ordered by display
// name; AcademicUnitsHasMore reports that more exist. Each scope lists only the
// returned roots at which it is delegable.
type PersonalAccessTokenGrantableScopes struct {
	Scopes               []PersonalAccessTokenGrantableScope
	AcademicUnits        []*model.AcademicUnit
	AcademicUnitsHasMore bool
}

// PersonalAccessTokenGrantableScope is one action the caller may delegate.
type PersonalAccessTokenGrantableScope struct {
	Action          model.Action
	ResourceTypes   []model.ResourceType
	InstitutionWide bool
	AcademicUnitIDs []model.AcademicUnitID
}

// ListGrantablePersonalAccessTokenScopes reports which actions the interactive
// caller may currently delegate to a new PAT, and at which Academic Unit roots.
// It applies creation's delegation ceiling, so every offered placement is one
// CreatePersonalAccessToken accepts for the same principal.
func (s *Service) ListGrantablePersonalAccessTokenScopes(
	ctx context.Context,
	inv invocation.Invocation,
) (*PersonalAccessTokenGrantableScopes, error) {
	principal := inv.Principal()
	if err := requireInteractiveSession(principal, false, s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	delegation, err := s.authorizer.PersonalAccessTokenDelegation(ctx, principal)
	if err != nil {
		return nil, personalAccessTokenFailure("authorization", err)
	}

	units := make([]*model.AcademicUnit, 0, len(delegation.AcademicUnitRootIDs))
	for _, id := range delegation.AcademicUnitRootIDs {
		unit, lookupErr := s.academicUnits.Get(ctx, id)
		if lookupErr != nil {
			// The root was resolved moments ago, so a missing unit is a race
			// with archival rather than a caller error.
			return nil, apperror.New("personal_access_token.unavailable").
				WithField("resource", "academic_unit").Wrap(lookupErr)
		}

		units = append(units, unit)
	}

	sort.SliceStable(units, func(i, j int) bool {
		if units[i].DisplayName != units[j].DisplayName {
			return units[i].DisplayName < units[j].DisplayName
		}
		return units[i].ID < units[j].ID
	})
	result := &PersonalAccessTokenGrantableScopes{
		Scopes: make([]PersonalAccessTokenGrantableScope, 0, len(delegation.Actions)),
	}
	if len(units) > personalAccessTokenGrantableAcademicUnitLimit {
		units = units[:personalAccessTokenGrantableAcademicUnitLimit]
		result.AcademicUnitsHasMore = true
	}

	result.AcademicUnits = units
	listed := make(map[model.AcademicUnitID]struct{}, len(units))
	for _, unit := range units {
		listed[unit.ID] = struct{}{}
	}

	for _, action := range delegation.Actions {
		definition, ok := model.DefinitionForAction(action.Action)
		if !ok {
			continue
		}

		scope := PersonalAccessTokenGrantableScope{
			Action:          action.Action,
			ResourceTypes:   definition.ResourceTypes(),
			InstitutionWide: action.InstitutionWide,
			AcademicUnitIDs: []model.AcademicUnitID{},
		}
		for _, id := range action.AcademicUnitIDs {
			if _, ok := listed[id]; ok {
				scope.AcademicUnitIDs = append(scope.AcademicUnitIDs, id)
			}
		}

		result.Scopes = append(result.Scopes, scope)
	}

	return result, nil
}
