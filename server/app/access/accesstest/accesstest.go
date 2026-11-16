// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package accesstest builds a real access.Authorizer and access.Auditor over
// in-memory stores. Tests grant a caller Roles instead of faking the
// authorization decision, so they exercise the same code production runs.
package accesstest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Grant gives a User the actions at one scope. A zero ScopeType grants them
// Institution-wide.
type Grant struct {
	UserID    model.UserID
	Actions   []model.Action
	ScopeType model.RoleScopeType
	ScopeID   string
}

// Options describe the world the authorizer decides in.
type Options struct {
	Grants []Grant
	// Units are the Academic Units grants and resources may name. A unit
	// with no Institution joins the fixture's Institution, and a grant at an
	// Academic Unit that is not listed creates that unit as a root.
	Units []*model.AcademicUnit
	// Users resolves User resources. Pass the test's own User store so the
	// authorizer and the service under test see the same Users; nil means
	// no User resolves.
	Users store.UserStore
	// Classes, AcademicPeriods, Programmes and ProgrammeLevels resolve the
	// resources of those types. Pass the test's own stores so the authorizer
	// and the service under test see the same records. A nil Classes or
	// AcademicPeriods resolves nothing; nil Programmes or ProgrammeLevels
	// leaves those resources unavailable, as production does without them.
	Classes         store.ClassStore
	AcademicPeriods store.AcademicPeriodStore
	Programmes      store.ProgrammeStore
	ProgrammeLevels store.ProgrammeLevelStore
	// Exams, Sittings and Submissions resolve Exam, Exam Sitting and
	// Submission resources through their Resolve methods. Pass the test's own
	// stores so both sides see the same ownership; nil resolves nothing.
	Exams       store.ExamAuthoringStore
	Sittings    store.ExamSittingStore
	Submissions store.ExamSubmissionStore
	// Now is the decision clock; nil means time.Now.
	Now func() time.Time
	// Observe, when set, is told what the authorizer and auditor do, in
	// order, so a test can place them in the log its other fakes share:
	//   "grants"                    the caller's Role Bindings were read
	//   "decision:<action>:<status>" a decision was recorded
	//   "audit-begin:<action>"       a mutation attempt was recorded
	//   "audit-<status>"             an attempt was completed
	Observe func(string)
}

// Fixture is a ready authorizer and the auditor recording its decisions.
type Fixture struct {
	Institution *model.Institution
	Authorizer  *access.Authorizer
	Auditor     *access.Auditor
	Audits      *Audits
}

// New builds the Fixture.
func New(t testing.TB, options Options) *Fixture {
	t.Helper()

	institution := &model.Institution{ID: model.NewInstitutionID(), Name: "Test Institution"}
	units := &academicUnits{units: map[model.AcademicUnitID]*model.AcademicUnit{}}
	for _, unit := range options.Units {
		if unit.InstitutionID.IsZero() {
			unit.InstitutionID = institution.ID
		}
		units.units[unit.ID] = unit
	}
	for _, grant := range options.Grants {
		id := model.AcademicUnitID(grant.ScopeID)
		if _, known := units.units[id]; grant.ScopeType == model.RoleScopeAcademicUnit && !known {
			units.units[id] = &model.AcademicUnit{ID: id, InstitutionID: institution.ID, Name: "Unit " + grant.ScopeID}
		}
	}
	users := options.Users
	if users == nil {
		users = noUsers{}
	}
	var classes store.ClassStore = noClasses{}
	if options.Classes != nil {
		classes = options.Classes
	}
	var periods store.AcademicPeriodStore = noAcademicPeriods{}
	if options.AcademicPeriods != nil {
		periods = options.AcademicPeriods
	}
	var exams store.ExamAuthoringStore = noExams{}
	if options.Exams != nil {
		exams = options.Exams
	}
	var sittings store.ExamSittingStore = noSittings{}
	if options.Sittings != nil {
		sittings = options.Sittings
	}
	var submissions store.ExamSubmissionStore = noSubmissions{}
	if options.Submissions != nil {
		submissions = options.Submissions
	}

	roles := &roleStore{roles: map[model.RoleID]*model.Role{}}
	observe := options.Observe
	if observe == nil {
		observe = func(string) {}
	}
	bindings := &roleBindingStore{observe: observe}
	for _, grant := range options.Grants {
		role := &model.Role{ID: model.NewRoleID(), Name: "test-" + string(grant.ScopeType)}
		for _, action := range grant.Actions {
			role.Permissions = append(role.Permissions, string(action))
		}
		roles.roles[role.ID] = role

		scopeType, scopeID := grant.ScopeType, grant.ScopeID
		if scopeType == "" {
			scopeType, scopeID = model.RoleScopeInstitution, institution.ID.String()
		}
		bindings.bindings = append(bindings.bindings, &model.RoleBinding{
			ID: model.NewRoleBindingID(), UserID: grant.UserID, RoleID: role.ID,
			ScopeType: scopeType, ScopeID: scopeID,
		})
	}

	institutions := institutionStore{institution: institution}
	audits := &Audits{observe: observe}
	auditor, err := access.NewAuditor(access.AuditorDependencies{Audits: audits, Institutions: institutions, NodeID: "test-node"})
	require.NoError(t, err)

	resolver, err := access.NewScopeResolver(access.ScopeResolverDependencies{
		Institutions: institutions, AcademicUnits: units, Classes: classes, Users: users,
		ClassMembers: noClassMembers{}, Exams: exams, Sittings: sittings,
		Submissions: submissions, AcademicPeriods: periods,
		Programmes: options.Programmes, ProgrammeLevels: options.ProgrammeLevels,
	})
	require.NoError(t, err)

	authorizer, err := access.NewAuthorizer(access.AuthorizerDependencies{
		Roles: roles, Bindings: bindings, Resolver: resolver, Audit: auditor, Now: options.Now,
	})
	require.NoError(t, err)

	return &Fixture{Institution: institution, Authorizer: authorizer, Auditor: auditor, Audits: audits}
}

