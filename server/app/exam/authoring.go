// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ManagerMailRelationship is what a manager notice tells its recipient they now
// are to the exam.
type ManagerMailRelationship string

// The relationships a manager notice can announce.
const (
	ManagerMailRelationshipManager         ManagerMailRelationship = "manager"
	ManagerMailRelationshipOwner           ManagerMailRelationship = "owner"
	ManagerMailRelationshipNoLongerManager ManagerMailRelationship = "no_longer_manager"
)

// ManagerMailPreparation is one manager notice to freeze. TemplateKey and
// Relationship must agree, or PrepareManagerMail rejects it.
type ManagerMailPreparation struct {
	Recipient    *model.User
	OccurrenceID model.MailOccurrenceID
	TemplateKey  model.MailTemplateKey
	ExamTitle    string
	Relationship ManagerMailRelationship
	ActionAt     time.Time
}

// ManagerMailPreparer freezes a manager notice so Authoring can commit it in
// the same transaction as the manager change.
type ManagerMailPreparer interface {
	PrepareManagerMail(ManagerMailPreparation) (*store.ExamManagerMail, error)
}

// Authoring owns exam creation, draft editing, the catalog, archiving and
// manager membership.
type Authoring struct {
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore
	users               store.UserStore
	sittings            store.ExamSittingStore

	mail       ManagerMailPreparer
	authorizer *access.Authorizer
	// auditor fails an audit attempt whose mutation does not commit; the store
	// completes a committed one in the mutation's own transaction.
	auditor           access.MutationAuditAdapter
	publisher         realtime.Publisher
	now               func() time.Time
	newID             func() model.ExamID
	institutionOrigin string
	operational       operational.Recorder
}

// AuthoringDependencies are the collaborators of NewAuthoring.
type AuthoringDependencies struct {
	Persistence       store.ExamAuthoringStore
	Memberships       store.AcademicUnitMemberStore
	Users             store.UserStore
	Sittings          store.ExamSittingStore
	Mail              ManagerMailPreparer
	Authorizer        *access.Authorizer
	Auditor           *access.Auditor
	Publisher         realtime.Publisher
	Now               func() time.Time
	NewID             func() model.ExamID
	InstitutionOrigin string
	Operational       operational.Recorder
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
		auditor:             deps.Auditor.MutationAudit(),
		publisher:           deps.Publisher,
		now:                 deps.Now,
		newID:               deps.NewID,
		institutionOrigin:   deps.InstitutionOrigin,
		operational:         deps.Operational,
	}, nil
}
