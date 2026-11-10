// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type mutationAttemptAuditorFake struct {
	events   *[]string
	beginID  string
	beginIDs []string
	beginErr error
	failErr  error
	failID   string
	failCode string
	attempt  access.Mutation
	attempts []access.Mutation
}

func (a *mutationAttemptAuditorFake) BeginAtScope(_ context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID, operation string, value, prior map[string]any) (model.AuditEventID, error) {
	*a.events = append(*a.events, "begin-at-scope")
	a.attempt = access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   resource,
		ScopeType:  scopeType,
		ScopeID:    scopeID,
		Operation:  operation,
		Value:      value,
		Prior:      prior,
	}
	a.attempts = append(a.attempts, a.attempt)
	return model.AuditEventID(a.nextBeginID()), a.beginErr
}

func (a *mutationAttemptAuditorFake) Begin(_ context.Context, invocation invocation.Invocation, action model.Action, resource model.Resource, operation string, value, prior map[string]any) (model.AuditEventID, error) {
	*a.events = append(*a.events, "begin")
	a.attempt = access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   resource,
		Operation:  operation,
		Value:      value,
		Prior:      prior,
	}
	a.attempts = append(a.attempts, a.attempt)
	return model.AuditEventID(a.nextBeginID()), a.beginErr
}

func (a *mutationAttemptAuditorFake) BeginForSubject(ctx context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	var id model.AuditEventID
	var err error
	if attempt.ScopeType != "" {
		id, err = a.BeginAtScope(ctx, attempt.Invocation, attempt.Action, attempt.Resource, attempt.ScopeType, attempt.ScopeID, attempt.Operation, attempt.Value, attempt.Prior)
	} else {
		id, err = a.Begin(ctx, attempt.Invocation, attempt.Action, attempt.Resource, attempt.Operation, attempt.Value, attempt.Prior)
	}
	a.attempt.Subject = attempt.Subject
	a.attempts[len(a.attempts)-1].Subject = attempt.Subject
	return id, err
}

func (a *mutationAttemptAuditorFake) nextBeginID() string {
	if len(a.beginIDs) == 0 {
		return a.beginID
	}
	id := a.beginIDs[0]
	a.beginIDs = a.beginIDs[1:]
	return id
}

func (a *mutationAttemptAuditorFake) Fail(_ context.Context, id model.AuditEventID, code string) error {
	*a.events = append(*a.events, "fail")
	a.failID = id.String()
	a.failCode = code
	return a.failErr
}

type accessAcademicPeriodStoreFake struct {
	period  *model.AcademicPeriod
	periods map[string]*model.AcademicPeriod
	gets    int
	err     error
}

func (s *accessAcademicPeriodStoreFake) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	s.gets++
	if s.err != nil {
		return nil, s.err
	}
	if period := s.periods[id.String()]; period != nil {
		return period, nil
	}
	if s.period == nil {
		return nil, store.NewErrNotFound("academic_period", "missing")
	}
	return s.period, nil
}

type accessInstitutionStoreFake struct {
	store.InstitutionStore
	institution *model.Institution
	err         error
}

func (s *accessInstitutionStoreFake) Get(context.Context, model.InstitutionID) (*model.Institution, error) {
	return s.institution, s.err
}
func (s *accessInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, s.err
}

type accessClassStoreFake struct {
	store.ClassStore
	gets int
}

func (s *accessClassStoreFake) Get(context.Context, model.ClassID) (*model.Class, error) {
	s.gets++
	return nil, store.NewErrNotFound("class", "")
}

type accessUserStoreFake struct {
	store.UserStore
	user            *model.User
	match           store.UserVisibilityMatch
	matchVisibility store.UserVisibilityScope
	matchCalls      int
}

func (s *accessUserStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	if s.user == nil {
		return nil, store.NewErrNotFound("user", "")
	}
	return s.user, nil
}

func (s *accessUserStoreFake) MatchVisibility(_ context.Context, _ model.UserID, visibility store.UserVisibilityScope) (store.UserVisibilityMatch, error) {
	s.matchCalls++
	s.matchVisibility = visibility
	return s.match, nil
}

type accessClassMemberStoreFake struct{ store.ClassMemberStore }
type accessExamAuthoringStoreFake struct {
	store.ExamAuthoringStore
	exam *model.Exam
}

func (s *accessExamAuthoringStoreFake) Resolve(context.Context, model.ExamID) (*model.Exam, error) {
	return s.exam, nil
}

type accessExamSittingStoreFake struct {
	store.ExamSittingStore
	snapshot *store.ExamSittingSnapshot
}

type accessExamSubmissionStoreFake struct {
	store.ExamSubmissionStore
	authorization *store.ExamSubmissionAuthorization
}

func (s *accessExamSubmissionStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	if s.authorization == nil {
		return nil, store.NewErrNotFound("submission", "")
	}
	return s.authorization, nil
}

func (s *accessExamSittingStoreFake) Resolve(context.Context, model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if s.snapshot == nil {
		return nil, store.NewErrNotFound("exam_sitting", "")
	}
	return s.snapshot, nil
}

type accessRoleStoreFake struct {
	store.RoleStore
	roles []*model.Role
}

func (s *accessRoleStoreFake) GetByIDs(context.Context, []model.RoleID) ([]*model.Role, error) {
	return s.roles, nil
}

type accessRoleBindingStoreFake struct {
	store.RoleBindingStore
	bindings []*model.RoleBinding
	activeAt []time.Time
	lookup   func(time.Time) ([]*model.RoleBinding, error)
}

func (s *accessRoleBindingStoreFake) ListActiveByUser(_ context.Context, _ model.UserID, at time.Time) ([]*model.RoleBinding, error) {
	s.activeAt = append(s.activeAt, at)
	if s.lookup != nil {
		return s.lookup(at)
	}
	return s.bindings, nil
}

type accessDecisionAuditFake struct{}

func (accessDecisionAuditFake) RecordAuthorizationDecision(context.Context, model.Principal, model.Action, model.Resource, model.RoleScopeType, string, model.RequestMetadata, bool) error {
	return nil
}

func (accessDecisionAuditFake) RecordUserSearchDecision(context.Context, model.Principal, model.Resource, model.RoleScopeType, string, model.RequestMetadata, bool) error {
	return nil
}
