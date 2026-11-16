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
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Content stages and opens object bytes. Objects are opaque to the file
// store: paths and entries live only in the database.
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

// Service manages exam draft starter workspaces; build it with New.
type Service struct {
	workspaces          store.ExamStarterWorkspaceStore
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer *access.Authorizer
	// auditor only begins and fails audits; the store completes them in the
	// same transaction as the change.
	auditor     access.MutationAuditAdapter
	content     Content
	publisher   realtime.Publisher
	now         func() time.Time
	newEntryID  func() model.StarterWorkspaceEntryID
	newObjectID func() model.StarterWorkspaceObjectID
	newVersion  func() model.WorkspaceContentVersion
}

// Dependencies are the Service's collaborators; New requires every field.
type Dependencies struct {
	Persistence store.ExamStarterWorkspaceStore
	Access      store.ExamAuthoringStore
	Memberships store.AcademicUnitMemberStore
	Authorizer  *access.Authorizer
	Auditor     *access.Auditor
	Content     Content
	Publisher   realtime.Publisher
	Now         func() time.Time
	NewEntryID  func() model.StarterWorkspaceEntryID
	NewObjectID func() model.StarterWorkspaceObjectID
	NewVersion  func() model.WorkspaceContentVersion
}

// New returns an error when any dependency is nil.
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
		auditor:             deps.Auditor.MutationAudit(),
		content:             deps.Content,
		publisher:           deps.Publisher,
		now:                 deps.Now,
		newEntryID:          deps.NewEntryID,
		newObjectID:         deps.NewObjectID,
		newVersion:          deps.NewVersion,
	}, nil
}
