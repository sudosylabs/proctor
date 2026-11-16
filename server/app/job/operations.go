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
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Operations holds the operator job use cases. A nil Operations answers that
// jobs are unavailable.
type Operations struct {
	deps OperationsDependencies
	// audit fails the attempt of a cancel or retry that does not commit; the
	// store completes a committed one.
	audit access.MutationAuditAdapter
}

// OperationsDependencies are the collaborators of Operations, validated once
// by NewOperations.
type OperationsDependencies struct {
	Jobs          *Engine
	Authorization InstitutionAuthorization
	Audit         *access.Auditor
	Users         store.UserStore
	ExamExports   store.ExamExportStore
	Now           func() time.Time
}

// InstitutionAuthorization authorizes a job operator action on the
// institution. It is not access.Authorizer.AuthorizeInstitution because an
// unreadable institution is reported with the job operator codes here.
type InstitutionAuthorization struct {
	Authorizer   *access.Authorizer
	Institutions store.InstitutionStore
}

// AuthorizeInstitution reads the institution and authorizes action on it.
func (a InstitutionAuthorization) AuthorizeInstitution(
	ctx context.Context,
	inv invocation.Invocation,
	action model.Action,
) (model.Resource, error) {
	institution, err := a.Institutions.GetSingleton(ctx)
	if err != nil {
		return model.Resource{}, operationsStoreErrors.Translate(err)
	}

	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err = a.Authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return model.Resource{}, err
	}

	return resource, nil
}

// NewOperations refuses a missing collaborator.
func NewOperations(deps OperationsDependencies) (*Operations, error) {
	if deps.Jobs == nil {
		return nil, errors.New("job operations engine is required")
	}
	if deps.Authorization.Authorizer == nil || deps.Authorization.Institutions == nil {
		return nil, errors.New("job operations authorization is required")
	}
	if deps.Audit == nil {
		return nil, errors.New("job operations audit is required")
	}
	if deps.Users == nil {
		return nil, errors.New("job operations initiator names are required")
	}
	if deps.ExamExports == nil {
		return nil, errors.New("job operations exam export titles are required")
	}
	if deps.Now == nil {
		return nil, errors.New("job operations clock is required")
	}
	return &Operations{deps: deps, audit: deps.Audit.MutationAudit()}, nil
}

// ListJobs lists one page of operator-visible jobs, with who started each and
// what it is about.
func (s *Operations) ListJobs(ctx context.Context, inv invocation.Invocation, query ListQuery) (Page, error) {
	if s == nil {
		return Page{}, apperror.New("job.unavailable")
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionJobView); err != nil {
		return Page{}, err
	}

	page, err := s.deps.Jobs.List(ctx, query)
	if err != nil {
		return Page{}, operationsStoreErrors.Translate(err)
	}
	if err = s.presentJobs(ctx, page.Items); err != nil {
		return Page{}, operationsStoreErrors.Translate(err)
	}

	return page, nil
}

// CountRecentJobFailures counts operator-visible jobs that failed within the
// span, authorized and audited as ListJobs is, stopping at maximum.
func (s *Operations) CountRecentJobFailures(
	ctx context.Context,
	inv invocation.Invocation,
	within time.Duration,
	maximum int,
) (int, error) {
	if s == nil {
		return 0, apperror.New("job.unavailable")
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionJobView); err != nil {
		return 0, err
	}

	total, err := s.deps.Jobs.CountRecentFailures(ctx, within, maximum)
	if err != nil {
		return 0, operationsStoreErrors.Translate(err)
	}

	return total, nil
}

// GetJob returns one operator-visible job.
func (s *Operations) GetJob(ctx context.Context, inv invocation.Invocation, id model.JobID) (View, error) {
	if s == nil {
		return View{}, apperror.New("job.unavailable")
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionJobView); err != nil {
		return View{}, err
	}
	view, err := s.deps.Jobs.Get(ctx, id)
	if err != nil {
		return View{}, operationsStoreErrors.Translate(err)
	}
	views := []View{view}
	if err = s.presentJobs(ctx, views); err != nil {
		return View{}, operationsStoreErrors.Translate(err)
	}
	return views[0], nil
}

// ListJobAttempts lists the execution attempts of one job.
func (s *Operations) ListJobAttempts(
	ctx context.Context,
	inv invocation.Invocation,
	query AttemptListQuery,
) (AttemptPage, error) {
	if s == nil {
		return AttemptPage{}, apperror.New("job.unavailable")
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionJobView); err != nil {
		return AttemptPage{}, err
	}

	page, err := s.deps.Jobs.Attempts(ctx, query)
	if err != nil {
		return AttemptPage{}, operationsStoreErrors.Translate(err)
	}

	return page, nil
}

