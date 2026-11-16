// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package academic owns the academic structure of the installation: the
// Institution, Academic Units, Programmes, Programme Levels, Academic Periods,
// Classes, Academic Unit and Class memberships, and Affiliations. Service holds
// every one of these use cases, one file per entity.
// Academic administration batches, Student Progression and onboarding imports
// live in app/onboarding and call Service methods.
// Start with academic.go for the Service and its collaborators, then open the
// file named for the entity you are changing. effects.go builds the realtime
// events a committed change publishes.
package academic

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/store"
)

// Dependencies are the collaborators New distributes to the Service.
// Publisher delivers the realtime events the use cases build in effects.go
// after a change commits.
type Dependencies struct {
	Store      store.Catalog
	Authorizer *access.Authorizer
	Auditor    *access.Auditor
	Mail       *mail.Composer
	Publisher  realtime.Publisher
	Now        func() time.Time
	NewID      func() string
}

// Service holds every academic use case.
type Service struct {
	institutions        store.InstitutionStore
	academicUnits       store.AcademicUnitStore
	programmes          store.ProgrammeStore
	programmeLevels     store.ProgrammeLevelStore
	academicPeriods     store.AcademicPeriodStore
	classes             store.ClassStore
	classMembers        store.ClassMemberStore
	users               store.UserStore
	affiliations        store.AffiliationStore
	academicUnitMembers store.AcademicUnitMemberStore
	roleBindings        store.RoleBindingStore
	// sittings finds the Sitting boards a Class Membership change refreshes.
	sittings store.ExamSittingStore

	authorizer *access.Authorizer
	auditor    access.MutationAuditAdapter
	mail       *mail.Composer
	publisher  realtime.Publisher
	now        func() time.Time
	newID      func() string
}

// New builds the Service, keeping the Store contracts it calls.
func New(deps Dependencies) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("academic store catalog is required")
	}
	return &Service{
		institutions:        deps.Store.Institution(),
		academicUnits:       deps.Store.AcademicUnit(),
		programmes:          deps.Store.Programme(),
		programmeLevels:     deps.Store.ProgrammeLevel(),
		academicPeriods:     deps.Store.AcademicPeriod(),
		classes:             deps.Store.Class(),
		classMembers:        deps.Store.ClassMember(),
		users:               deps.Store.User(),
		affiliations:        deps.Store.Affiliation(),
		academicUnitMembers: deps.Store.AcademicUnitMember(),
		roleBindings:        deps.Store.RoleBinding(),
		sittings:            deps.Store.ExamSitting(),
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor.MutationAudit(),
		mail:                deps.Mail,
		publisher:           deps.Publisher,
		now:                 deps.Now,
		newID:               deps.NewID,
	}, nil
}
