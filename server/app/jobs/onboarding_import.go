// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type onboardingImportCheckpoint struct {
	AfterRow  int `json:"after_row"`
	Processed int `json:"processed"`
}

type onboardingImportJobCommand struct {
	ImportID string `json:"import_id"`
}

// OnboardingImportService is the onboarding import use cases the import Jobs
// run: parse, preview a Student progression, execute, read and fail an
// import.
type OnboardingImportService interface {
	PreviewProgression(context.Context, model.OnboardingImportID) (string, error)
	Parse(context.Context, model.OnboardingImportID) (string, error)
	Get(context.Context, model.OnboardingImportID) (*store.OnboardingImport, error)
	Execute(context.Context, model.OnboardingImportID, int, func(int, int) error) error
	Fail(context.Context, model.OnboardingImportID, string) error
}

type onboardingImportParseHandler struct{ service OnboardingImportService }

type studentProgressionPreviewHandler struct{ service OnboardingImportService }

func (h studentProgressionPreviewHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	command, err := decodeOnboardingImportJob(execution, model.JobTypeStudentProgressionPreview)
	if err != nil || h.service == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("student progression preview command is invalid"))
	}
	terminalCode, err := h.service.PreviewProgression(ctx, model.OnboardingImportID(command.ImportID))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return job.Canceled("job.canceled")
		}
		if terminalCode != "" {
			if failErr := h.service.Fail(ctx, model.OnboardingImportID(command.ImportID), terminalCode); failErr != nil {
				return job.RetryableFailure("dependency.unavailable", failErr)
			}
			return job.PermanentFailure(terminalCode, err)
		}
		return job.RetryableFailure("dependency.unavailable", err)
	}
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: json.RawMessage(`{"preview_ready":true}`)}
}

func (h onboardingImportParseHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	command, err := decodeOnboardingImportJob(execution, model.JobTypeOnboardingImportParse)
	if err != nil || h.service == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("onboarding import parse command is invalid"))
	}
	terminalCode, err := h.service.Parse(ctx, model.OnboardingImportID(command.ImportID))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return job.Canceled("job.canceled")
		}
		if terminalCode != "" {
			return job.PermanentFailure(terminalCode, err)
		}
		return job.RetryableFailure("dependency.unavailable", err)
	}
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: json.RawMessage(`{"preview_ready":true}`)}
}

type onboardingImportExecuteHandler struct{ service OnboardingImportService }

func (h onboardingImportExecuteHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	command, err := decodeOnboardingImportJob(execution, model.JobTypeOnboardingImportExecute)
	if err != nil || h.service == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("onboarding import execution command is invalid"))
	}

	checkpoint := onboardingImportCheckpoint{}
	if len(execution.Job.Checkpoint) != 0 {
		if decodeStrictJobDocument(execution.Job.Checkpoint, &checkpoint) != nil || checkpoint.AfterRow < 0 || checkpoint.Processed < 0 {
			return job.PermanentFailure("job.checkpoint.invalid", errors.New("onboarding import checkpoint is invalid"))
		}
	}

	current, err := h.service.Get(ctx, model.OnboardingImportID(command.ImportID))
	if err != nil {
		return job.RetryableFailure("dependency.unavailable", err)
	}

	total := max(current.ValidRows, 1)
	lastAfter, lastProcessed := checkpoint.AfterRow, checkpoint.Processed
	err = h.service.Execute(ctx, model.OnboardingImportID(command.ImportID), checkpoint.AfterRow, func(after, processed int) error {
		lastAfter, lastProcessed = after, checkpoint.Processed+processed
		document, marshalErr := json.Marshal(onboardingImportCheckpoint{AfterRow: lastAfter, Processed: lastProcessed})
		if marshalErr != nil {
			return marshalErr
		}

		return execution.Checkpoint(ctx, job.CheckpointValue{Document: document,
			Progress: &model.JobProgress{Current: int64(lastProcessed), Total: int64(total), Stage: "executing"}})
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return job.Canceled("job.canceled")
		}
		return job.RetryableFailure("dependency.unavailable", err)
	}

	result, _ := json.Marshal(onboardingImportCheckpoint{AfterRow: lastAfter, Processed: lastProcessed})
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: result}
}

func decodeOnboardingImportJob(execution job.Execution, expected model.JobType) (onboardingImportJobCommand, error) {
	var command onboardingImportJobCommand
	if execution.Job.Type != expected ||
		decodeStrictJobDocument(execution.Job.Command, &command) != nil ||
		!model.OnboardingImportID(command.ImportID).IsValid() {
		return command, errors.New("invalid onboarding import job")
	}

	return command, nil
}

func onboardingImportParseDescriptor(handler job.Handler) job.Descriptor {
	return onboardingImportDescriptor(model.JobTypeOnboardingImportParse, handler, true)
}

func studentProgressionPreviewDescriptor(handler job.Handler) job.Descriptor {
	descriptor := onboardingImportDescriptor(model.JobTypeStudentProgressionPreview, handler, true)
	descriptor.PublicErrorCodes = append(descriptor.PublicErrorCodes, model.StudentProgressionFailureCodes()...)
	return descriptor
}

func onboardingImportExecuteDescriptor(handler job.Handler) job.Descriptor {
	descriptor := onboardingImportDescriptor(model.JobTypeOnboardingImportExecute, handler, true)
	descriptor.Checkpoints = true
	descriptor.ProgressStages = []string{"executing"}
	descriptor.Timeout = 30 * time.Minute
	return descriptor
}

// NewOnboardingImportParse registers the parsing and validation of an
// uploaded onboarding import through its application use case.
func NewOnboardingImportParse(service OnboardingImportService) job.Registration {
	return job.Registration{Descriptor: onboardingImportParseDescriptor(onboardingImportParseHandler{service: service})}
}

// NewStudentProgressionPreview registers the preview of a Student progression
// through the onboarding import use case.
func NewStudentProgressionPreview(service OnboardingImportService) job.Registration {
	return job.Registration{Descriptor: studentProgressionPreviewDescriptor(studentProgressionPreviewHandler{service: service})}
}

// NewOnboardingImportExecute registers execution of a confirmed onboarding
// import through its application use case.
func NewOnboardingImportExecute(service OnboardingImportService) job.Registration {
	return job.Registration{Descriptor: onboardingImportExecuteDescriptor(onboardingImportExecuteHandler{service: service})}
}

func onboardingImportDescriptor(kind model.JobType, handler job.Handler, cancelable bool) job.Descriptor {
	descriptor := job.NewDescriptor(kind, handler)
	descriptor.PublicErrorCodes = []string{
		"dependency.unavailable",
		"job.canceled",
		"job.checkpoint.invalid",
		"job.command.invalid",
		"onboarding_import.authorization_lost",
		"onboarding_import.file_invalid",
	}
	descriptor.Concurrency = 2
	descriptor.BaseRetryDelay = 10 * time.Second
	descriptor.MaximumRetryDelay = 5 * time.Minute
	descriptor.Cancelable = cancelable
	descriptor.Visibility = job.VisibilityDomain
	descriptor.SuccessRetention = 7 * 24 * time.Hour
	descriptor.FailureRetention = 7 * 24 * time.Hour
	return descriptor
}
