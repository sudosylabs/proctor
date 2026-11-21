// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListCandidateStarterWorkspace is only for comparison with already-admitted
// starter code; it never reads a later draft or another attempt's bytes.
func (s *SQLExamAttemptStore) ListCandidateStarterWorkspace(ctx context.Context,
	access store.CandidateAttemptAccess, limit int) ([]store.CandidateStarterWorkspaceFile, error) {
	if limit < 1 || limit > 128 {
		return nil, store.NewErrInvalidInput("exam_attempt", "starter_limit", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "read admitted starter workspace", func(ctx context.Context,
		tx *sqlxTxWrapper) ([]store.CandidateStarterWorkspaceFile, error) {
		if _, err := lockCandidateGuard(ctx, tx, access); err != nil {
			return nil, err
		}
		var ids []string
		if err := tx.Select(ctx, &ids, `SELECT e.object_id FROM exam_revision_starter_workspace_entries e
 JOIN exam_attempts a ON a.admission_revision_id=e.exam_revision_id
 WHERE a.id=? AND e.kind='file' ORDER BY e.entry_id LIMIT ?`, access.AttemptID.String(), limit); err != nil {
			return nil, err
		}
		result := make([]store.CandidateStarterWorkspaceFile, 0, len(ids))
		for _, raw := range ids {
			id, err := model.ParseStarterWorkspaceObjectID(raw)
			if err != nil {
				return nil, invalidPersistedState("exam_revision_starter_workspace", "object_id", err)
			}
			result = append(result, store.CandidateStarterWorkspaceFile{ObjectID: id})
		}
		return result, nil
	})
}
