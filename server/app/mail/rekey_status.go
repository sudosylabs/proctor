// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// RekeyProgressView is how far a running rekey Job has read.
type RekeyProgressView struct {
	Current int64
	Total   int64
	Stage   string
}

// RekeyProofView is a finished rekey Job's proof that no active payload
// references the retiring key.
type RekeyProofView struct {
	NonPrimaryReferences int64
	RetiringReferences   int64
	RetirementSafe       bool
}

// RekeyStatusView is the safe, typed operator projection for one rekey
// Job. It deliberately excludes the raw command, checkpoint, result, payload
// identities, ciphertext, recipients, and key material.
type RekeyStatusView struct {
	JobID           model.JobID
	Status          model.JobStatus
	PrimaryKeyID    string
	RetiringKeyID   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     model.OptionalTime
	PublicErrorCode string
	AttemptCount    int
	MaximumAttempts int
	Processed       int64
	Reencrypted     int64
	Progress        *RekeyProgressView
	Proof           *RekeyProofView
}

// GetMailRekeyStatus reports the progress of one mail rekey Job.
func (s *Operations) GetMailRekeyStatus(
	ctx context.Context,
	invocation invocation.Invocation,
	jobID model.JobID,
) (RekeyStatusView, error) {
	if s == nil {
		return RekeyStatusView{}, apperror.New("mail.unavailable")
	}
	if err := access.RequireStrongRecentSession(invocation.Principal(), s.deps.Now(), s.deps.RecentAuthenticationTTL); err != nil {
		return RekeyStatusView{}, err
	}
	if _, err := s.deps.Authorization.AuthorizeInstitution(ctx, invocation, model.ActionMailRekey); err != nil {
		return RekeyStatusView{}, err
	}
	if s.rekeyJobs == nil || !jobID.IsValid() {
		return RekeyStatusView{}, apperror.New("mail.unavailable")
	}

	job, err := s.rekeyJobs.Get(ctx, jobID)
	if err != nil {
		if store.IsNotFound(err) {
			return RekeyStatusView{}, apperror.New("resource.not_found")
		}
		return RekeyStatusView{}, apperror.New("mail.unavailable").Wrap(err)
	}

	// Job identities are shared across domains. A caller with mail.rekey must
	// not learn whether a valid identifier belongs to some unrelated Job type.
	if job == nil {
		return RekeyStatusView{}, apperror.New("mail.unavailable").Wrap(errors.New("mail rekey persistence returned no Job"))
	}
	if job.Type != model.JobTypeMailRekey {
		return RekeyStatusView{}, apperror.New("resource.not_found")
	}

	view, err := projectRekeyStatus(job)
	if err != nil {
		return RekeyStatusView{}, apperror.New("mail.unavailable").Wrap(err)
	}

	return view, nil
}

func projectRekeyStatus(job *model.Job) (RekeyStatusView, error) {
	if job == nil || job.Validate() != nil || job.Type != model.JobTypeMailRekey {
		return RekeyStatusView{}, errors.New("mail rekey Job is invalid")
	}

	command, commandErr := DecodeRekeyCommand(job.Command)
	if commandErr != nil ||
		!secretseal.ValidKeyID(command.PrimaryKeyID) ||
		!secretseal.ValidKeyID(command.RetiringKeyID) ||
		command.PrimaryKeyID == command.RetiringKeyID {
		return RekeyStatusView{}, errors.New("mail rekey command is invalid")
	}

	view := RekeyStatusView{
		JobID:           job.ID,
		Status:          job.Status,
		PrimaryKeyID:    command.PrimaryKeyID,
		RetiringKeyID:   command.RetiringKeyID,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		CompletedAt:     job.CompletedAt,
		PublicErrorCode: job.PublicErrorCode,
		AttemptCount:    job.AttemptCount,
		MaximumAttempts: job.MaximumAttempts,
	}
	if len(job.Checkpoint) > 0 {
		checkpoint, err := DecodeRekeyCheckpoint(job.Checkpoint)
		if err != nil ||
			job.Progress == nil ||
			job.Progress.Current != checkpoint.Processed ||
			job.Progress.Total < checkpoint.Processed ||
			job.Progress.Stage != "reencrypting" {
			return RekeyStatusView{}, errors.New("mail rekey checkpoint projection is invalid")
		}

		view.Processed, view.Reencrypted = checkpoint.Processed, checkpoint.Reencrypted
		view.Progress = &RekeyProgressView{Current: job.Progress.Current, Total: job.Progress.Total, Stage: job.Progress.Stage}
	} else if job.Progress != nil {
		return RekeyStatusView{}, errors.New("mail rekey progress has no checkpoint")
	}

	if len(job.Result) > 0 {
		if job.Status != model.JobStatusSucceeded {
			return RekeyStatusView{}, errors.New("mail rekey result state is invalid")
		}

		result, resultErr := DecodeRekeyResult(job.Result)
		if resultErr != nil ||
			result.PrimaryKeyID != command.PrimaryKeyID ||
			result.RetiringKeyID != command.RetiringKeyID ||
			result.Processed < view.Processed ||
			result.Reencrypted < view.Reencrypted ||
			result.Reencrypted > result.Processed ||
			result.NonPrimaryReferences < 0 ||
			result.RetiringReferences < 0 ||
			result.RetiringReferences > result.NonPrimaryReferences ||
			!result.RetirementSafe ||
			result.NonPrimaryReferences != 0 ||
			result.RetiringReferences != 0 {
			return RekeyStatusView{}, errors.New("mail rekey retirement proof is invalid")
		}

		view.Processed, view.Reencrypted = result.Processed, result.Reencrypted
		view.Proof = &RekeyProofView{
			NonPrimaryReferences: result.NonPrimaryReferences,
			RetiringReferences:   result.RetiringReferences,
			RetirementSafe:       result.RetirementSafe,
		}
	} else if job.Status == model.JobStatusSucceeded {
		return RekeyStatusView{}, errors.New("successful mail rekey has no proof")
	}

	return view, nil
}
