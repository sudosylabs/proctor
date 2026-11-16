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

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
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
// listing's direction and first-page total reach the Store, and what the
// student-count gate does with the unit the listing is scoped to.
//
// The gate asks for class.members.view on the Academic Unit, but that action
// accepts only a Class resource, so the real authorizer refuses the request
// as invalid and a listing with any Class fails, whatever the caller holds.
// The former fake authorizer answered the gate and hid this. Fixing it
// changes the listing's behavior, so it is left for its own change.
func TestSearchClassesCountsStudentsOnlyForClassMemberViewers(t *testing.T) {
	t.Parallel()
	unitID := model.NewAcademicUnitID()
	first, second := &model.Class{ID: model.NewClassID()}, &model.Class{ID: model.NewClassID()}
	now := model.TimeFromMillis(9000)
	for _, viewer := range []bool{false, true} {
		events := []string{}
		principal, caller := newCaller()
		actions := []model.Action{model.ActionClassView}
		if viewer {
			actions = append(actions, model.ActionClassMembersView)
		}
		access := accessWith(t, &events, accesstest.Options{Grants: []accesstest.Grant{unitGrant(principal.UserID, unitID, actions...)}})
		classes := &classPageStoreFake{events: &events, listed: []*model.Class{first, second}}
		members := &classStudentCountStoreFake{events: &events, counts: []store.ClassStudentCount{{ClassID: first.ID, Count: 12}}}
		service := mustNew(Dependencies{
			Store:      &catalogFake{class: classes, classMember: members},
			Authorizer: access.Authorizer,
			Auditor:    access.Auditor,
			Now:        func() time.Time { return now },
		})
		_, err := service.SearchClasses(context.Background(), caller, SearchClassesQuery{
			AcademicUnitID: unitID, Query: " algebra ", NameQuery: paging.NameQuery{Descending: true},
		})
		require.True(t, classes.options.Descending, "the direction reaches the Store")
		require.Equal(t, "algebra", classes.counted.Term, "the count reads the list's term")
		require.Equal(t, []string{"class.view academic_unit:" + unitID.String() + " success"}, decisions(access), "only the listing is a recorded decision")
		require.True(t, apperror.Is(err, "authorization.request.invalid"), "viewer=%t: SearchClasses() error = %v", viewer, err)
		require.Equal(t, []string{"grants", "decision:class.view:success", "search", "count"}, events, "no student count is read")
		require.Nil(t, members.classIDs, "no student count is read")
	}
}
