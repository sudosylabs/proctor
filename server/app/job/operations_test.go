// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// jobOperationsStoreFake is the Job Store behind the real Engine the
// operator service drives. It logs each read and transition into the test's
// shared log, beside the decisions and audit the real access fixture logs.
type jobOperationsStoreFake struct {
	*jobRunnerStoreFake
	events        *[]string
	job           *model.Job
	list          []*model.Job
	listOptions   store.JobListOptions
	transitionErr error
	transitions   int
	mutation      *store.JobMutation
}

func (f *jobOperationsStoreFake) Get(context.Context, model.JobID) (*model.Job, error) {
	*f.events = append(*f.events, "get")
	return f.job, nil
}

func (f *jobOperationsStoreFake) List(_ context.Context, options store.JobListOptions) ([]*model.Job, error) {
	*f.events = append(*f.events, "list")
	f.listOptions = options
	if f.list != nil {
		return slices.Clone(f.list), nil
	}
	return []*model.Job{f.job}, nil
}

func (f *jobOperationsStoreFake) Count(_ context.Context, options store.JobListOptions, maximum int) (int, error) {
	if f.list != nil {
		return min(len(f.list), maximum), nil
	}
	return 1, nil
}

func (f *jobOperationsStoreFake) Cancel(_ context.Context, mutation *store.JobMutation) (*model.Job, error) {
	*f.events = append(*f.events, "cancel")
	return f.transition(mutation)
}

func (f *jobOperationsStoreFake) Retry(_ context.Context, mutation *store.JobMutation) (*model.Job, error) {
	*f.events = append(*f.events, "retry")
	return f.transition(mutation)
}

func (f *jobOperationsStoreFake) transition(mutation *store.JobMutation) (*model.Job, error) {
	f.transitions++
	f.mutation = mutation
	if f.transitionErr != nil {
		return nil, f.transitionErr
	}
	return f.job, nil
}

type jobInstitutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
}

func (f jobInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return f.institution, nil
}

// jobOperationsFixture is a real Engine over the store fake, with the real
// authorizer and auditor deciding for an operator granted job.view and
// job.manage.
type jobOperationsFixture struct {
	access   *accesstest.Fixture
	operator invocation.Invocation
	engine   *Engine
	deps     OperationsDependencies
}

func newJobOperationsFixture(t *testing.T, events *[]string, persistence store.JobStore, descriptors ...Descriptor) jobOperationsFixture {
	t.Helper()
	operator := jobOperatorInvocation()
	fixture := accesstest.New(t, accesstest.Options{
		Grants: []accesstest.Grant{{
			UserID: operator.Principal().UserID, Actions: []model.Action{model.ActionJobView, model.ActionJobManage},
		}},
		Observe: func(event string) { *events = append(*events, event) },
	})
	if len(descriptors) == 0 {
		descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
		descriptor.Cancelable = true
		descriptor.ExplicitRetryStatuses = []model.JobStatus{model.JobStatusFailed}
		descriptors = []Descriptor{descriptor}
	}
	engine, err := New(Dependencies{
		Store: persistence, Descriptors: descriptors, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Policy: Policy{BackstopInterval: time.Hour},
	})
	require.NoError(t, err)
	return jobOperationsFixture{
		access:   fixture,
		operator: operator,
		engine:   engine,
		deps: OperationsDependencies{
			Jobs:          engine,
			Authorization: InstitutionAuthorization{Authorizer: fixture.Authorizer, Institutions: jobInstitutionStoreFake{institution: fixture.Institution}},
			Audit:         fixture.Auditor,
			Users:         &jobUserStoreFake{},
			ExamExports:   &jobExamExportStoreFake{},
			Now:           time.Now,
		},
	}
}

func jobOperatorInvocation() invocation.Invocation {
	return invocation.New(model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: model.NowUTC(), ClientType: model.SessionClientWeb,
	}, model.RequestMetadata{RequestID: "job-operations"})
}

// failedTestJob is a failed Job of the test descriptor's type, which the
// test descriptor may both cancel and retry.
func failedTestJob(t *testing.T) *model.Job {
	t.Helper()
	at := time.Now()
	queued, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, []byte(`{}`), "operations", at, at, 8)
	require.NoError(t, err)
	running, err := queued.Start(at)
	require.NoError(t, err)
	failed, err := running.Fail("job.failed", at)
	require.NoError(t, err)
	return failed
}

