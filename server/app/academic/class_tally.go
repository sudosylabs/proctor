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

// StructureCounts are the capped counts beside a page of programmes, levels
// or periods, each keyed by the listed item's ID and capped at
// store.MaximumListCount. A nil map means the caller may not see that count
// at all: Levels needs programme_level.view, Classes counts only the classes
// the caller holds class.view on, and Students only the current students of
// classes they hold class.members.view on, as a class's student_count does.
type StructureCounts struct {
	Levels   map[string]int
	Classes  map[string]int
	Students map[string]int
}

// tallyClasses fills Classes and, when students is set, Students for the
// listed containers, one read each. The grants are read once for the page and
// record no decision, since the listing's own decision is already recorded.
func (s *Service) tallyClasses(
	ctx context.Context,
	inv invocation.Invocation,
	by store.ClassTallyContainer,
	ids []string,
	students bool,
	counts *StructureCounts,
) error {
	if len(ids) == 0 {
		return nil
	}
	at := model.TimeUTC(s.now())

	var err error
	counts.Classes, err = s.tallyVisible(ctx, inv, model.ActionClassView, by, ids, time.Time{}, at)
	if err != nil || !students {
		return err
	}
	counts.Students, err = s.tallyVisible(ctx, inv, model.ActionClassMembersView, by, ids, at, at)
	return err
}

func (s *Service) tallyVisible(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
	by store.ClassTallyContainer,
	ids []string,
	studentsAt, at time.Time,
) (map[string]int, error) {
	grant, err := s.authorizer.GrantedScopesAt(ctx, inv.Principal(), action, model.ResourceClass, at)
	if err != nil {
		return nil, err
	}
	if !grant.InstitutionWide && len(grant.AcademicUnitRootIDs) == 0 && len(grant.ClassIDs) == 0 {
		return nil, nil
	}
	tallies, err := s.classes.Tally(ctx, store.ClassTallyOptions{
		By:           by,
		ContainerIDs: ids,
		Visibility: store.ClassVisibilityScope{
			InstitutionWide:     grant.InstitutionWide,
			AcademicUnitRootIDs: grant.AcademicUnitRootIDs,
			ClassIDs:            grant.ClassIDs,
		},
		StudentsAt: studentsAt,
		Maximum:    store.MaximumListCount,
	})
	if err != nil {
		return nil, classStoreErrors.Translate(err)
	}
	counted := make(map[string]int, len(tallies))
	for _, tally := range tallies {
		counted[tally.ContainerID] = tally.Count
	}
	return counted, nil
}

// countProgrammeLevels fills Levels when the caller may list the levels of
// every programme of the unit; every programme on one page shares its unit.
func (s *Service) countProgrammeLevels(
	ctx context.Context,
	inv invocation.Invocation,
	unitID model.AcademicUnitID,
	programmes []*model.Programme,
	counts *StructureCounts,
) error {
	if len(programmes) == 0 {
		return nil
	}
	// A grant narrower than the unit, such as one Class's, may reach only
	// some programmes, so it shows no level count rather than guess.
	granted, err := s.unitsGranted(ctx, inv, model.ActionProgrammeLevelView, model.ResourceProgramme, []model.AcademicUnitID{unitID}, model.TimeUTC(s.now()))
	if err != nil || len(granted) == 0 {
		return err
	}
	ids := make([]model.ProgrammeID, 0, len(programmes))
	for _, programme := range programmes {
		ids = append(ids, programme.ID)
	}
	levels, err := s.programmeLevels.CountByProgrammes(ctx, ids, store.MaximumListCount)
	if err != nil {
		return programmeLevelStoreErrors.Translate(err)
	}
	counts.Levels = make(map[string]int, len(levels))
	for _, count := range levels {
		counts.Levels[count.ProgrammeID.String()] = count.Count
	}
	return nil
}

// ProgrammePage is one page of programmes with their counts.
type ProgrammePage struct {
	paging.Counted[*model.Programme]
	StructureCounts
}

// ProgrammeView is one programme with its counts.
type ProgrammeView struct {
	Programme *model.Programme
	StructureCounts
}

// ProgrammeLevelPage is one page of levels with their counts.
type ProgrammeLevelPage struct {
	paging.Counted[*model.ProgrammeLevel]
	StructureCounts
}

// ProgrammeLevelView is one level with its counts.
type ProgrammeLevelView struct {
	Level *model.ProgrammeLevel
	StructureCounts
}

// AcademicPeriodPage is one page of periods with their class counts.
type AcademicPeriodPage struct {
	paging.Page[*model.AcademicPeriod]
	StructureCounts
}

func (s *Service) programmeCounts(
	ctx context.Context,
	inv invocation.Invocation,
	unitID model.AcademicUnitID,
	programmes []*model.Programme,
) (StructureCounts, error) {
	counts := StructureCounts{}
	if err := s.countProgrammeLevels(ctx, inv, unitID, programmes, &counts); err != nil {
		return counts, err
	}
	ids := make([]string, 0, len(programmes))
	for _, programme := range programmes {
		ids = append(ids, programme.ID.String())
	}
	err := s.tallyClasses(ctx, inv, store.ClassTallyByProgramme, ids, true, &counts)
	return counts, err
}

func (s *Service) levelCounts(
	ctx context.Context,
	inv invocation.Invocation,
	levels []*model.ProgrammeLevel,
) (StructureCounts, error) {
	counts := StructureCounts{}
	ids := make([]string, 0, len(levels))
	for _, level := range levels {
		ids = append(ids, level.ID.String())
	}
	err := s.tallyClasses(ctx, inv, store.ClassTallyByProgrammeLevel, ids, true, &counts)
	return counts, err
}

func (s *Service) periodCounts(
	ctx context.Context,
	inv invocation.Invocation,
	periods []*model.AcademicPeriod,
) (StructureCounts, error) {
	counts := StructureCounts{}
	ids := make([]string, 0, len(periods))
	for _, period := range periods {
		ids = append(ids, period.ID.String())
	}
	err := s.tallyClasses(ctx, inv, store.ClassTallyByAcademicPeriod, ids, false, &counts)
	return counts, err
}
