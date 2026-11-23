// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type classPageStoreFake struct {
	store.ClassStore
	events  *[]string
	unitID  model.AcademicUnitID
	listed  []*model.Class
	options store.AcademicNameListOptions
	counted store.AcademicNameListOptions
}

func (s *classPageStoreFake) SearchByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions) ([]*model.Class, error) {
	*s.events = append(*s.events, "search")
	s.options = options
	return s.listed, nil
}

func (s *classPageStoreFake) CountByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions, maximum int) (int, error) {
	*s.events = append(*s.events, "count")
	s.counted = options
	return min(len(s.listed), maximum), nil
}

// Get and GetAcademicUnitID let the authorizer resolve a Class-scoped grant.
func (s *classPageStoreFake) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	for _, class := range s.listed {
		if class.ID == id {
			return class, nil
		}
	}
	return nil, store.NewErrNotFound("class", id.String())
}

func (s *classPageStoreFake) GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error) {
	return s.unitID, nil
}

// classPageUnitStoreFake answers the ancestors of the units it holds.
type classPageUnitStoreFake struct {
	store.AcademicUnitStore
	events *[]string
	units  []*model.AcademicUnit
}

func (s *classPageUnitStoreFake) ListAncestors(_ context.Context, id model.AcademicUnitID) ([]*model.AcademicUnit, error) {
	*s.events = append(*s.events, "ancestors")
	var chain []*model.AcademicUnit
	for next := id; !next.IsZero(); {
		index := slices.IndexFunc(s.units, func(unit *model.AcademicUnit) bool { return unit.ID == next })
		if index < 0 {
			break
		}
		chain = append(chain, s.units[index])
		next = s.units[index].ParentID
	}
	return chain, nil
}

type classStudentCountStoreFake struct {
	store.ClassMemberStore
	events   *[]string
	classIDs []model.ClassID
	at       time.Time
	maximum  int
	counts   []store.ClassStudentCount
}

func (s *classStudentCountStoreFake) CountCurrentByClasses(_ context.Context, classIDs []model.ClassID, at time.Time, maximum int) ([]store.ClassStudentCount, error) {
	*s.events = append(*s.events, "count-students")
	s.classIDs, s.at, s.maximum = classIDs, at, maximum
	return s.counts, nil
}

type classTeacherStoreFake struct {
	store.RoleBindingStore
	events  *[]string
	options store.ClassTeacherOptions
	read    []store.ClassTeachers
}

func (s *classTeacherStoreFake) ListTeachersByClasses(_ context.Context, options store.ClassTeacherOptions) ([]store.ClassTeachers, error) {
	*s.events = append(*s.events, "list-teachers")
	s.options = options
	return s.read, nil
}

