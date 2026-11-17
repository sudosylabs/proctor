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
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/correction"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/search"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/user"
)

// App is the application's public surface: one field per service, nothing
// embedded, so every use case is a method on the service that owns it.
type App struct {
	Identity        *identity.Service
	Onboarding      *onboarding.Service
	Administration  *administration.Service
	Academic        *academic.Service
	ExamAuthoring   *exam.Authoring
	ExamPublication *exam.Publication
	ExamRecords     *exam.Records
	ExamExports     *exam.Exports
	Sittings        *sitting.Service
	Attempts        *attempt.Service
	// Terminals is nil when the installation runs no execution environments.
	Terminals *environment.Terminals
	// Assistant is nil when the installation has the assistant off.
	Assistant *assistant.Service
	// Reviews covers integrity review and result release; it does not grade.
	Reviews           *review.Service
	Resources         *resource.Service
	Corrections       *correction.Service
	StarterWorkspaces *workspace.Service
	Users             *user.Service
	Search            *search.Service
	Counts            *administration.Counts
	Realtime          *realtime.Service
	RealtimeAdmission *RealtimeAdmission
	// Jobs is nil when the graph has no job store.
	Jobs *job.Operations
	// Mail is nil when the graph has no job or mail store.
	Mail *mail.Operations

	jobEngine *job.Engine
}

// JobEngine returns the durable job runtime, or nil for test graphs built
// without a job store.
func (a *App) JobEngine() *job.Engine {
	if a == nil {
		return nil
	}
	return a.jobEngine
}
