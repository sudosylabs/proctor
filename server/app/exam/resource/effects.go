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
	"github.com/sudosylabs/proctor/server/store"
)

// publishDraftUpdated tells everyone who may view the Exam that its Draft
// reached draftRevision. An Exam Resource change is announced as a Draft
// update: the event names neither the Resource nor what was done to it.
func (s *Service) publishDraftUpdated(ctx context.Context, examID model.ExamID, draftRevision int64) error {
	event, err := realtime.NewExamDraftUpdatedEvent(examID, draftRevision)
	if err != nil {
		return err
	}

	return s.publisher.Publish(ctx, event)
}

// publishChanged announces a committed Exam Resource command unless the
// Store replayed an earlier outcome. A failed publication is reported and the
// command still succeeds.
func (s *Service) publishChanged(ctx context.Context, result *store.ExamResourceCommandResult) {
	if result.Replayed || result.Value == nil {
		return
	}

	if err := s.publishDraftUpdated(ctx, result.Value.Resource.ExamID, result.Value.DraftRevision); err != nil {
		s.publisher.Report(ctx, "exam_resource_changed", err)
	}
}