// TestSearchClassesCountsStudentsOnlyForClassMemberViewers pins that a Class
// listing's direction and first-page total reach the Store, and that student
// counts and teachers are read only for the listed Classes inside the
// caller's class.members.view grant, without recording a second decision.
func TestSearchClassesCountsStudentsOnlyForClassMemberViewers(t *testing.T) {
	t.Parallel()
	world := newClassWorld(1)
	parentID, unitID := model.NewAcademicUnitID(), world.unitID
	first, second := world.class(), world.class()
	now := model.TimeFromMillis(9000)
	for name, test := range map[string]struct {
		grants func(model.UserID) []accesstest.Grant
		counts map[model.ClassID]int
		reads  []string
	}{
		"institution-wide": {
			grants: func(user model.UserID) []accesstest.Grant {
				return []accesstest.Grant{institutionGrant(user, model.ActionClassView, model.ActionClassMembersView)}
			},
			counts: map[model.ClassID]int{first.ID: 12, second.ID: 0},
			reads:  []string{"grants", "count-students", "list-teachers"},
		},
		"unit subtree": {
			grants: func(user model.UserID) []accesstest.Grant {
				return []accesstest.Grant{unitGrant(user, parentID, model.ActionClassView, model.ActionClassMembersView)}
			},
			counts: map[model.ClassID]int{first.ID: 12, second.ID: 0},
			reads:  []string{"grants", "ancestors", "count-students", "list-teachers"},
		},
		"one class": {
			grants: func(user model.UserID) []accesstest.Grant {
				return []accesstest.Grant{
					unitGrant(user, unitID, model.ActionClassView),
					{UserID: user, Actions: []model.Action{model.ActionClassMembersView}, ScopeType: model.RoleScopeClass, ScopeID: second.ID.String()},
				}
			},
			counts: map[model.ClassID]int{second.ID: 0},
			reads:  []string{"grants", "count-students", "list-teachers"},
		},
		"no grant": {
			grants: func(user model.UserID) []accesstest.Grant {
				return []accesstest.Grant{unitGrant(user, unitID, model.ActionClassView)}
			},
			reads: []string{"grants"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			principal, caller := newCaller()
			// accesstest places each unit in its Institution, so every
			// subtest holds its own.
			parent := &model.AcademicUnit{ID: parentID, Name: "faculty"}
			unit := &model.AcademicUnit{ID: unitID, ParentID: parentID, Name: "unit"}
			classes := &classPageStoreFake{events: &events, unitID: unit.ID, listed: []*model.Class{first, second}}
			access := accessWith(t, &events, accesstest.Options{
				Grants:          test.grants(principal.UserID),
				Units:           []*model.AcademicUnit{parent, unit},
				Classes:         classes,
				AcademicPeriods: academicPeriodsOf(world.period),
				Programmes:      programmesOf(world.programme),
				ProgrammeLevels: programmeLevelsOf(world.levels...),
				Now:             func() time.Time { return now },
			})
			units := &classPageUnitStoreFake{events: &events, units: []*model.AcademicUnit{parent, unit}}
			members := &classStudentCountStoreFake{events: &events, counts: []store.ClassStudentCount{{ClassID: first.ID, Count: 12}}}
			teacherID := model.NewUserID()
			// A teacher of a Class outside the grant is never presented.
			teachers := &classTeacherStoreFake{events: &events, read: []store.ClassTeachers{
				{ClassID: first.ID, UserIDs: []model.UserID{teacherID}, More: 1},
			}}
			service := mustNew(Dependencies{
				Store:      &catalogFake{academicUnit: units, class: classes, classMember: members, roleBinding: teachers},
				Authorizer: access.Authorizer,
				Auditor:    access.Auditor,
				Now:        func() time.Time { return now },
			})
			page, err := service.SearchClasses(context.Background(), caller, SearchClassesQuery{
				AcademicUnitID: unit.ID, Query: " algebra ", NameQuery: paging.NameQuery{Descending: true},
			})
			require.NoError(t, err)
			require.True(t, classes.options.Descending, "the direction reaches the Store")
			require.Equal(t, "algebra", classes.counted.Term, "the count reads the list's term")
			require.Equal(t, paging.Total{Counted: true, Total: 2}, page.Total)
			require.Equal(t, []string{"class.view academic_unit:" + unit.ID.String() + " success"}, decisions(access),
				"only the listing is a recorded decision")
			require.Equal(t, append([]string{"grants", "decision:class.view:success", "search", "count"}, test.reads...), events)
			require.Equal(t, test.counts, page.StudentCounts)
			if test.counts == nil {
				require.Nil(t, members.classIDs, "no student count is read")
				return
			}
			require.Len(t, members.classIDs, len(test.counts), "only visible Classes are counted")
			for _, id := range members.classIDs {
				require.Contains(t, test.counts, id)
			}
			require.True(t, now.Equal(members.at), "students are counted at the page's instant")
			require.Equal(t, store.MaximumListCount, members.maximum)
			require.ElementsMatch(t, members.classIDs, teachers.options.ClassIDs, "teachers are read for the counted Classes alone")
			require.True(t, now.Equal(teachers.options.ActiveAt), "teachers are read at the page's instant")
			require.Equal(t, ClassTeacherLimit, teachers.options.Limit)
			require.Len(t, page.Teachers, len(test.counts))
			for id := range test.counts {
				want := &store.ClassTeachers{ClassID: id, UserIDs: []model.UserID{}}
				if id == first.ID {
					want = &store.ClassTeachers{ClassID: id, UserIDs: []model.UserID{teacherID}, More: 1}
				}
				require.Equal(t, want, page.Teachers[id])
			}
		})
	}
}
