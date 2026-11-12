// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type classPageStoreFake struct {
	store.ClassStore
	events  *[]string
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

// TestSearchClassesCountsStudentsOnlyForClassMemberViewers pins that a Class
// listing's direction and first-page total reach the Store, and that student
// counts are read only for a caller holding class.members.view on the unit.
func TestSearchClassesCountsStudentsOnlyForClassMemberViewers(t *testing.T) {
	t.Parallel()
	unitID := model.NewAcademicUnitID()
	first, second := &model.Class{ID: model.NewClassID()}, &model.Class{ID: model.NewClassID()}
	now := model.TimeFromMillis(9000)
	for _, viewer := range []bool{false, true} {
		events := []string{}
		classes := &classPageStoreFake{events: &events, listed: []*model.Class{first, second}}
		members := &classStudentCountStoreFake{events: &events, counts: []store.ClassStudentCount{{ClassID: first.ID, Count: 12}}}
		service := mustNew(Dependencies{
			Store:      &catalogFake{class: classes, classMember: members},
			Authorizer: &programmeAuthorizerFake{events: &events, classMembers: viewer},
			Now:        func() time.Time { return now },
		})
		page, err := service.SearchClasses(context.Background(), invocation.Invocation{}, SearchClassesQuery{
			AcademicUnitID: unitID, Query: " algebra ", NameQuery: paging.NameQuery{Descending: true},
		})
		require.NoError(t, err)
		require.True(t, classes.options.Descending, "the direction reaches the Store")
		require.Equal(t, "algebra", classes.counted.Term, "the count reads the list's term")
		require.Equal(t, paging.Total{Counted: true, Total: 2}, page.Total)
		gate := "can-view-class-members:academic_unit:" + unitID.String()
		if !viewer {
			require.Equal(t, []string{"authorize", "search", "count", gate}, events, "no student count is read without class.members.view")
			require.Nil(t, page.StudentCounts)
			continue
		}
		require.Equal(t, []string{"authorize", "search", "count", gate, "count-students"}, events)
		require.Equal(t, []model.ClassID{first.ID, second.ID}, members.classIDs)
		require.True(t, now.Equal(members.at), "students are counted at the service's now")
		require.Equal(t, store.MaximumListCount, members.maximum)
		require.Equal(t, map[model.ClassID]int{first.ID: 12}, page.StudentCounts)
	}
}
