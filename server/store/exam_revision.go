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

// ExamPublicationCheck asks which of publication's checks the exam's current
// draft fails, without publishing. The installation fields come from
// configuration exactly as for ExamRevisionPublication.
type ExamPublicationCheck struct {
	InstitutionOrigin  string
	ExecutionEnabled   bool
	AssistantEnabled   bool
	AssistantProviders []string
	ExamID             model.ExamID
	ActorUserID        model.UserID
	ManagerOverride    bool
}

// ExamPublicationProblemReason names one publication check a draft fails.
type ExamPublicationProblemReason string

// The reasons are listed in the order publication checks them; Publish fails
// with the first one found.
const (
	ExamPublicationExamArchived                ExamPublicationProblemReason = "exam_archived"
	ExamPublicationExecutionDisabled           ExamPublicationProblemReason = "execution_disabled"
	ExamPublicationEnvironmentProfileUnusable  ExamPublicationProblemReason = "environment_profile_unusable"
	ExamPublicationAssistantDisabled           ExamPublicationProblemReason = "assistant_disabled"
	ExamPublicationAssistantProfileUnusable    ExamPublicationProblemReason = "assistant_profile_unusable"
	ExamPublicationTooManyResources            ExamPublicationProblemReason = "too_many_resources"
	ExamPublicationResourceTooLarge            ExamPublicationProblemReason = "resource_too_large"
	ExamPublicationTooManyStarterEntries       ExamPublicationProblemReason = "too_many_starter_entries"
	ExamPublicationStarterFileTooLarge         ExamPublicationProblemReason = "starter_file_too_large"
	ExamPublicationStarterWorkspaceTooLarge    ExamPublicationProblemReason = "starter_workspace_too_large"
	ExamPublicationBrowserPolicyOriginMismatch ExamPublicationProblemReason = "browser_policy_origin"
	ExamPublicationNoChanges                   ExamPublicationProblemReason = "no_changes"
)

// ExamPublicationProblemReasons lists every reason in check order.
func ExamPublicationProblemReasons() []ExamPublicationProblemReason {
	return []ExamPublicationProblemReason{
		ExamPublicationExamArchived,
		ExamPublicationExecutionDisabled,
		ExamPublicationEnvironmentProfileUnusable,
		ExamPublicationAssistantDisabled,
		ExamPublicationAssistantProfileUnusable,
		ExamPublicationTooManyResources,
		ExamPublicationResourceTooLarge,
		ExamPublicationTooManyStarterEntries,
		ExamPublicationStarterFileTooLarge,
		ExamPublicationStarterWorkspaceTooLarge,
		ExamPublicationBrowserPolicyOriginMismatch,
		ExamPublicationNoChanges,
	}
}

// ExamPublicationProblem is one failed check. ResourceID names the Resource a
// resource_too_large problem is about and is empty otherwise.
type ExamPublicationProblem struct {
	Reason     ExamPublicationProblemReason
	ResourceID model.ExamResourceID
}

// PublicationError is the Store error Publish returns for a failed check.
func (p ExamPublicationProblem) PublicationError() error {
	switch p.Reason {
	case ExamPublicationExamArchived:
		return NewErrConflict("exam", "exam_archived", nil)
	case ExamPublicationExecutionDisabled:
		return NewErrConflict("exam_revision", "execution_disabled", nil)
	case ExamPublicationEnvironmentProfileUnusable:
		return NewErrConflict("exam_draft", "environment_profile_unusable", nil)
	case ExamPublicationAssistantDisabled:
		return NewErrConflict("exam_revision", "assistant_disabled", nil)
	case ExamPublicationAssistantProfileUnusable:
		return NewErrConflict("exam_revision", "assistant_profile_unusable", nil)
	case ExamPublicationTooManyResources,
		ExamPublicationResourceTooLarge,
		ExamPublicationTooManyStarterEntries,
		ExamPublicationStarterFileTooLarge,
		ExamPublicationStarterWorkspaceTooLarge:
		return NewErrConflict("exam_revision", "exam_revision_capacity", nil)
	case ExamPublicationBrowserPolicyOriginMismatch:
		return NewErrInvalidInput("exam_revision", "browser_policy", nil)
	case ExamPublicationNoChanges:
		return NewErrConflict("exam_revision", "exam_revision_no_changes", nil)
	default:
		return NewErrInvalidInput("exam_revision", "publication_problem", nil)
	}
}

// ExamPublicationCheckResult lists every failed check in check order, for the
// draft at DraftRevision. An empty list means Publish would succeed now.
type ExamPublicationCheckResult struct {
	DraftRevision int64
	Problems      []ExamPublicationProblem
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
	// CheckPublication runs Publish's checks on the current draft without
	// writing, and reports every one it fails rather than the first. It
	// rechecks manager authority like Publish.
	CheckPublication(context.Context, *ExamPublicationCheck) (*ExamPublicationCheckResult, error)
	GetSummary(context.Context, model.ExamID, model.ExamRevisionID) (*ExamRevisionSummary, error)
	List(context.Context, ExamRevisionListOptions) ([]ExamRevisionSummary, error)
	GetSnapshot(context.Context, model.ExamID, model.ExamRevisionID) (*model.ExamRevision, error)
}
