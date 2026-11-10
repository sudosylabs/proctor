// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/user"
)

// App is the long-lived application surface. It holds each focused service
// that owns public use cases, and embeds the rest, so it declares no use
// case of its own. Construction receives only the explicit
// Dependencies bundle; infrastructure getters and platform location are not
// part of the public surface.
type App struct {
	// Identity holds sign-in, Session, MFA, token, Desktop authorization,
	// account recovery, account state and bootstrap use cases.
	Identity *identity.Service
	// Onboarding brings people in: Invitations, browser Invitation handoff,
	// public registration, academic administration batches, onboarding
	// imports and Student Progression.
	Onboarding *onboarding.Service
	// Administration holds the institution administrator's access and
	// retention controls: Roles, Role Bindings, the Access Policy and access
	// discovery, retention, and the audit listings.
	Administration *administration.Service
	// Academic holds the Institution, Academic Unit, Programme, Academic
	// Period, Class, membership and Affiliation use cases.
	Academic *academic.Service
	// ExamAuthoring holds the Exam catalog: creating, listing and archiving
	// Exams, editing their Draft, and managing Exam Managers.
	ExamAuthoring *exam.Authoring
	// ExamPublication publishes immutable Exam Revisions and lists them.
	ExamPublication *exam.Publication
	// ExamRecords completes a Sitting's records, waives Submission reviews
	// and manages retention holds.
	ExamRecords *exam.Records
	// ExamExports builds and serves Exam exports.
	ExamExports *exam.Exports
	// Sittings schedules Exam Sittings and moves them through their
	// lifecycle.
	Sittings *sitting.Service
	// Attempts holds the candidate side, from joining a Sitting to sealing a
	// Submission, plus manager inspection of Attempts.
	Attempts *attempt.Service
	// Reviews holds post-Submission Integrity review and result release; it
	// does not grade.
	Reviews *review.Service
	// Resources manages the Exam Resources of an Exam Draft.
	Resources *resource.Service
	// Corrections changes an Open or Paused Sitting's instructions, browser
	// policy or Resources by publishing a new Exam Revision.
	Corrections *correction.Service
	// StarterWorkspaces manages the Starter Workspace of an Exam Draft.
	StarterWorkspaces *workspace.Service
	// Users holds the User directory, profile, settings and profile-picture
	// use cases.
	Users *user.Service
	*searchService
	*administrationCountsService
	*realtimeService
	*realtimeAdmissionService
	jobs *job.Engine
	*jobOperationsService
	*mailService
}

// Jobs returns the root-owned durable Job runtime, or nil for lifecycle-only
// test graphs that intentionally provide no Job store.
func (a *App) Jobs() *job.Engine {
	if a == nil {
		return nil
	}
	return a.jobs
}

// New constructs the application graph from explicit dependencies. Only the
// module-root composition package should call this. The ordered recipe remains
// visible here while private construction modules retain each cohesive slice's
// projection and wiring knowledge.
func New(deps Dependencies) (*App, error) {
	if deps.OperationalRecorder == nil {
		deps.OperationalRecorder = nopOperationalRecorder{}
	}
	if err := validateApplicationDependencies(deps); err != nil {
		return nil, err
	}
	foundation, err := constructApplicationFoundation(deps)
	if err != nil {
		return nil, err
	}
	access, err := constructAccessAndAcademics(deps, foundation)
	if err != nil {
		return nil, err
	}
	profiles, err := constructProfilesAndFiles(deps, foundation, access)
	if err != nil {
		return nil, err
	}
	accounts, err := constructAccounts(deps, foundation, access)
	if err != nil {
		return nil, err
	}
	examinations, err := constructExaminations(deps, foundation, access, accounts)
	if err != nil {
		return nil, err
	}
	accounts.onboarding, accounts.onboardingPorts, err = constructOnboarding(deps, foundation, access, accounts)
	if err != nil {
		return nil, err
	}
	var administration administrationConstruction
	jobs, err := constructJobs(deps, foundation, access, accounts, examinations, profiles)
	if err != nil {
		return nil, err
	}
	administration.search = newSearchService(searchServiceDependencies{
		Exams: examinations.authoring, Users: profiles.users,
		ClassAuthorization: access.authorization, Classes: deps.Store.Class(),
	})
	administration.counts = newAdministrationCountsService(administrationCountsDependencies(accounts, jobs))
	return assembleApplication(deps, foundation, accounts, access, examinations, profiles, jobs, administration), nil
}

// administrationCountsDependencies projects the summary's counters from the
// services that exist; a reduced lifecycle test graph may lack some, and
// their counts are then left out.
func administrationCountsDependencies(accounts accountConstruction, jobs jobConstruction) administrationCountsServiceDependencies {
	var deps administrationCountsServiceDependencies
	if accounts.onboardingPorts.CountPendingInvitations != nil {
		deps.Invitations = pendingInvitationCounter(accounts.onboardingPorts.CountPendingInvitations)
	}
	if accounts.onboardingPorts.CountOpenImports != nil {
		deps.Imports = openImportCounter(accounts.onboardingPorts.CountOpenImports)
	}
	if jobs.operations != nil {
		deps.Jobs = jobs.operations
	}
	if jobs.mail != nil {
		deps.Mail = jobs.mail
	}
	return deps
}