func TestJobOperationsAuthorizeBeforeEngineInspection(t *testing.T) {
	t.Parallel()
	events := make([]string, 0, 3)
	record := failedTestJob(t)
	persistence := &jobOperationsStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, events: &events, job: record}
	fixture := newJobOperationsFixture(t, &events, persistence)
	service, err := NewOperations(fixture.deps)
	require.NoError(t, err)

	page, appErr := service.ListJobs(context.Background(), fixture.operator, ListQuery{Limit: 20})
	require.NoError(t, appErr)
	require.Len(t, page.Items, 1, "page=%#v options=%#v events=%v", page, persistence.listOptions, events)
	require.Equal(t, record.ID, page.Items[0].ID, "page=%#v options=%#v events=%v", page, persistence.listOptions, events)
	require.Equal(t, 21, persistence.listOptions.Limit, "the engine reads one more than the page to know whether more follow")
	// Contract: authorization precedes the read.
	require.Equal(t, []string{"grants", "decision:" + string(model.ActionJobView) + ":success", "list"}, events)

	events = events[:0]
	_, appErr = service.GetJob(context.Background(), jobOperatorInvocation(), record.ID)
	require.True(t, apperror.Is(appErr, "authorization.denied"), "Get() error = %v, want the denial of a caller without job.view", appErr)
	require.Equal(t, []string{"grants", "decision:" + string(model.ActionJobView) + ":fail"}, events)
}

func TestJobOperationsAuditAfterPreparationAndBeforeTransition(t *testing.T) {
	t.Parallel()
	events := make([]string, 0, 6)
	record := failedTestJob(t)
	persistence := &jobOperationsStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, events: &events, job: record}
	fixture := newJobOperationsFixture(t, &events, persistence)
	service, err := NewOperations(fixture.deps)
	require.NoError(t, err)
	manage := string(model.ActionJobManage)

	_, appErr := service.CancelJob(context.Background(), fixture.operator, record.ID)
	require.NoError(t, appErr)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"grants", "decision:" + manage + ":success", "get", "audit-begin:" + manage, "cancel"}, events)
	attempts := fixture.access.Audits.Attempts()
	require.Len(t, attempts, 1)
	require.Equal(t, model.Resource{Type: model.ResourceInstitution, ID: fixture.access.Institution.ID.String()}, attempts[0].Resource)
	require.JSONEq(t, `{"operation":"cancel","value":{"job_id":"`+record.ID.String()+`","type":"`+string(record.Type)+`","status":"`+string(record.Status)+`"}}`,
		string(attempts[0].Parameters))
	require.Equal(t, 1, persistence.transitions, "events")
	require.Equal(t, attempts[0].ID, persistence.mutation.AuditEventID, "the transition completes the attempt it was given")

	events = events[:0]
	fixture.access.Audits.FailAttempts(errors.New("audit unavailable"))
	_, appErr = service.RetryJob(context.Background(), fixture.operator, record.ID)
	require.True(t, apperror.Is(appErr, "audit.unavailable"), "Retry() error = %v, want the audit failure", appErr)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write.
	require.Equal(t, []string{"grants", "decision:" + manage + ":success", "get", "audit-begin:" + manage}, events)
	require.Equal(t, 1, persistence.transitions, "failed-audit events")

	events = events[:0]
	fixture.access.Audits.FailAttempts(nil)
	persistence.transitionErr = errors.New("transition unavailable")
	_, appErr = service.RetryJob(context.Background(), fixture.operator, record.ID)
	require.Error(t, appErr, "Retry() ignored transition failure")
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable write, and the audit outcome follows the write.
	require.Equal(t, []string{"grants", "decision:" + manage + ":success", "get", "audit-begin:" + manage, "retry", "audit-fail"}, events)
	attempts = fixture.access.Audits.Attempts()
	require.Len(t, attempts, 2, "failed-transition attempts")
	require.Equal(t, model.AuditStatusFail, attempts[1].Status, "failed-transition attempt")
	require.Equal(t, "job.conflict", attempts[1].ErrorCode, "failed-transition attempt")
}

func TestJobOperationsTranslateEnginePolicyErrors(t *testing.T) {
	t.Parallel()
	events := make([]string, 0, 3)
	persistence := &jobOperationsStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, events: &events, job: failedTestJob(t)}
	// The descriptor is not cancelable, so the engine refuses the preparation.
	fixture := newJobOperationsFixture(t, &events, persistence,
		testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() })))
	service, err := NewOperations(fixture.deps)
	require.NoError(t, err)
	_, appErr := service.CancelJob(context.Background(), fixture.operator, persistence.job.ID)
	require.True(t, apperror.Is(appErr, "job.cancel.unsupported"), "Cancel() error=%#v", appErr)
}

