// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Authorizer is the part of *access.Authorizer the Starter Workspace use
// cases call.
type Authorizer interface {
	Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error
}

// Auditor records the attempt and failure of each audited Starter Workspace
// command; the Store completes the audit event with the change it commits.
type Auditor interface {
	Begin(context.Context, access.Mutation) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

// Content stages and opens the bytes of a Starter Workspace object. The
// object is opaque to the file store: paths and entries live only in the
// Store.
type Content interface {
	StageStarterWorkspaceObject(
		context.Context,
		model.StarterWorkspaceObjectID,
		io.Reader,
		int64,
		string,
	) (*model.StarterWorkspaceContent, error)
	OpenStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID) (io.ReadCloser, error)
}

// Service manages the Starter Workspace of an Exam Draft.
type Service struct {
	workspaces store.ExamStarterWorkspaceStore
	// exams reads the Exam ownership a command is authorized against.
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer  Authorizer
	auditor     Auditor
	content     Content
	publisher   realtime.Publisher
	now         func() time.Time
	newEntryID  func() model.StarterWorkspaceEntryID
	newObjectID func() model.StarterWorkspaceObjectID
	newVersion  func() model.WorkspaceContentVersion
}

// Dependencies are the collaborators New validates once and distributes to
// the Service.
type Dependencies struct {
	Persistence store.ExamStarterWorkspaceStore
	Access      store.ExamAuthoringStore
	Memberships store.AcademicUnitMemberStore
	Authorizer  Authorizer
	Auditor     Auditor
	Content     Content
	Publisher   realtime.Publisher
	Now         func() time.Time
	NewEntryID  func() model.StarterWorkspaceEntryID
	NewObjectID func() model.StarterWorkspaceObjectID
	NewVersion  func() model.WorkspaceContentVersion
}

// New returns a Starter Workspace Service, or an error when a required
// dependency is missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Persistence == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.Content == nil ||
		deps.Publisher == nil ||
		deps.Now == nil ||
		deps.NewEntryID == nil ||
		deps.NewObjectID == nil ||
		deps.NewVersion == nil {
		return nil, errors.New("Starter Workspace dependencies are required")
	}

	return &Service{
		workspaces:          deps.Persistence,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor,
		content:             deps.Content,
		publisher:           deps.Publisher,
		now:                 deps.Now,
		newEntryID:          deps.NewEntryID,
		newObjectID:         deps.NewObjectID,
		newVersion:          deps.NewVersion,
	}, nil
}
