// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCheckExamDraftReportsEveryProblemWithPublishingsCodes(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	resourceID := model.NewExamResourceID()
	revisions := &revisionStoreFake{check: &store.ExamPublicationCheckResult{DraftRevision: 7, Problems: []store.ExamPublicationProblem{
		{Reason: store.ExamPublicationExecutionDisabled},
		{Reason: store.ExamPublicationResourceTooLarge, ResourceID: resourceID},
	}}}
	service := draftCheckService(t, f, revisions)

	check, err := service.CheckExamDraft(context.Background(), f.inv, f.examID)
	require.NoError(t, err)
	require.False(t, check.Publishable())
	require.Equal(t, int64(7), check.DraftRevision)
	require.Equal(t, []DraftProblem{
		{Reason: store.ExamPublicationExecutionDisabled, Code: "exam.execution.disabled", Field: "policy.execution"},
		{Reason: store.ExamPublicationResourceTooLarge, Code: "exam.revision.capacity_exceeded", Field: "resources.size_bytes", ResourceID: resourceID},
	}, check.Problems)
	require.Equal(t, []string{decided(model.ActionExamPublish, f.examResource(), model.AuditStatusSuccess)}, f.decisions(),
		"the check is authorized like publishing")
	require.False(t, revisions.checkInput.ManagerOverride)
	require.Equal(t, "https://institution.example", revisions.checkInput.InstitutionOrigin)
}

func TestCheckExamDraftEveryReasonHasAPresentation(t *testing.T) {
	t.Parallel()
	for _, reason := range store.ExamPublicationProblemReasons() {
		require.NotEmpty(t, draftProblemFields[reason], "reason %q has no field", reason)
		problem := store.ExamPublicationProblem{Reason: reason}
		code := apperror.CodeOf(examStoreErrors.Translate(problem.PublicationError()), "exam.unavailable")
		require.NotEqual(t, "exam.unavailable", code, "reason %q has no publication code", reason)
	}
}

func TestCheckExamDraftNeedsPublishPermission(t *testing.T) {
	t.Parallel()
	f := newAuthoringFixture(t, model.ActionExamView)
	f.persistence.actorIsManager = true
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID}}
	revisions := &revisionStoreFake{check: &store.ExamPublicationCheckResult{DraftRevision: 1, Problems: []store.ExamPublicationProblem{}}}
	service := draftCheckService(t, f, revisions)

	_, err := service.CheckExamDraft(context.Background(), f.inv, f.examID)
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "resource.not_found", fault.Code(), "a refused check conceals the exam")
	require.Nil(t, revisions.checkInput, "the store is not asked before authorization")
}

func draftCheckService(t *testing.T, f authoringFixture, revisions *revisionStoreFake) *Publication {
	t.Helper()
	service, err := NewPublication(PublicationDependencies{
		Persistence:       revisions,
		Access:            f.persistence,
		Memberships:       f.memberships,
		Content:           &revisionContentFake{},
		Authorizer:        f.access.Authorizer,
		Auditor:           f.access.Auditor,
		Publisher:         &realtimetest.Publisher{},
		Now:               fixedPublicationTime,
		NewID:             model.NewExamRevisionID,
		InstitutionOrigin: "https://institution.example",
	})
	require.NoError(t, err)
	return service
}

func (f *revisionStoreFake) CheckPublication(_ context.Context, input *store.ExamPublicationCheck) (*store.ExamPublicationCheckResult, error) {
	f.checkInput = input
	return f.check, f.err
}
