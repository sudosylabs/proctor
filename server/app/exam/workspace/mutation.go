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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Result is the entry a committed Starter Workspace command left, the object
// behind it when the entry is a file, and the Draft revision the change
// produced. Replayed reports a retained idempotent outcome.
type Result struct {
	Entry         model.StarterWorkspaceEntry
	Object        *model.StarterWorkspaceObject
	DraftRevision int64
	Replayed      bool
}

type mutationRunner func(
	context.Context,
	*store.ExamStarterWorkspaceMutation,
	*store.CommandIdempotency,
) (*store.ExamStarterWorkspaceMutationResult, error)

func (s *Service) runMutation(
	ctx context.Context,
	inv invocation.Invocation,
	authorization authorizationDecision,
	auditOperation string,
	effectOperation ChangeOperation,
	mutation *store.ExamStarterWorkspaceMutation,
	commandIdempotency *store.CommandIdempotency,
	run mutationRunner,
) (Result, error) {
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     authorization.action,
		Resource:   model.Resource{Type: model.ResourceExam, ID: mutation.ExamID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.unitID.String(),
		Operation:  auditOperation,
		Value: map[string]any{
			"exam_id":                 mutation.ExamID.String(),
			"entry_id":                mutation.EntryID.String(),
			"expected_draft_revision": mutation.ExpectedDraftRevision,
		},
	})
	if err != nil {
		return Result{}, err
	}

	mutation.AuditEventID = auditID
	mutation.AuditAt = mutation.ChangedAt
	result, err := run(ctx, mutation, commandIdempotency)
	if err != nil {
		mapped := storeErrors.Translate(err)
		if failErr := s.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.starter_workspace.unavailable")); failErr != nil {
			return Result{}, errors.Join(mapped, failErr)
		}

		return Result{}, mapped
	}

	if result == nil || result.Entry == nil || result.DraftRevision < 1 {
		return Result{}, apperror.New("exam.starter_workspace.unavailable").Wrap(
			errors.New("Starter Workspace Store returned incomplete mutation result"),
		)
	}

	if !result.Replayed {
		if effectErr := s.publishChanged(
			ctx,
			mutation.ExamID,
			result.Entry.ID,
			result.DraftRevision,
			effectOperation,
			mutation.ChangedAt,
		); effectErr != nil {
			s.publisher.Report(ctx, "exam_starter_workspace_"+string(effectOperation), effectErr)
		}
	}

	return Result{Entry: *result.Entry, Object: result.Object, DraftRevision: result.DraftRevision, Replayed: result.Replayed}, nil
}

func validateMutation(examID model.ExamID, entryID model.StarterWorkspaceEntryID, revision int64, path string) (string, error) {
	if err := validateMutationBase(examID, entryID, revision); err != nil {
		return "", err
	}
	normalized, err := NormalizePath(path)
	if err != nil || normalized != path {
		return "", apperror.Invalid("exam.starter_workspace.invalid", "path").Wrap(err)
	}
	return normalized, nil
}

func validateMutationBase(examID model.ExamID, entryID model.StarterWorkspaceEntryID, revision int64) error {
	if !examID.IsValid() || revision < 1 || !entryID.IsZero() && !entryID.IsValid() {
		return apperror.Invalid("exam.starter_workspace.invalid", "command")
	}
	return nil
}
