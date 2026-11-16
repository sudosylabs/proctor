// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamSittingLifecycleJobFactoryFencesEachBoundaryByRevision(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	startAt, endAt := at.Add(time.Hour), at.Add(2*time.Hour)
	sittingID := model.NewExamSittingID()
	factory := NewExamSittingLifecycleJobFactory(func() time.Time { return at }, model.NewJobID)

	openJob, deadlineJob, err := factory.BoundaryJobs(sittingID, 4, startAt, endAt)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, openJob, model.JobTypeExamSittingLifecycle, sittingID,
		model.ExamSittingLifecycleJobOpen, 4, at, startAt)
	assertExamSittingLifecycleJob(t, deadlineJob, model.JobTypeExamSittingLifecycle, sittingID,
		model.ExamSittingLifecycleJobDeadline, 4, at, endAt)

	nextDeadline := endAt.Add(time.Hour)
	extended, err := factory.DeadlineJob(sittingID, 5, nextDeadline)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, extended, model.JobTypeExamSittingLifecycle, sittingID,
		model.ExamSittingLifecycleJobDeadline, 5, at, nextDeadline)

	finalize, err := factory.FinalizeJob(sittingID, 6, nextDeadline)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, finalize, model.JobTypeExamSittingSealing, sittingID,
		model.ExamSittingLifecycleJobFinalize, 6, at, nextDeadline)

	// With execution environments the sealing Job waits for them to settle;
	// the boundary Jobs do not move.
	settling := factory.WithSettleBudget(5 * time.Second)
	delayed, err := settling.FinalizeJob(sittingID, 6, nextDeadline)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, delayed, model.JobTypeExamSittingSealing, sittingID,
		model.ExamSittingLifecycleJobFinalize, 6, at, nextDeadline.Add(5*time.Second))
	boundary, err := settling.DeadlineJob(sittingID, 7, nextDeadline)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, boundary, model.JobTypeExamSittingLifecycle, sittingID,
		model.ExamSittingLifecycleJobDeadline, 7, at, nextDeadline)
}

func TestExamSittingLifecycleHandlerReconcilesAndTreatsStaleWorkAsSuccess(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sittingID := model.NewExamSittingID()
	record := mustExamSittingLifecycleJob(t, sittingID, at)
	attemptID := model.NewJobAttemptID()
	persistence := newSittingLifecycleStore(lifecycleJobResult(t, sittingID, false))
	sittings, control := newLifecycleSittings(t, persistence)
	handler := examSittingLifecycleHandler{sittings: sittings}

	outcome := handler.Run(context.Background(), job.NewExecution(record, &model.JobAttempt{ID: attemptID}, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.NoError(t, outcome.Err)
	require.Equal(t, []model.ExamSittingID{sittingID}, persistence.advancedIDs(), "reconcile identity")
	requireLifecycleJobAudit(t, control, invocation.Job(record.ID, attemptID), persistence.advanced[0].AuditEventID)
	var result ExamSittingLifecycleJobResult
	require.NoError(t, json.Unmarshal(outcome.Result, &result), "result = %#v", result)
	require.False(t, result.Changed, "result = %#v", result)
	require.Equal(t, model.ExamSittingScheduled, result.State, "result = %#v", result)
	require.Equal(t, int64(1), result.Revision, "result = %#v", result)
}

func TestExamSittingLifecycleHandlerClassifiesFailures(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sittingID := model.NewExamSittingID()
	valid := mustExamSittingLifecycleJob(t, sittingID, at)
	attempt := &model.JobAttempt{ID: model.NewJobAttemptID()}

	tests := []struct {
		name string
		job  *model.Job
		err  error
		kind job.OutcomeKind
		code string
	}{
		{name: "invalid command", job: func() *model.Job {
			copy := *valid
			copy.Command = json.RawMessage(`{"exam_sitting_id":null}`)
			return &copy
		}(), kind: job.OutcomePermanentFailure, code: "job.command.invalid"},
		{name: "invalid durable request", job: valid, err: store.NewErrInvalidInput("exam_sitting", "advance_due", nil), kind: job.OutcomePermanentFailure, code: "job.invariant_failed"},
		{name: "database constraint rejection", job: valid, err: store.NewErrConstraintViolation("exam_sitting", "exam_sittings_lifecycle_check"), kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
		{name: "missing durable target", job: valid, err: store.NewErrNotFound("exam_sitting", sittingID.String()), kind: job.OutcomePermanentFailure, code: "job.invariant_failed"},
		{name: "dependency failure", job: valid, err: errors.New("database unavailable"), kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persistence := newSittingLifecycleStore(lifecycleJobResult(t, sittingID, false))
			persistence.err = tt.err
			sittings, _ := newLifecycleSittings(t, persistence)
			outcome := (examSittingLifecycleHandler{sittings: sittings}).Run(context.Background(), job.NewExecution(tt.job, attempt, nil, nil))
			require.Equal(t, tt.kind, outcome.Kind)
			require.Equal(t, tt.code, outcome.PublicErrorCode)
		})
	}
}

func TestExamSittingLifecycleDescriptorIsBoundedAndNonCancelable(t *testing.T) {
	t.Parallel()
	sittings, _ := newLifecycleSittings(t, newSittingLifecycleStore())
	descriptor := examSittingLifecycleDescriptor(examSittingLifecycleHandler{sittings: sittings})
	require.Equal(t, model.JobTypeExamSittingLifecycle, descriptor.Type)
	require.False(t, descriptor.Cancelable)
	require.Equal(t, job.VisibilityOperator, descriptor.Visibility)
	require.GreaterOrEqual(t, descriptor.Concurrency, 1)
}

// sittingLifecycleStore is the Sitting Store behind the real Sitting service
// the lifecycle, recovery and sealing Jobs drive. It resolves the Sittings
// whose results it holds, answers each due advance or sealing finish with
// that result, or with err, and keeps what it was asked.
type sittingLifecycleStore struct {
	store.ExamSittingStore
	mu          sync.Mutex
	unitID      model.AcademicUnitID
	results     map[model.ExamSittingID]*store.ExamSittingLifecycleResult
	pages       [][]store.ExamSittingLifecycleDue
	listOptions []store.ExamSittingLifecycleDueOptions
	advanced    []*store.ExamSittingDueAdvance
	finished    []*store.ExamSittingFinishSealing
	err         error
}

func newSittingLifecycleStore(results ...*store.ExamSittingLifecycleResult) *sittingLifecycleStore {
	persistence := &sittingLifecycleStore{unitID: model.NewAcademicUnitID(), results: map[model.ExamSittingID]*store.ExamSittingLifecycleResult{}}
	for _, result := range results {
		persistence.results[result.Value.Sitting.ID] = result
	}
	return persistence
}

func (f *sittingLifecycleStore) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.results[id]
	if !ok {
		return nil, store.NewErrNotFound("exam_sitting", id.String())
	}
	return &store.ExamSittingSnapshot{Sitting: result.Value.Sitting, AcademicUnitID: f.unitID}, nil
}

func (f *sittingLifecycleStore) AdvanceDue(_ context.Context, input *store.ExamSittingDueAdvance) (*store.ExamSittingLifecycleResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanced = append(f.advanced, input)
	return f.answer(input.SittingID)
}

func (f *sittingLifecycleStore) FinishSealing(_ context.Context, input *store.ExamSittingFinishSealing) (*store.ExamSittingLifecycleResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, input)
	return f.answer(input.SittingID)
}

