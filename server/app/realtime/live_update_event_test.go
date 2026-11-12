// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestJobChangedEventIsAnInstitutionJobViewSignal(t *testing.T) {
	t.Parallel()

	institutionID, jobID := model.NewInstitutionID(), model.NewJobID()
	event, err := NewJobChangedEvent(institutionID, jobID, model.JobTypeOnboardingImportExecute, model.JobStatusRunning,
		&model.JobProgress{Current: 4, Total: 10, Stage: "executing"})
	require.NoError(t, err)
	require.Equal(t, "job.changed", event.Name)
	require.Equal(t, model.ActionJobView, event.Action)
	require.Equal(t, model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()}, event.Resource)
	require.Empty(t, event.UserID)
	require.JSONEq(t, `{"schema_version":1,"job_id":"`+jobID.String()+`","type":"onboarding_import.execute",`+
		`"status":"running","progress":{"current":4,"total":10,"stage":"executing"}}`, string(event.Data))

	withoutProgress, err := NewJobChangedEvent(institutionID, jobID, model.JobTypeCleanup, model.JobStatusSucceeded, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"schema_version":1,"job_id":"`+jobID.String()+`","type":"job.cleanup","status":"succeeded"}`,
		string(withoutProgress.Data))
}

func TestJobChangedEventRefusesInvalidInput(t *testing.T) {
	t.Parallel()

	institutionID, jobID := model.NewInstitutionID(), model.NewJobID()
	tests := map[string]struct {
		institutionID model.InstitutionID
		jobID         model.JobID
		jobType       model.JobType
		status        model.JobStatus
		progress      *model.JobProgress
	}{
		"institution":       {jobID: jobID, jobType: model.JobTypeCleanup, status: model.JobStatusQueued},
		"job":               {institutionID: institutionID, jobType: model.JobTypeCleanup, status: model.JobStatusQueued},
		"type":              {institutionID: institutionID, jobID: jobID, jobType: "unknown.type", status: model.JobStatusQueued},
		"status":            {institutionID: institutionID, jobID: jobID, jobType: model.JobTypeCleanup, status: "paused"},
		"negative progress": {institutionID, jobID, model.JobTypeCleanup, model.JobStatusRunning, &model.JobProgress{Current: -1, Total: 2, Stage: "s"}},
		"empty total":       {institutionID, jobID, model.JobTypeCleanup, model.JobStatusRunning, &model.JobProgress{Total: 0, Stage: "s"}},
		"overrun":           {institutionID, jobID, model.JobTypeCleanup, model.JobStatusRunning, &model.JobProgress{Current: 3, Total: 2, Stage: "s"}},
		"stageless":         {institutionID, jobID, model.JobTypeCleanup, model.JobStatusRunning, &model.JobProgress{Current: 1, Total: 2}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewJobChangedEvent(test.institutionID, test.jobID, test.jobType, test.status, test.progress)
			require.Error(t, err)
		})
	}
}

func TestOnboardingImportChangedEventAddressesTheGivenScope(t *testing.T) {
	t.Parallel()

	importID := model.NewOnboardingImportID()
	scope := model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewAcademicUnitID().String()}
	event, err := NewOnboardingImportChangedEvent(importID, model.OnboardingImportExecuting, scope)
	require.NoError(t, err)
	require.Equal(t, "onboarding_import.changed", event.Name)
	require.Equal(t, model.ActionOnboardingBatchView, event.Action)
	require.Equal(t, scope, event.Resource)
	require.Empty(t, event.UserID)
	require.JSONEq(t, `{"schema_version":1,"import_id":"`+importID.String()+`","state":"executing"}`, string(event.Data))
	require.NoError(t, event.ValidateForPublish())
}

func TestOnboardingImportChangedEventRefusesInvalidInput(t *testing.T) {
	t.Parallel()

	importID := model.NewOnboardingImportID()
	unit := model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewAcademicUnitID().String()}
	tests := map[string]struct {
		importID model.OnboardingImportID
		state    model.OnboardingImportState
		scope    model.Resource
	}{
		"import":      {state: model.OnboardingImportParsing, scope: unit},
		"state":       {importID: importID, state: "paused", scope: unit},
		"scope id":    {importID: importID, state: model.OnboardingImportParsing, scope: model.Resource{Type: model.ResourceClass}},
		"scope type":  {importID: importID, state: model.OnboardingImportParsing, scope: model.Resource{Type: model.ResourceExam, ID: model.NewExamID().String()}},
		"empty scope": {importID: importID, state: model.OnboardingImportParsing},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewOnboardingImportChangedEvent(test.importID, test.state, test.scope)
			require.Error(t, err)
		})
	}
}

func TestInvitationChangedEventIsAddressedToTheInviter(t *testing.T) {
	t.Parallel()

	inviterID, invitationID := model.NewUserID(), model.NewInvitationID()
	event, err := NewInvitationChangedEvent(inviterID, invitationID, model.InvitationAccepted)
	require.NoError(t, err)
	require.Equal(t, "invitation.changed", event.Name)
	require.Equal(t, inviterID, event.UserID)
	require.Empty(t, event.Action)
	require.Equal(t, model.Resource{}, event.Resource)
	require.JSONEq(t, `{"schema_version":1,"invitation_id":"`+invitationID.String()+`","state":"accepted"}`, string(event.Data))

	_, err = NewInvitationChangedEvent("", invitationID, model.InvitationAccepted)
	require.Error(t, err, "missing inviter")
	_, err = NewInvitationChangedEvent(inviterID, "", model.InvitationAccepted)
	require.Error(t, err, "missing Invitation")
	_, err = NewInvitationChangedEvent(inviterID, invitationID, "used")
	require.Error(t, err, "unknown state")
}

func TestCurrentUserSessionsChangedEventIsAddressedToTheUser(t *testing.T) {
	t.Parallel()

	userID := model.NewUserID()
	event, err := NewCurrentUserSessionsChangedEvent(userID)
	require.NoError(t, err)
	require.Equal(t, Event{Name: "current_user.sessions.changed", UserID: userID, Data: []byte(`{"schema_version":1}`)}, event)

	_, err = NewCurrentUserSessionsChangedEvent("")
	require.Error(t, err)
}

func TestEventMayAddressAnOwningScopeItsActionAccepts(t *testing.T) {
	t.Parallel()

	accepted := Event{Name: "onboarding_import.changed", Action: model.ActionOnboardingBatchView,
		Resource: model.Resource{Type: model.ResourceClass, ID: model.NewClassID().String()}}
	require.NoError(t, accepted.ValidateForPublish())

	refused := Event{Name: "job.changed", Action: model.ActionJobView,
		Resource: model.Resource{Type: model.ResourceClass, ID: model.NewClassID().String()}}
	require.Error(t, refused.ValidateForPublish())
}
