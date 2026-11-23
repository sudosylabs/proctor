// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// academicUnitAdministratorsListed is how many administrators a unit names
// before counting the rest.
const academicUnitAdministratorsListed = 3

// AcademicUnitSummaries describe the listed units. MemberCounts has an entry
// only for a unit the caller holds academic_unit.members.view on, and
// Administrators only for one they hold role_binding.view on, so neither
// shows anything the member or role binding listings would not. A unit with
// no administrator has an empty entry. Counts are capped at
// store.MaximumListCount.
type AcademicUnitSummaries struct {
	MemberCounts   map[model.AcademicUnitID]store.AcademicUnitMemberCount
	Administrators map[model.AcademicUnitID]store.AcademicUnitAdministrators
}

// AcademicUnitPage is one page of units with their summaries.
type AcademicUnitPage struct {
	paging.Page[*model.AcademicUnit]
	AcademicUnitSummaries
}

// AcademicUnitView is one unit with its summary.
type AcademicUnitView struct {
	Unit *model.AcademicUnit
	AcademicUnitSummaries
}

// summarizeAcademicUnits reads each grant once for the page and records no
// decision, since the listing's academic_unit.view decision is already
// recorded. Each summary is one read for the whole page.
func (s *Service) summarizeAcademicUnits(
	ctx context.Context,
	inv invocation.Invocation,
	units []*model.AcademicUnit,
) (AcademicUnitSummaries, error) {
	summaries := AcademicUnitSummaries{}
	if len(units) == 0 {
		return summaries, nil
	}

	at := model.TimeUTC(s.now())
	ids := make([]model.AcademicUnitID, 0, len(units))
	for _, unit := range units {
		ids = append(ids, unit.ID)
	}

	counted, err := s.unitsGranted(ctx, inv, model.ActionAcademicUnitMembersView, model.ResourceAcademicUnit, ids, at)
	if err != nil {
		return summaries, err
	}
	if len(counted) > 0 {
		counts, countErr := s.academicUnitMembers.CountCurrentByAcademicUnits(ctx, store.AcademicUnitMemberCountOptions{
			AcademicUnitIDs: counted, ActiveAt: at, Maximum: store.MaximumListCount,
		})
		if countErr != nil {
			return summaries, AcademicUnitMemberStoreErrors.Translate(countErr)
		}
		summaries.MemberCounts = make(map[model.AcademicUnitID]store.AcademicUnitMemberCount, len(counts))
		for _, count := range counts {
			summaries.MemberCounts[count.AcademicUnitID] = count
		}
	}

	named, err := s.unitsGranted(ctx, inv, model.ActionRoleBindingView, model.ResourceAcademicUnit, ids, at)
	if err != nil || len(named) == 0 {
		return summaries, err
	}
	administrators, err := s.roleBindings.ListAcademicUnitAdministrators(ctx, store.AcademicUnitAdministratorOptions{
		AcademicUnitIDs: named, ActiveAt: at, Limit: academicUnitAdministratorsListed,
	})
	if err != nil {
		return summaries, academicUnitStoreErrors.Translate(err)
	}
	summaries.Administrators = make(map[model.AcademicUnitID]store.AcademicUnitAdministrators, len(named))
	for _, id := range named {
		summaries.Administrators[id] = store.AcademicUnitAdministrators{AcademicUnitID: id}
	}
	for _, listed := range administrators {
		summaries.Administrators[listed.AcademicUnitID] = listed
	}
	return summaries, nil
}

// unitsGranted returns the listed units the caller holds action on now: all
// of them for an institution-wide grant, else those inside a granted subtree.
func (s *Service) unitsGranted(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	resourceType model.ResourceType,
	ids []model.AcademicUnitID,
	at time.Time,
) ([]model.AcademicUnitID, error) {
	grant, err := s.authorizer.GrantedScopesAt(ctx, inv.Principal(), action, resourceType, at)
	if err != nil {
		return nil, err
	}
	if grant.InstitutionWide {
		return ids, nil
	}
	if len(grant.AcademicUnitRootIDs) == 0 {
		return nil, nil
	}
	within, err := s.academicUnits.ListWithinSubtrees(ctx, ids, grant.AcademicUnitRootIDs)
	if err != nil {
		return nil, academicUnitStoreErrors.Translate(err)
	}
	return within, nil
}