func (f *sittingLifecycleStore) answer(id model.ExamSittingID) (*store.ExamSittingLifecycleResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	result := *f.results[id]
	return &result, nil
}

func (f *sittingLifecycleStore) ListLifecycleDue(_ context.Context, options store.ExamSittingLifecycleDueOptions) ([]store.ExamSittingLifecycleDue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listOptions = append(f.listOptions, options)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return nil, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

// ListCandidateInvalidationTargetsBySitting answers that the Sitting has no
// candidate whose views a lifecycle change must invalidate.
func (f *sittingLifecycleStore) ListCandidateInvalidationTargetsBySitting(context.Context, model.ExamSittingID, model.UserID, int) ([]model.UserID, error) {
	return nil, nil
}

func (f *sittingLifecycleStore) advancedIDs() []model.ExamSittingID {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]model.ExamSittingID, 0, len(f.advanced))
	for _, input := range f.advanced {
		ids = append(ids, input.SittingID)
	}
	return ids
}

type (
	lifecycleAuthoringStore  struct{ store.ExamAuthoringStore }
	lifecycleMembershipStore struct{ store.AcademicUnitMemberStore }
)

// newLifecycleSittings builds the production Sitting service over the store,
// with the real system auditor and lifecycle Job factory. The lifecycle Jobs
// act as the system, so no caller holds a grant.
func newLifecycleSittings(t *testing.T, persistence *sittingLifecycleStore) (*sitting.Service, *accesstest.Fixture) {
	t.Helper()
	control := accesstest.New(t, accesstest.Options{})
	composer, err := mail.NewSittingComposer(mail.SittingComposerDependencies{Renderer: mailtest.NewRenderer(t), Sender: &mailSenderFake{},
		Now: time.Now})
	require.NoError(t, err)
	service, err := sitting.New(sitting.Dependencies{
		Persistence: persistence,
		Access:      lifecycleAuthoringStore{},
		Memberships: lifecycleMembershipStore{},
		Authorizer:  control.Authorizer,
		Auditor:     control.Auditor,
		Publisher:   &realtimetest.Publisher{},
		Jobs:        NewExamSittingLifecycleJobFactory(time.Now, model.NewJobID),
		Mail: sitting.ScheduleMail{Composer: composer, Classes: &sittingMailClassStoreFake{},
			Revisions: sittingMailRevisionStoreFake{}},
		Now:   time.Now,
		NewID: model.NewExamSittingID,
	})
	require.NoError(t, err)
	return service, control
}

