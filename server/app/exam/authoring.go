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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Authorizer is the Access Control the Exam authoring and publication use
// cases ask; *access.Authorizer satisfies it and tests substitute a fake.
type Authorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
	AuthorizeExamList(context.Context, invocation.Invocation) (store.ExamListVisibility, error)
}

type Auditor interface {
	Begin(context.Context, access.Mutation) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

type ManagerMailRelationship string

const (
	ManagerMailRelationshipManager         ManagerMailRelationship = "manager"
	ManagerMailRelationshipOwner           ManagerMailRelationship = "owner"
	ManagerMailRelationshipNoLongerManager ManagerMailRelationship = "no_longer_manager"
)

type ManagerMailPreparation struct {
	Recipient    *model.User
	OccurrenceID model.MailOccurrenceID
	TemplateKey  model.MailTemplateKey
	ExamTitle    string
	Relationship ManagerMailRelationship
	ActionAt     time.Time
}

type ManagerMailPreparer interface {
	PrepareManagerMail(ManagerMailPreparation) (*store.ExamManagerMail, error)
}

// Authoring owns Exam creation, Exam Draft editing, the Exam catalog,
// archival, and Exam Manager membership.
type Authoring struct {
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore
	// users reads a prospective Exam Manager and the recipients of manager
	// notices.
	users store.UserStore
	// sittings finds the Sitting boards an archived Exam refreshes.
	sittings store.ExamSittingStore

	mail              ManagerMailPreparer
	authorizer        Authorizer
	auditor           Auditor
	publisher         realtime.Publisher
	now               func() time.Time
	newID             func() model.ExamID
	institutionOrigin string
	operational       OperationalRecorder
}

// OperationalRecorder receives the outcome of each use case the module
// observes. It is optional; a module built without one records nothing.
type OperationalRecorder interface {
	RecordOperational(subsystem, event string, err error)
}

// AuthoringDependencies are the collaborators NewAuthoring validates once and
// distributes to the Authoring.
type AuthoringDependencies struct {
	Persistence       store.ExamAuthoringStore
	Memberships       store.AcademicUnitMemberStore
	Users             store.UserStore
	Sittings          store.ExamSittingStore
	Mail              ManagerMailPreparer
	Authorizer        Authorizer
	Auditor           Auditor
	Publisher         realtime.Publisher
	Now               func() time.Time
	NewID             func() model.ExamID
	InstitutionOrigin string
	Operational       OperationalRecorder
}

// NewAuthoring returns an Authoring service, or an error when a required
// dependency is missing.
func NewAuthoring(deps AuthoringDependencies) (*Authoring, error) {
	if deps.Persistence == nil ||
		deps.Memberships == nil ||
		deps.Users == nil ||
		deps.Sittings == nil ||
		deps.Mail == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Now == nil ||
		deps.NewID == nil {
		return nil, errors.New("exam authoring dependencies are required")
	}

	return &Authoring{
		exams:               deps.Persistence,
		academicUnitMembers: deps.Memberships,
		users:               deps.Users,
		sittings:            deps.Sittings,
		mail:                deps.Mail,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor,
		publisher:           deps.Publisher,
		now:                 deps.Now,
		newID:               deps.NewID,
		institutionOrigin:   deps.InstitutionOrigin,
		operational:         deps.Operational,
	}, nil
}
