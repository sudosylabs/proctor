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

	"github.com/sudosylabs/proctor/server/model"
)

type jobChangedData struct {
	SchemaVersion int              `json:"schema_version"`
	JobID         string           `json:"job_id"`
	Type          model.JobType    `json:"type"`
	Status        model.JobStatus  `json:"status"`
	Progress      *jobProgressData `json:"progress,omitempty"`
}

type jobProgressData struct {
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Stage   string `json:"stage"`
}

// NewJobChangedEvent tells operators who may view Jobs to refetch one Job:
// its status changed, or it checkpointed new progress. progress is nil unless
// the Job's type reports progress. The command, checkpoint, result and errors
// never travel; GET /api/v1/jobs/{job_id} is the authority.
func NewJobChangedEvent(
	institutionID model.InstitutionID,
	jobID model.JobID,
	jobType model.JobType,
	status model.JobStatus,
	progress *model.JobProgress,
) (Event, error) {
	if !institutionID.IsValid() || !jobID.IsValid() || !jobType.IsValid() || !status.IsValid() {
		return Event{}, errors.New("Job change event requires valid Institution, Job, type and status")
	}

	data := jobChangedData{SchemaVersion: 1, JobID: jobID.String(), Type: jobType, Status: status}
	if progress != nil {
		if progress.Current < 0 || progress.Total < 1 || progress.Current > progress.Total || progress.Stage == "" {
			return Event{}, errors.New("Job change event progress is out of bounds")
		}
		data.Progress = &jobProgressData{Current: progress.Current, Total: progress.Total, Stage: progress.Stage}
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("encode Job change event: %w", err)
	}

	return Event{
		Name:     jobChangedMessage.name,
		Action:   model.ActionJobView,
		Resource: model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		Data:     encoded,
	}, nil
}
