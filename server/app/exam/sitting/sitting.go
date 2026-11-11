// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

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

// Authorizer is the part of *access.Authorizer the Sitting use cases call:
// the permission decision, and the audited denial of a Browser Activity read
// refused before a permission is checked.
type Authorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
	RecordDenial(context.Context, invocation.Invocation, model.Action, model.Resource, model.AcademicUnitID) error
}

// Auditor records the attempt and failure of each audited manager command;
// the Store completes the audit event in the transaction that commits the
// change.
type Auditor interface {
	Begin(context.Context, access.Mutation) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// SystemAuditor owns audit creation for trusted durable work. It deliberately
// accepts invocation.System rather than an Invocation so Jobs cannot impersonate a User.
// access.SystemAuditAdapter satisfies it and tests substitute a fake.
type SystemAuditor interface {
	BeginJob(
		context.Context,
		invocation.System,
		model.Action,
		model.Resource,
		model.RoleScopeType,
		string,
		string,
		map[string]any,
	) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// LifecycleJobFactory constructs validated durable intents. Persistence owns
// inserting them atomically with the corresponding Sitting mutation.
type LifecycleJobFactory interface {
	BoundaryJobs(model.ExamSittingID, int64, time.Time, time.Time) (*model.Job, *model.Job, error)
	DeadlineJob(model.ExamSittingID, int64, time.Time) (*model.Job, error)
	FinalizeJob(model.ExamSittingID, int64, time.Time) (*model.Job, error)
}

// ScheduleMailPreparer owns the bounded, presentation-ready fan-out intent.
// The Sitting service supplies only safe schedule facts; it cannot select
// recipients, delivery work, ciphertext, or fan-out deadlines.
type ScheduleMailPreparer interface {
	Prepare(context.Context, ScheduleMailRequest) (*store.ExamSittingMailFanout, error)
}

// ScheduleMailRequest is the committed schedule fact a schedule notice
// announces. PriorClassID is the Class the Sitting had before an update or
// a cancellation.
type ScheduleMailRequest struct {
	ActorUserID     model.UserID
	ExamID          model.ExamID
	ExamRevisionID  model.ExamRevisionID
	SittingID       model.ExamSittingID
	SittingRevision int64
	ClassID         model.ClassID
	PriorClassID    model.ClassID
	StartsAt        time.Time
	EndsAt          time.Time
	ChangeKind      store.ExamSittingMailChangeKind
}

// Service schedules Exam Sittings and drives their lifecycle, both on manager
// commands and on lifecycle Jobs.
type Service struct {
	sittings store.ExamSittingStore
	// exams reads the Exam ownership a manager decision is authorized against.
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer    Authorizer
	auditor       Auditor
	systemAuditor SystemAuditor
	publisher     realtime.Publisher
	jobs          LifecycleJobFactory
	mail          ScheduleMailPreparer
	now           func() time.Time
	newID         func() model.ExamSittingID
	operational   OperationalRecorder
}

// OperationalRecorder receives the outcome of each use case the module
// observes. It is optional; a module built without one records nothing.
type OperationalRecorder interface {
	RecordOperational(subsystem, event string, err error)
}

// Dependencies are the collaborators New validates once and distributes to
// the Service.
type Dependencies struct {
	Persistence   store.ExamSittingStore
	Access        store.ExamAuthoringStore
	Memberships   store.AcademicUnitMemberStore
	Authorizer    Authorizer
	Auditor       Auditor
	SystemAuditor SystemAuditor
	Publisher     realtime.Publisher
	Jobs          LifecycleJobFactory
	Mail          ScheduleMailPreparer
	Now           func() time.Time
	NewID         func() model.ExamSittingID
	Operational   OperationalRecorder
}

// New returns an Exam Sitting Service, or an error when a required dependency
// is missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Persistence == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.SystemAuditor == nil ||
		deps.Publisher == nil ||
		deps.Jobs == nil ||
		deps.Mail == nil ||
		deps.Now == nil ||
		deps.NewID == nil {
		return nil, errors.New("Exam Sitting dependencies are required")
	}

	return &Service{
		sittings:            deps.Persistence,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor,
		systemAuditor:       deps.SystemAuditor,
		publisher:           deps.Publisher,
		jobs:                deps.Jobs,
		mail:                deps.Mail,
		now:                 deps.Now,
		newID:               deps.NewID,
		operational:         deps.Operational,
	}, nil
}
