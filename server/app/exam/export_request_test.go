// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamExportsRequireDedicatedAndEveryOrdinaryReadPermission(t *testing.T) {
	for _, test := range []struct {
		name                                         string
		manager, member, integrity, browser, sitting bool
		deny                                         model.Action
	}{
		{name: "ordinary manager", manager: true, member: true}, {name: "scoped override"}, {name: "manager without membership", manager: true},
		{name: "integrity", manager: true, member: true, integrity: true}, {name: "Browser history", manager: true, member: true, browser: true}, {name: "Sitting", sitting: true},
		{name: "missing export", deny: model.ActionExamRecordsExportOverride},
		{name: "missing Submission read", deny: model.ActionSubmissionViewOverride},
		{name: "missing browser read", manager: true, member: true, browser: true, deny: model.ActionExamAttemptBrowserActivityView},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, service, s, auth, _ := newExportFixture(t)
			f.persistence.actorIsManager = test.manager
			if test.member {
				f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID, UserID: f.userID}}
			}
			auth.deny = test.deny
			scope := s.value.Scope
			if test.sitting {
				scope.SubmissionID = ""
			}
			categories := []model.RetentionCategory{model.RetentionCategoryWork}
			if test.integrity {
				categories = append(categories, model.RetentionCategoryIntegrity)
			}
			if test.browser {
				categories = []model.RetentionCategory{model.RetentionCategoryBrowserActivity}
			}
			_, err := service.CreateExamExport(context.Background(), f.inv, CreateExportCommand{Scope: scope, Categories: categories, IdempotencyKey: "export-key"})
			if test.deny != "" {
				require.Error(t, err)
				require.Nil(t, s.create)
				return
			}
			require.NoError(t, err)
			ordinary := test.manager && test.member
			want := model.ActionExamRecordsExportOverride
			read := model.ActionSubmissionViewOverride
			if ordinary {
				want = model.ActionExamRecordsExport
				read = model.ActionSubmissionView
			}
			require.Equal(t, want, s.create.Action, "authorization=%v creation=%#v", auth.actions, s.create)
			require.Contains(t, auth.actions, read, "authorization=%v creation=%#v", auth.actions, s.create)
			require.True(t, slices.Equal(s.create.SubmissionIDs, []model.SubmissionID{s.selected[0].SubmissionID}), "authorization=%v creation=%#v", auth.actions, s.create)
		})
	}
}

func TestExamExportsSelfAccessAndMissingAuditFailClosed(t *testing.T) {
	f, service, s, auth, _ := newExportFixture(t)
	s.selected[0].CandidateUserID = f.userID
	command := CreateExportCommand{Scope: s.value.Scope, Categories: s.value.Categories, IdempotencyKey: "export-key"}
	_, err := service.CreateExamExport(context.Background(), f.inv, command)
	require.Error(t, err, "candidate exported own manager-only records")
	require.True(t, auth.self, "candidate exported own manager-only records")
	require.Nil(t, s.create, "candidate exported own manager-only records")
	s.selected[0].CandidateUserID = model.NewUserID()
	f.auditor.err = errors.New("audit unavailable")
	_, err = service.CreateExamExport(context.Background(), f.inv, command)
	require.Error(t, err, "creation bypassed required audit")
	require.Nil(t, s.create, "creation bypassed required audit")
}

func TestExamExportReplayUsesCapturedScopeAfterSittingGrowsPastBound(t *testing.T) {
	f, service, s, auth, _ := newExportFixture(t)
	original := slices.Clone(s.selected)
	s.creationScope = original
	s.selected = make([]store.ExamSubmissionAuthorization, model.ExamExportMaximumSubmissions+1)
	s.value.Scope.SubmissionID = ""
	command := CreateExportCommand{Scope: s.value.Scope, Categories: s.value.Categories, IdempotencyKey: "retained-export"}
	_, err := service.CreateExamExport(context.Background(), f.inv, command)
	require.NoError(t, err)
	require.NotNil(t, s.creationCommand, "original captured scope was not preserved")
	require.Equal(t, store.ExamExportCreateOperation, s.creationCommand.Operation, "original captured scope was not preserved")
	require.NotNil(t, s.create, "original captured scope was not preserved")
	require.Len(t, s.create.SubmissionIDs, 1, "original captured scope was not preserved")
	require.Equal(t, original[0].SubmissionID, s.create.SubmissionIDs[0], "original captured scope was not preserved")
	auth.deny = model.ActionSubmissionViewOverride
	s.create = nil
	_, err = service.CreateExamExport(context.Background(), f.inv, command)
	require.Error(t, err, "replay-aware scope skipped current ordinary read authority")
	require.Nil(t, s.create, "replay-aware scope skipped current ordinary read authority")
}
