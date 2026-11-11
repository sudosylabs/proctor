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
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/search"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/user"
)

// App is the long-lived application surface. It names each focused service
// that owns public use cases in its own field and embeds nothing, so it
// declares no use case of its own. Construction receives only the explicit
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
	// Search finds Exams, Classes and Users within the caller's own list
	// visibility for each type.
	Search *search.Service
	// Counts summarises the administration work waiting on the caller:
	// pending Invitations, failed Jobs and mail deliveries, and open
	// onboarding imports.
	Counts *administration.Counts
	// Realtime delivers committed events locally and to peers, unbinds
	// Attempt Connections, propagates Session revocation and authentication
	// or authorization invalidation, and receives the WebSocket sink and
	// cluster fan-out that composition attaches.
	Realtime *RealtimeService
	// RealtimeAdmission revalidates a WebSocket connection's principal and
	// authorizes each of its subscriptions.
	RealtimeAdmission *RealtimeAdmission
	// Jobs lists, inspects, cancels and retries Jobs and lists their attempts
	// for operators; it is nil when the graph has no Job store.
	Jobs *job.Operations
	// Mail holds the operator mail controls: deliveries, metrics, test mail
	// and payload-key rotation; it is nil when the graph has no Job or mail
	// store.
	Mail *mail.Operations

	jobEngine *job.Engine
}

// JobEngine returns the root-owned durable Job runtime, or nil for lifecycle-only
// test graphs that intentionally provide no Job store.
func (a *App) JobEngine() *job.Engine {
	if a == nil {
		return nil
	}
	return a.jobEngine
}
