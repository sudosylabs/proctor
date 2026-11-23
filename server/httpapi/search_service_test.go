// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/app/search"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// searchFixture is the real search.Service over the real Exam catalog and
// User directory, all deciding with one real authorizer. A handler test
// asserts what each requested type asked of its Store.
type searchFixture struct {
	access  *accesstest.Fixture
	exams   *searchExamRecords
	classes *searchClassRecords
	users   *directoryUserStore
	service *search.Service
}

// newSearchFixture builds the fixture with the caller's grants.
func newSearchFixture(t *testing.T, grants ...accesstest.Grant) *searchFixture {
	t.Helper()
	fixture := &searchFixture{
		exams:   &searchExamRecords{},
		classes: &searchClassRecords{},
		users:   &directoryUserStore{byID: map[model.UserID]*model.User{}},
	}
	fixture.access = accesstest.New(t, accesstest.Options{Grants: grants, Users: fixture.users})
	publisher := &realtimetest.Publisher{}

	authoring, err := exam.NewAuthoring(exam.AuthoringDependencies{
		Persistence: fixture.exams, Memberships: searchUnusedMemberships{}, Users: fixture.users,
		Sittings: searchUnusedSittings{}, Mail: searchUnusedManagerMail{},
		Authorizer: fixture.access.Authorizer, Auditor: fixture.access.Auditor, Publisher: publisher,
		Now: time.Now, NewID: model.NewExamID,
	})
	require.NoError(t, err)

	directory := &userFixture{
		users: fixture.users, roles: &directoryRoleStore{}, bindings: &directoryBindingStore{},
		files: &pictureFileStore{}, pictures: &pictureContent{}, settings: &settingsDocumentStore{},
	}
	users, err := user.New(user.Dependencies{
		Store:      userCatalog{fixture: directory},
		Authorizer: fixture.access.Authorizer,
		Auditor:    fixture.access.Auditor,
		Publisher:  publisher,
		Pictures:   directory.pictures,
		Now:        time.Now,
	})
	require.NoError(t, err)

	fixture.service = search.New(search.Dependencies{
		Exams: authoring, Users: users, ClassAuthorization: fixture.access.Authorizer, Classes: fixture.classes,
	})
	return fixture
}

// storeReads is how many reads the three searched Stores answered.
func (f *searchFixture) storeReads() int {
	return len(f.exams.lists) + len(f.classes.searches) + len(f.users.listOptions)
}

// searchExamRecords is the Exam catalog: it answers the Exams a test lists
// and records each listing.
type searchExamRecords struct {
	store.ExamAuthoringStore
	listed []store.ExamSummary
	lists  []store.ExamListOptions
}

func (s *searchExamRecords) Count(_ context.Context, _ store.ExamListOptions, maximum int) (int, error) {
	return min(len(s.listed), maximum), nil
}

func (s *searchExamRecords) List(_ context.Context, options store.ExamListOptions) ([]store.ExamSummary, error) {
	s.lists = append(s.lists, options)
	return slices.Clone(s.listed), nil
}

// searchClassRecords answers the Classes a test lists to every visible
// Class search and records each one.
type searchClassRecords struct {
	store.ClassStore
	listed   []*model.Class
	searches []store.ClassSearchOptions
}

func (s *searchClassRecords) SearchVisible(_ context.Context, options store.ClassSearchOptions) ([]*model.Class, error) {
	s.searches = append(s.searches, options)
	return slices.Clone(s.listed), nil
}

// The Exam catalog reads none of these when it lists.
type (
	searchUnusedMemberships struct{ store.AcademicUnitMemberStore }
	searchUnusedSittings    struct{ store.ExamSittingStore }
	searchUnusedManagerMail struct{}
)

func (searchUnusedManagerMail) PrepareManagerMail(exam.ManagerMailPreparation) (*store.ExamManagerMail, error) {
	return nil, errors.New("a search prepares no Exam Manager mail")
}