// Audits is an in-memory audit store that keeps every event it is given.
type Audits struct {
	store.AuditStore
	observe func(string)

	mu          sync.Mutex
	events      []*model.AuditEvent
	attempts    map[model.AuditEventID]bool
	attemptErr  error
	decisionErr error
	completeErr error
}

// FailAttempts makes every later save of a mutation attempt return err, as
// an audit store that became unavailable after the decision would.
func (a *Audits) FailAttempts(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attemptErr = err
}

// FailDecisions makes every later save of an authorization decision return
// err, as an audit store that became unavailable would.
func (a *Audits) FailDecisions(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.decisionErr = err
}

// FailCompletions makes every later completion of an attempt return err.
func (a *Audits) FailCompletions(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completeErr = err
}

// Attempts returns the recorded mutation attempts, completed or not.
func (a *Audits) Attempts() []*model.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var attempts []*model.AuditEvent
	for _, event := range a.events {
		if a.attempts[event.ID] {
			attempts = append(attempts, event)
		}
	}
	return attempts
}

// Decisions returns the recorded authorization decisions, in order.
func (a *Audits) Decisions() []*model.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var decisions []*model.AuditEvent
	for _, event := range a.events {
		if !a.attempts[event.ID] {
			decisions = append(decisions, event)
		}
	}
	return decisions
}

// Save keeps the event, refusing a mutation attempt after FailAttempts.
func (a *Audits) Save(_ context.Context, event *model.AuditEvent) (*model.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if event.Status == model.AuditStatusAttempt {
		a.observe("audit-begin:" + event.Action)
		if a.attemptErr != nil {
			return nil, a.attemptErr
		}
	} else {
		a.observe("decision:" + event.Action + ":" + string(event.Status))
		if a.decisionErr != nil {
			return nil, a.decisionErr
		}
	}
	saved := *event
	if saved.ID.IsZero() {
		saved.ID = model.NewAuditEventID()
	}
	a.events = append(a.events, &saved)
	if saved.Status == model.AuditStatusAttempt {
		if a.attempts == nil {
			a.attempts = map[model.AuditEventID]bool{}
		}
		a.attempts[saved.ID] = true
	}
	return &saved, nil
}

// Complete records the outcome of a kept attempt.
func (a *Audits) Complete(_ context.Context, completion *store.AuditCompletion) (*model.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observe("audit-" + string(completion.Status))
	if a.completeErr != nil {
		return nil, a.completeErr
	}
	for _, event := range a.events {
		if event.ID == completion.ID {
			event.Status = completion.Status
			event.ErrorCode = completion.ErrorCode
			return event, nil
		}
	}
	return nil, store.NewErrNotFound("audit_event", completion.ID.String())
}

type institutionStore struct {
	store.InstitutionStore
	institution *model.Institution
}

func (s institutionStore) Get(context.Context, model.InstitutionID) (*model.Institution, error) {
	return s.institution, nil
}

func (s institutionStore) GetSingleton(context.Context) (*model.Institution, error) {
	return s.institution, nil
}

type academicUnits struct {
	store.AcademicUnitStore
	units map[model.AcademicUnitID]*model.AcademicUnit
}

func (s *academicUnits) Get(_ context.Context, id model.AcademicUnitID) (*model.AcademicUnit, error) {
	unit, ok := s.units[id]
	if !ok {
		return nil, store.NewErrNotFound("academic_unit", id.String())
	}
	return unit, nil
}

// ListAncestors returns the unit and its parents, nearest first.
func (s *academicUnits) ListAncestors(_ context.Context, id model.AcademicUnitID) ([]*model.AcademicUnit, error) {
	var chain []*model.AcademicUnit
	for unit, ok := s.units[id]; ok; unit, ok = s.units[unit.ParentID] {
		chain = append(chain, unit)
		if unit.ParentID.IsZero() {
			break
		}
	}
	return chain, nil
}

type roleStore struct {
	store.RoleStore
	roles map[model.RoleID]*model.Role
}

func (s *roleStore) GetByIDs(_ context.Context, ids []model.RoleID) ([]*model.Role, error) {
	roles := make([]*model.Role, 0, len(ids))
	for _, id := range ids {
		if role, ok := s.roles[id]; ok {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

type roleBindingStore struct {
	store.RoleBindingStore
	observe  func(string)
	bindings []*model.RoleBinding
}

// ListActiveByUser returns every binding of the User; test grants never end.
func (s *roleBindingStore) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.RoleBinding, error) {
	s.observe("grants")
	var bindings []*model.RoleBinding
	for _, binding := range s.bindings {
		if binding.UserID == userID {
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}

type noUsers struct{ store.UserStore }

func (noUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	return nil, store.NewErrNotFound("user", id.String())
}

type noClasses struct{ store.ClassStore }

func (noClasses) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	return nil, store.NewErrNotFound("class", id.String())
}

type noExams struct{ store.ExamAuthoringStore }

func (noExams) Resolve(_ context.Context, id model.ExamID) (*model.Exam, error) {
	return nil, store.NewErrNotFound("exam", id.String())
}

type noSittings struct{ store.ExamSittingStore }

func (noSittings) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	return nil, store.NewErrNotFound("exam_sitting", id.String())
}

type noSubmissions struct{ store.ExamSubmissionStore }

func (noSubmissions) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return nil, store.NewErrNotFound("submission", id.String())
}

type (
	noClassMembers    struct{ store.ClassMemberStore }
	noAcademicPeriods struct{ store.AcademicPeriodStore }
)