// requireLifecycleJobAudit asserts the newest system attempt names the Job
// execution that did the work and is the one handed to the Store.
func requireLifecycleJobAudit(t *testing.T, control *accesstest.Fixture, system invocation.System, storeAuditID model.AuditEventID) {
	t.Helper()
	attempts := control.Audits.Attempts()
	require.NotEmpty(t, attempts, "system attempts")
	attempt := attempts[len(attempts)-1]
	require.Equal(t, storeAuditID, attempt.ID, "the Store completes the attempt the service began")
	require.Empty(t, attempt.ActorID, "a lifecycle Job acts as no User")
	var parameters struct {
		Value struct {
			JobID        string `json:"job_id"`
			JobAttemptID string `json:"job_attempt_id"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal(attempt.Parameters, &parameters))
	require.Equal(t, system.JobID.String(), parameters.Value.JobID, "audited Job")
	require.Equal(t, system.AttemptID.String(), parameters.Value.JobAttemptID, "audited Job attempt")
}

func mustExamSittingLifecycleJob(t *testing.T, sittingID model.ExamSittingID, at time.Time) *model.Job {
	t.Helper()
	command, err := model.EncodeExamSittingLifecycleCommand(model.ExamSittingLifecycleCommand{ExamSittingID: sittingID})
	require.NoError(t, err)
	job, err := model.NewJob(model.NewJobID(), model.JobTypeExamSittingLifecycle, command, "exam-sitting-test", at, at, 8)
	require.NoError(t, err)
	return job
}

func lifecycleJobResult(t *testing.T, sittingID model.ExamSittingID, changed bool) *store.ExamSittingLifecycleResult {
	t.Helper()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sitting, err := model.NewExamSitting(sittingID, model.NewExamID(), model.NewExamRevisionID(), model.NewClassID(), at.Add(time.Hour), at.Add(2*time.Hour), at)
	require.NoError(t, err)
	result := &store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: sitting}, Changed: changed}
	if changed {
		err = sitting.Open(at.Add(time.Hour))
		require.NoError(t, err)
		result.Transition = store.ExamSittingTransitionOpened
	}
	return result
}

func assertExamSittingLifecycleJob(t *testing.T, job *model.Job, wantType model.JobType,
	sittingID model.ExamSittingID, phase model.ExamSittingLifecycleJobPhase, revision int64,
	createdAt, availableAt time.Time,
) {
	t.Helper()
	require.NotNil(t, job)
	require.Equal(t, wantType, job.Type)
	require.Equal(t, model.JobDedupeActive, job.DedupePolicy)
	require.Equal(t, model.JobStatusQueued, job.Status)
	require.Equal(t, 8, job.MaximumAttempts)
	require.Equal(t, createdAt, job.CreatedAt)
	require.Equal(t, availableAt, job.AvailableAt)
	wantKey, err := model.ExamSittingLifecycleDedupeKey(sittingID, phase, revision)
	require.NoError(t, err)
	require.Equal(t, wantKey, job.DedupeKey, "dedupe key")
	command, err := model.DecodeExamSittingLifecycleCommand(job.Command)
	require.NoError(t, err, "command = %#v", command)
	require.Equal(t, sittingID, command.ExamSittingID, "command = %#v", command)
}

func TestExamSittingLifecycleJobReconcileLeavesClosingToDedicatedSealingJob(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sitting, err := model.NewExamSitting(model.NewExamSittingID(), model.NewExamID(), model.NewExamRevisionID(), model.NewClassID(), at.Add(time.Hour), at.Add(2*time.Hour), at)
	require.NoError(t, err)
	err = sitting.Open(at.Add(time.Hour))
	require.NoError(t, err)
	err = sitting.EnterClosing(model.ExamSittingReasonScheduledEndReached, at.Add(2*time.Hour))
	require.NoError(t, err)
	closing := *sitting
	record := mustExamSittingLifecycleJob(t, sitting.ID, at)
	attemptID := model.NewJobAttemptID()
	persistence := newSittingLifecycleStore(&store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: &closing}, Changed: true, Transition: store.ExamSittingTransitionScheduledEndReached})
	sittings, control := newLifecycleSittings(t, persistence)
	outcome := (examSittingLifecycleHandler{sittings: sittings}).Run(context.Background(), job.NewExecution(record, &model.JobAttempt{ID: attemptID}, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "result/call")
	var result ExamSittingLifecycleJobResult
	require.NoError(t, json.Unmarshal(outcome.Result, &result), "result/call")
	require.Equal(t, model.ExamSittingClosing, result.State, "result/call")
	requireLifecycleJobAudit(t, control, invocation.System{JobID: record.ID, AttemptID: attemptID}, persistence.advanced[0].AuditEventID)

	// Another node may close before the lifecycle read. The dedicated sealing
	// handler still owns any closure work, so the handler reports AdvanceDue's
	// authoritative value and never invokes the legacy zero-Attempt closer.
	racing, _ := newLifecycleSittings(t, newSittingLifecycleStore(&store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: &closing}}))
	outcome = (examSittingLifecycleHandler{sittings: racing}).Run(context.Background(), job.NewExecution(record, &model.JobAttempt{ID: attemptID}, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.NoError(t, json.Unmarshal(outcome.Result, &result))
	require.Equal(t, model.ExamSittingClosing, result.State)
}
