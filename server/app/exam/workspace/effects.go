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

// publishChanged tells everyone who may view the Exam which Starter Workspace
// entry changed, how, at which Draft revision and when. The event carries no
// path, content or checksum.
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

	return s.deps.Publisher.Publish(ctx, event)
}