// CancelJob cancels a job of a cancelable type, with audit.
func (s *Operations) CancelJob(ctx context.Context, inv invocation.Invocation, id model.JobID) (View, error) {
	if s == nil {
		return View{}, apperror.New("job.unavailable")
	}
	return s.controlJob(ctx, inv, "cancel", id, s.deps.Jobs.PrepareCancellation, s.deps.Jobs.Cancel)
}

// RetryJob retries a job whose type allows an explicit retry from its current
// status, with audit.
func (s *Operations) RetryJob(ctx context.Context, inv invocation.Invocation, id model.JobID) (View, error) {
	if s == nil {
		return View{}, apperror.New("job.unavailable")
	}
	return s.controlJob(ctx, inv, "retry", id, s.deps.Jobs.PrepareRetry, s.deps.Jobs.Retry)
}

func (s *Operations) controlJob(
	ctx context.Context,
	inv invocation.Invocation,
	operation string,
	id model.JobID,
	prepare func(context.Context, model.JobID) (ControlTarget, error),
	apply func(context.Context, ControlTarget, AuditReference) (View, error),
) (View, error) {
	resource, err := s.deps.Authorization.AuthorizeInstitution(ctx, inv, model.ActionJobManage)
	if err != nil {
		return View{}, err
	}

	target, err := prepare(ctx, id)
	if err != nil {
		return View{}, operationsStoreErrors.Translate(err)
	}

	view := target.View()
	auditID, err := s.audit.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionJobManage,
		Resource:   resource,
		Operation:  operation,
		Value:      map[string]any{"job_id": view.ID.String(), "type": string(view.Type), "status": string(view.Status)},
	})
	if err != nil {
		return View{}, err
	}

	updated, err := apply(ctx, target, AuditReference{EventID: auditID, At: s.deps.Now()})
	if err != nil {
		_ = s.audit.Fail(ctx, auditID, "job.conflict")
		return View{}, operationsStoreErrors.Translate(err)
	}

	return updated, nil
}

// presentJobs reads each kind of fact once for the whole page. A reader of
// jobs holds institution-wide job.view, which covers both.
func (s *Operations) presentJobs(ctx context.Context, views []View) error {
	userIDs := make([]model.UserID, 0, len(views))
	subjectJobs := make(map[model.JobType][]model.JobID)
	for _, view := range views {
		if !view.CreatedByUserID.IsZero() {
			userIDs = append(userIDs, view.CreatedByUserID)
		}
		if s.subjectResolver(view.Type) != nil {
			subjectJobs[view.Type] = append(subjectJobs[view.Type], view.ID)
		}
	}

	names := make(map[model.UserID]string)
	if len(userIDs) > 0 {
		found, err := s.deps.Users.ListDisplayNames(ctx, userIDs)
		if err != nil {
			return err
		}

		for _, name := range found {
			names[name.UserID] = name.DisplayName
		}
	}

	subjects := make(map[model.JobID]string)
	for jobType, ids := range subjectJobs {
		found, err := s.subjectResolver(jobType)(ctx, ids)
		if err != nil {
			return err
		}

		maps.Copy(subjects, found)
	}

	for index := range views {
		views[index].CreatedByDisplayName = names[views[index].CreatedByUserID]
		views[index].Subject = boundSubject(subjects[views[index].ID])
	}

	return nil
}

type subjectReader func(context.Context, []model.JobID) (map[model.JobID]string, error)

// subjectResolver returns nil for job types that have no subject.
func (s *Operations) subjectResolver(jobType model.JobType) subjectReader {
	switch jobType {
	case model.JobTypeExamExportBuild:
		return s.examExportBuildSubjects
	default:
		return nil
	}
}

// examExportBuildSubjects names each export build's exam by the title of the
// sealed revision its sitting runs.
func (s *Operations) examExportBuildSubjects(ctx context.Context, ids []model.JobID) (map[model.JobID]string, error) {
	titles, err := s.deps.ExamExports.ListBuildTitles(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[model.JobID]string, len(titles))
	for _, title := range titles {
		result[title.JobID] = title.Title
	}
	return result, nil
}

// maximumSubjectLength bounds the subject an operator sees, in characters.
const maximumSubjectLength = 200

func boundSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if utf8.RuneCountInString(subject) <= maximumSubjectLength {
		return subject
	}
	return string([]rune(subject)[:maximumSubjectLength])
}

var operationsStoreErrors = apperror.StoreTable{
	Rules: []apperror.StoreRule{
		{Kind: apperror.StoreSentinel, Sentinel: ErrQueryInvalid, Code: "job.query.invalid"},
		{Kind: apperror.StoreSentinel, Sentinel: ErrCancelUnsupported, Code: "job.cancel.unsupported"},
		{Kind: apperror.StoreSentinel, Sentinel: ErrRetryUnsupported, Code: "job.retry.unsupported"},
	},
	NotFound:    "resource.not_found",
	Conflict:    "job.conflict",
	Unavailable: "job.unavailable",
}
