// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// examCountsWindow is how often one exam's catalogue counts may be refetched.
const examCountsWindow = 5 * time.Second

type examCountsChangedData struct {
	SchemaVersion int    `json:"schema_version"`
	ExamID        string `json:"exam_id"`
}

// NewExamCountsChangedEvent tells an exam's viewers to refetch its catalogue
// counts. It carries no count.
func NewExamCountsChangedEvent(examID model.ExamID) (Event, error) {
	if !examID.IsValid() {
		return Event{}, errors.New("Exam counts event requires a valid Exam identity")
	}
	data, err := json.Marshal(examCountsChangedData{SchemaVersion: 1, ExamID: examID.String()})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam counts event: %w", err)
	}
	return Event{
		Name:     examCountsChangedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// noteExamCounts follows a delivered board or submissions event with the
// exam's counts event, at most once per exam per window. It is best effort.
func (s *Service) noteExamCounts(ctx context.Context, examID model.ExamID) {
	if examID.IsZero() || s.examCounts == nil {
		return
	}
	counts, err := NewExamCountsChangedEvent(examID)
	if err == nil {
		err = s.examCounts.Publish(ctx, examID.String(), examCountsChangedMessage.name, counts)
	}
	if err != nil {
		s.Report(ctx, examCountsChangedMessage.name, err)
	}
}
