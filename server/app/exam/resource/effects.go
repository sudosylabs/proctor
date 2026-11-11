// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// publishDraftUpdated tells everyone who may view the Exam that its Draft
// reached draftRevision. An Exam Resource change is announced as a Draft
// update: the event names neither the Resource nor what was done to it.
func (s *Service) publishDraftUpdated(ctx context.Context, examID model.ExamID, draftRevision int64) error {
	event, err := realtime.NewExamDraftUpdatedEvent(examID, draftRevision)
	if err != nil {
		return err
	}

	return s.deps.Publisher.PublishRealtimeEvent(ctx, event)
}
