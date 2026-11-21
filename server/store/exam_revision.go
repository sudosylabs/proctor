// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type ExamRevisionPublication struct {
	// InstitutionOrigin comes from installation configuration, never from the
	// request.
	InstitutionOrigin string
	// ExecutionEnabled comes from installation configuration. When false, a
	// draft with execution enabled conflicts with execution_disabled; an
	// enabled policy must also name a usable profile
	// (environment_profile_unusable).
	ExecutionEnabled      bool
	AssistantEnabled      bool
	AssistantProviders    []string
	RevisionID            model.ExamRevisionID
	ExamID                model.ExamID
	ActorUserID           model.UserID
	ManagerOverride       bool
	ExpectedDraftRevision int64
	Kind                  model.ExamRevisionPublicationKind
	PublishedAt           time.Time
	AuditEventID          model.AuditEventID
	AuditAt               time.Time
}

type ExamRevisionPublicationResult struct {
	Revision      *ExamRevisionSummary
	ExamRevision  int64
	DraftRevision int64
	Replayed      bool
}

// ExamRevisionSummary is the revision view authoring callers may see. It
// excludes instructions, policy bytes, resource metadata, starter workspace
// paths and every content identity.
type ExamRevisionSummary struct {
	ID                      model.ExamRevisionID
	ExamID                  model.ExamID
	Number                  int64
	SourceDraftRevision     int64
	Title                   string
	PolicySchemaVersion     int
	PolicyDigest            string
	BrowserPolicyDigest     string
	Capacity                model.ExamCapacityPolicy
	StarterWorkspaceDigest  string
	ContentDigest           string
	ResourceCount           int
	StarterWorkspaceEntries int
	StarterWorkspaceBytes   int64
	PublishedByUserID       model.UserID
	PublishedAt             time.Time
	BaseRevisionID          model.ExamRevisionID
	Kind                    model.ExamRevisionPublicationKind
}

// ConsistentWith reports whether the summary is the revision asked for, of
// the given exam, with a title.
func (s *ExamRevisionSummary) ConsistentWith(revisionID model.ExamRevisionID, examID model.ExamID) bool {
	return !(s == nil ||
		s.ID != revisionID ||
		s.ExamID != examID ||
		s.Title == "")
}

type ExamRevisionListOptions struct {
	ExamID           model.ExamID
	BeforeNumber     int64
	BeforeRevisionID model.ExamRevisionID
	Limit            int
}

// ExamRevisionStore publishes immutable exam revisions. Publish resolves a
// replay before any stale, archived or no-change decision; otherwise one
// transaction locks the exam and draft, rechecks manager authority, validates
// and freezes the resources and starter workspace, writes the revision, makes
// it the default, rebases the draft and completes the audit. GetSnapshot is
// internal; public callers use GetSummary and List, which carry no authored
// content.
type ExamRevisionStore interface {
	Publish(context.Context, *ExamRevisionPublication, *CommandIdempotency) (*ExamRevisionPublicationResult, error)
	GetSummary(context.Context, model.ExamID, model.ExamRevisionID) (*ExamRevisionSummary, error)
	List(context.Context, ExamRevisionListOptions) ([]ExamRevisionSummary, error)
	GetSnapshot(context.Context, model.ExamID, model.ExamRevisionID) (*model.ExamRevision, error)
}
