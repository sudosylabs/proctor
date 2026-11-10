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
// stay in the parent app package for now and call Service methods.
// Start with academic.go for the Service and its collaborators, then open the
// file named for the entity you are changing.
package academic

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Authorizer is the part of *access.Authorizer the academic use cases call.
type Authorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
	AuthorizeWithScope(context.Context, invocation.Invocation, model.Action, model.Resource) (access.AuthorizationScope, error)
	AuthorizeInstitution(context.Context, invocation.Invocation, model.Action) (model.Resource, error)
	InstitutionResource(context.Context) (model.Resource, error)
	AuthorizeAcademicPeriodOwner(context.Context, invocation.Invocation, model.Action, *model.AcademicPeriod) error
	AuthorizeAcademicPeriodList(context.Context, invocation.Invocation) (store.AcademicPeriodVisibilityScope, error)
	AuthorizeUserRead(context.Context, invocation.Invocation, model.UserID) error
	// AuthorizePreflight checks the caller's coarse authority for a
	// membership family before the owning scope of an opaque identifier is
	// known.
	AuthorizePreflight(context.Context, invocation.Invocation, model.Action, model.ResourceType) error
	// CanViewInstitutionUsers reports Institution-wide user.view, the
	// authority that shows a User's email in the User directory.
	CanViewInstitutionUsers(context.Context, invocation.Invocation) (bool, error)
	// CanViewRoleBindings reports role_binding.view on the Resource.
	CanViewRoleBindings(context.Context, invocation.Invocation, model.Resource) (bool, error)
}

// Auditor records the attempt and failure of each audited mutation. The
// production access.MutationAuditAdapter also begins the scoped and subject
// attempts that access.RunAudited looks for.
type Auditor interface {
	access.MutationAuditor
}

// Mail prepares the transactional mail a membership change sends.
type Mail interface {
	PrepareClassTransition(mail.ClassTransitionPreparation) (*store.PreparedMail, error)
	PrepareRelationshipTransition(mail.RelationshipTransitionPreparation) (*store.PreparedMail, error)
}

// Effects publishes realtime effects after an academic change commits.
// Report records a publication that failed; the use case still succeeds.
type Effects interface {
	// Created, Updated and Archived announce an Academic Unit change.
	Created(context.Context, model.AcademicUnitID) error
	Updated(context.Context, model.AcademicUnitID) error
	Archived(context.Context, model.AcademicUnitID) error
	// Changed announces that a User's Affiliations changed.
	Changed(context.Context, model.UserID) error
	// MembershipChanged announces a candidate's Class Membership change.
	MembershipChanged(context.Context, model.UserID, []model.ClassID) error
	// InvalidateAuthorization drops a User's cached authorization.
	InvalidateAuthorization(context.Context, model.UserID)
	Report(context.Context, string, error)
}

// Dependencies are the collaborators New distributes to the Service.
type Dependencies struct {
	Store      store.Catalog
	Authorizer Authorizer
	Auditor    Auditor
	Mail       Mail
	Effects    Effects
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

	authorizer Authorizer
	auditor    Auditor
	mail       Mail
	effects    Effects
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
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor,
		mail:                deps.Mail,
		effects:             deps.Effects,
		now:                 deps.Now,
		newID:               deps.NewID,
	}, nil
}