func TestJobOperationsRequireDependencies(t *testing.T) {
	t.Parallel()

	events := []string{}
	fixture := newJobOperationsFixture(t, &events, &jobOperationsStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, events: &events})
	engine, authorizer, auditor := fixture.engine, fixture.deps.Authorization, fixture.deps.Audit
	users, exports := &jobUserStoreFake{}, &jobExamExportStoreFake{}
	tests := []struct {
		name          string
		jobs          *Engine
		authorization InstitutionAuthorization
		audit         *access.Auditor
		users         store.UserStore
		exports       store.ExamExportStore
		now           func() time.Time
	}{
		{name: "engine", authorization: authorizer, audit: auditor, users: users, exports: exports, now: time.Now},
		{name: "authorization", jobs: engine, audit: auditor, users: users, exports: exports, now: time.Now},
		{name: "audit", jobs: engine, authorization: authorizer, users: users, exports: exports, now: time.Now},
		{name: "initiator names", jobs: engine, authorization: authorizer, audit: auditor, exports: exports, now: time.Now},
		{name: "exam export titles", jobs: engine, authorization: authorizer, audit: auditor, users: users, now: time.Now},
		{name: "clock", jobs: engine, authorization: authorizer, audit: auditor, users: users, exports: exports},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewOperations(OperationsDependencies{Jobs: test.jobs, Authorization: test.authorization, Audit: test.audit, Users: test.users, ExamExports: test.exports, Now: test.now})
			require.Error(t, err, "nil %s dependency was accepted", test.name)
		})
	}
}

type jobUserStoreFake struct {
	store.UserStore
	names []store.UserDisplayName
	calls [][]model.UserID
	err   error
}

func (f *jobUserStoreFake) ListDisplayNames(_ context.Context, ids []model.UserID) ([]store.UserDisplayName, error) {
	f.calls = append(f.calls, slices.Clone(ids))
	return f.names, f.err
}

type jobExamExportStoreFake struct {
	store.ExamExportStore
	titles []store.ExamExportBuildTitle
	calls  [][]model.JobID
}

func (f *jobExamExportStoreFake) ListBuildTitles(_ context.Context, ids []model.JobID) ([]store.ExamExportBuildTitle, error) {
	f.calls = append(f.calls, slices.Clone(ids))
	return f.titles, nil
}

func TestJobOperationsPresentInitiatorsAndSubjectsOncePerPage(t *testing.T) {
	t.Parallel()
	events := make([]string, 0, 2)
	operator := model.NewUserID()
	firstExport, secondExport, rekey, system := model.NewJobID(), model.NewJobID(), model.NewJobID(), model.NewJobID()
	persistence := &jobOperationsStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, events: &events, list: []*model.Job{
		{ID: firstExport, Type: model.JobTypeExamExportBuild, CreatedByUserID: operator},
		{ID: secondExport, Type: model.JobTypeExamExportBuild, CreatedByUserID: operator},
		{ID: rekey, Type: model.JobTypeMailRekey, CreatedByUserID: operator},
		{ID: system, Type: model.JobTypeCleanup},
	}}
	var descriptors []Descriptor
	for _, jobType := range []model.JobType{model.JobTypeExamExportBuild, model.JobTypeMailRekey, model.JobTypeCleanup} {
		descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
		descriptor.Type = jobType
		descriptors = append(descriptors, descriptor)
	}
	fixture := newJobOperationsFixture(t, &events, persistence, descriptors...)
	users := &jobUserStoreFake{names: []store.UserDisplayName{{UserID: operator, DisplayName: "Ada Operator"}}}
	exports := &jobExamExportStoreFake{titles: []store.ExamExportBuildTitle{
		{JobID: firstExport, Title: "Algorithms final"}, {JobID: secondExport, Title: strings.Repeat("é", 250)},
	}}
	fixture.deps.Users, fixture.deps.ExamExports = users, exports
	service, err := NewOperations(fixture.deps)
	require.NoError(t, err)

	page, err := service.ListJobs(context.Background(), fixture.operator, ListQuery{Limit: 20})
	require.NoError(t, err)
	// Each fact is read once for the page, and only the export builds ask for a subject.
	require.Equal(t, [][]model.UserID{{operator, operator, operator}}, users.calls)
	require.Equal(t, [][]model.JobID{{firstExport, secondExport}}, exports.calls)
	require.Equal(t, []string{"Ada Operator", "Ada Operator", "Ada Operator", ""}, []string{page.Items[0].CreatedByDisplayName, page.Items[1].CreatedByDisplayName, page.Items[2].CreatedByDisplayName, page.Items[3].CreatedByDisplayName})
	require.Equal(t, "Algorithms final", page.Items[0].Subject)
	require.Equal(t, strings.Repeat("é", maximumSubjectLength), page.Items[1].Subject, "a subject is bounded in characters")
	require.Empty(t, page.Items[2].Subject, "a rekey has no subject")
	require.True(t, page.Items[3].CreatedByUserID.IsZero(), "system work has no initiator")
	require.Empty(t, page.Items[3].Subject)

	persistence.job = persistence.list[3]
	single, err := service.GetJob(context.Background(), fixture.operator, system)
	require.NoError(t, err)
	require.Empty(t, single.CreatedByDisplayName)
	require.Len(t, users.calls, 1, "a Job with no initiator reads no names")

	users.err = errors.New("users unavailable")
	_, err = service.ListJobs(context.Background(), fixture.operator, ListQuery{Limit: 20})
	require.True(t, apperror.Is(err, "job.unavailable"), "ListJobs() error=%#v", err)
}
