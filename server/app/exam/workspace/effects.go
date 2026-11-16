// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// ChangeOperation is the operation a starter workspace change event reports.
type ChangeOperation string

// Change operations; their string values are sent to clients.
const (
	ChangeDirectoryCreated ChangeOperation = "directory_created"
	ChangeFileCreated      ChangeOperation = "file_created"
	ChangeEntryMoved       ChangeOperation = "entry_moved"
	ChangeFileReplaced     ChangeOperation = "file_replaced"
	ChangeEntryRemoved     ChangeOperation = "entry_removed"
)

// publishChanged deliberately sends no path, content or checksum, only which
// entry changed and how.
func (s *Service) publishChanged(
	ctx context.Context,
	examID model.ExamID,
	entryID model.StarterWorkspaceEntryID,
	draftRevision int64,
	operation ChangeOperation,
	changedAt time.Time,
) error {
	event, err := realtime.NewExamStarterWorkspaceChangedEvent(examID, entryID, draftRevision, string(operation), changedAt)
	if err != nil {
		return err
	}

	return s.publisher.Publish(ctx, event)
}
