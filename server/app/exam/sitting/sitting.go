// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// LifecycleJobFactory builds validated lifecycle jobs; the store inserts them
// in the same transaction as the sitting change.
type LifecycleJobFactory interface {
	BoundaryJobs(model.ExamSittingID, int64, time.Time, time.Time) (*model.Job, *model.Job, error)
	DeadlineJob(model.ExamSittingID, int64, time.Time) (*model.Job, error)
	FinalizeJob(model.ExamSittingID, int64, time.Time) (*model.Job, error)
}

// ScheduleMailRequest is the schedule a notice announces. PriorClassID is the
// class before an update or cancellation.
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

// Service schedules exam sittings and drives their lifecycle.
type Service struct {
	sittings            store.ExamSittingStore
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer *access.Authorizer
	// auditor begins and fails manager audits; the store completes them with
	// the change it commits.
	auditor access.MutationAuditAdapter
	// systemAuditor takes an invocation.System rather than an Invocation, so a
	// job cannot act as a user.
	systemAuditor access.SystemAuditAdapter
	publisher     realtime.Publisher
	jobs          LifecycleJobFactory
	mail          ScheduleMail
	now           func() time.Time
	newID         func() model.ExamSittingID
	operational   operational.Recorder
}

// Dependencies are what New needs; only Operational may be nil.
type Dependencies struct {
	Persistence store.ExamSittingStore
	Access      store.ExamAuthoringStore
	Memberships store.AcademicUnitMemberStore
	Authorizer  *access.Authorizer
	Auditor     *access.Auditor
	Publisher   realtime.Publisher
	Jobs        LifecycleJobFactory
	Mail        ScheduleMail
	Now         func() time.Time
	NewID       func() model.ExamSittingID
	Operational operational.Recorder
}

// New returns an error when a required dependency is missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Persistence == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Jobs == nil ||
		deps.Mail.Composer == nil ||
		deps.Mail.Revisions == nil ||
		deps.Mail.Classes == nil ||
		deps.Now == nil ||
		deps.NewID == nil {
		return nil, errors.New("Exam Sitting dependencies are required")
	}

	return &Service{
		sittings:            deps.Persistence,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor.MutationAudit(),
		systemAuditor:       deps.Auditor.SystemAudit(),
		publisher:           deps.Publisher,
		jobs:                deps.Jobs,
		mail:                deps.Mail,
		now:                 deps.Now,
		newID:               deps.NewID,
		operational:         deps.Operational,
	}, nil
}
