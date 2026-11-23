// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type sittingSubmissionsChangedData struct {
	SchemaVersion int    `json:"schema_version"`
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
}

// NewSittingSubmissionsChangedEvent tells a sitting's viewers to refetch its
// submission list. It names no Submission, so one batch publishes it once.
func NewSittingSubmissionsChangedEvent(examID model.ExamID, sittingID model.ExamSittingID) (Event, error) {
	if !examID.IsValid() || !sittingID.IsValid() {
		return Event{}, errors.New("Sitting submissions event requires valid Exam and Sitting identities")
	}
	data, err := json.Marshal(sittingSubmissionsChangedData{SchemaVersion: 1, ExamID: examID.String(), ExamSittingID: sittingID.String()})
	if err != nil {
		return Event{}, fmt.Errorf("encode Sitting submissions event: %w", err)
	}
	return Event{
		Name:     sittingSubmissionsChangedMessage.name,
		Action:   model.ActionSubmissionView,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

type screenClipChangedData struct {
	SchemaVersion int    `json:"schema_version"`
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	ScreenClipID  string `json:"screen_clip_id"`
}

// NewScreenClipChangedEvent tells a sitting's managers that one clip was
// requested or changed state. It carries no reason, range or state.
func NewScreenClipChangedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	clipID model.ScreenClipID,
) (Event, error) {
	if !examID.IsValid() || !sittingID.IsValid() || !attemptID.IsValid() || !clipID.IsValid() {
		return Event{}, errors.New("screen clip event requires valid Exam, Sitting, Attempt and clip identities")
	}
	data, err := json.Marshal(screenClipChangedData{SchemaVersion: 1, ExamID: examID.String(), ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(), ScreenClipID: clipID.String()})
	if err != nil {
		return Event{}, fmt.Errorf("encode screen clip event: %w", err)
	}
	return Event{
		Name:     screenClipChangedMessage.name,
		Action:   model.ActionExamSittingManage,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}

type attemptEvidenceChangedData struct {
	SchemaVersion int    `json:"schema_version"`
	ExamID        string `json:"exam_id"`
	ExamSittingID string `json:"exam_sitting_id"`
	ExamAttemptID string `json:"exam_attempt_id"`
	AsOf          string `json:"as_of"`
}

// NewAttemptEvidenceChangedEvent tells a sitting's managers that an attempt
// gained flags, evidence or native conditions, as of asOf. It goes only to
// subscribers who manage the sitting, because the reads it points to do.
func NewAttemptEvidenceChangedEvent(
	examID model.ExamID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
	asOf time.Time,
) (Event, error) {
	if !examID.IsValid() || !sittingID.IsValid() || !attemptID.IsValid() || asOf.IsZero() {
		return Event{}, errors.New("Attempt evidence event requires valid Exam, Sitting and Attempt identities and a time")
	}
	data, err := json.Marshal(attemptEvidenceChangedData{SchemaVersion: 1, ExamID: examID.String(), ExamSittingID: sittingID.String(),
		ExamAttemptID: attemptID.String(), AsOf: model.FormatRFC3339(asOf)})
	if err != nil {
		return Event{}, fmt.Errorf("encode Attempt evidence event: %w", err)
	}
	return Event{
		Name:     attemptEvidenceChangedMessage.name,
		Action:   model.ActionExamSittingManage,
		Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		Data:     data,
	}, nil
}
