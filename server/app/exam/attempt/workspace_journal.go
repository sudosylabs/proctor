// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type WorkspaceJournalQuery struct {
	Access      CandidateAccess
	AfterCursor int64
	Limit       int
}

// WorkspaceJournalPage is one page of journal entries up to CurrentCursor.
// RefreshRequired tells the client to reload the manifest instead of applying
// entries.
type WorkspaceJournalPage struct {
	WorkspaceID     model.ExamAttemptWorkspaceID
	CurrentCursor   int64
	Entries         []model.AttemptWorkspaceJournalEntry
	HasMore         bool
	RefreshRequired bool
}

// ListCandidateExamWorkspaceJournal returns the Attempt Workspace journal
// entries strictly after AfterCursor, in contiguous cursor order. When
// RefreshRequired is set the page carries no entries and the client must
// reload the manifest.
func (s *Service) ListCandidateExamWorkspaceJournal(
	ctx context.Context,
	invocation invocation.Invocation,
	query WorkspaceJournalQuery,
) (_ WorkspaceJournalPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	candidateAccess, err := candidateSelector(invocation, query.Access)
	if err != nil {
		return WorkspaceJournalPage{}, err
	}
	if query.AfterCursor < 0 || query.Limit < 1 || query.Limit > model.AttemptWorkspaceJournalReadMaximum {
		return WorkspaceJournalPage{}, apperror.Invalid("exam.attempt.invalid", "workspace_journal")
	}

	stored, err := s.workspaces.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{
		Access:      candidateAccess,
		AfterCursor: query.AfterCursor,
		Limit:       query.Limit,
	})
	if err != nil {
		return WorkspaceJournalPage{}, storeError(storeErrors, err)
	}

	if !stored.ConsistentWith(query.AfterCursor) {
		return WorkspaceJournalPage{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Attempt Workspace journal page"))
	}

	entries := make([]model.AttemptWorkspaceJournalEntry, len(stored.Entries))
	for index, entry := range stored.Entries {
		if entry.Validate() != nil ||
			entry.WorkspaceID != stored.WorkspaceID ||
			entry.Cursor <= query.AfterCursor ||
			entry.Cursor > stored.CurrentCursor ||
			(index > 0 && entry.Cursor != entries[index-1].Cursor+1) {
			return WorkspaceJournalPage{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Attempt Workspace journal entry"))
		}

		entries[index] = entry
	}

	return WorkspaceJournalPage{
		WorkspaceID:     stored.WorkspaceID,
		CurrentCursor:   stored.CurrentCursor,
		Entries:         entries,
		HasMore:         stored.HasMore,
		RefreshRequired: stored.RefreshRequired,
	}, nil
}
