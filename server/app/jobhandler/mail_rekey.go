// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobhandler

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	mailRekeyPageSize           = 100
	mailFanoutBundleSealPurpose = mail.FanoutBundleSealingPurpose
)

type mailRekeyHandler struct {
	mail   store.MailStore
	sealer *secretseal.Sealer
}

func (h mailRekeyHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	if execution.Attempt == nil ||
		!execution.Job.ID.IsValid() ||
		!execution.Attempt.ID.IsValid() ||
		h.mail == nil ||
		h.sealer == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("mail rekey execution is incomplete"))
	}

	var command mail.RekeyCommand
	if decodeStrictJobDocument(execution.Job.Command, &command) != nil ||
		command.PrimaryKeyID == "" ||
		command.RetiringKeyID == "" ||
		command.RetiringKeyID == command.PrimaryKeyID ||
		!h.sealer.HasKey(command.RetiringKeyID) {
		return job.PermanentFailure("job.command.invalid", errors.New("mail rekey command is invalid for this key ring"))
	}

	if command.PrimaryKeyID != h.sealer.PrimaryKeyID() {
		return job.Relinquished("mail.rekey.primary_mismatch", errors.New("mail rekey command requires a different configured primary key"))
	}

	checkpoint, err := mail.DecodeRekeyCheckpoint(execution.Job.Checkpoint)
	if err != nil {
		return job.PermanentFailure("job.checkpoint.invalid", err)
	}

	initialProof, err := h.mail.ProveRekey(ctx, &store.MailRekeyProofRequest{
		JobID:         execution.Job.ID,
		PrimaryKeyID:  command.PrimaryKeyID,
		RetiringKeyID: command.RetiringKeyID,
	})
	if err != nil {
		return mailRekeyOutcome(err)
	}
	if err = validateMailRekeyProof(initialProof, command); err != nil {
		return job.PermanentFailure("mail.rekey.invariant_failed", err)
	}
	if initialProof.NonPrimaryReferences > math.MaxInt64-checkpoint.Processed {
		return job.PermanentFailure("mail.rekey.invariant_failed", errors.New("mail rekey progress exceeds its bound"))
	}

	progressTotal := checkpoint.Processed + initialProof.NonPrimaryReferences
	for {
		page, listErr := h.mail.ListRekeyTargets(ctx, &store.MailRekeyTargetPageRequest{
			JobID:        execution.Job.ID,
			PrimaryKeyID: command.PrimaryKeyID,
			AfterKind:    checkpoint.AfterKind,
			AfterID:      checkpoint.AfterID,
			Limit:        mailRekeyPageSize,
		})
		if listErr != nil {
			return mailRekeyOutcome(listErr)
		}
		if page == nil || len(page.Targets) > mailRekeyPageSize || (page.More && len(page.Targets) == 0) {
			return job.PermanentFailure("mail.rekey.invariant_failed", errors.New("mail rekey page is invalid"))
		}

		for _, target := range page.Targets {
			if !mailRekeyTargetAfter(target, checkpoint) || target.KeyID == command.PrimaryKeyID || !h.sealer.HasKey(target.KeyID) {
				return job.PermanentFailure("mail.rekey.invariant_failed", errors.New("mail rekey target is invalid"))
			}

			replacement, rekeyErr := h.reencryptTarget(target, execution.Job.ID, command.PrimaryKeyID)
			if rekeyErr != nil {
				return job.PermanentFailure("mail.rekey.payload_unavailable", rekeyErr)
			}

			applied, replaceErr := h.mail.ReplaceRekeyTarget(ctx, replacement)
			if replaceErr != nil {
				return mailRekeyOutcome(replaceErr)
			}

			checkpoint.AfterKind, checkpoint.AfterID = target.Kind, target.ID
			checkpoint.Processed++
			if applied {
				checkpoint.Reencrypted++
			}
			if checkpointErr := checkpointMailRekey(ctx, execution, checkpoint, progressTotal); checkpointErr != nil {
				return job.RetryableFailure("dependency.unavailable", checkpointErr)
			}
		}

		if page.More {
			continue
		}

		break
	}

	proof, err := h.mail.ProveRekey(ctx, &store.MailRekeyProofRequest{
		JobID:         execution.Job.ID,
		PrimaryKeyID:  command.PrimaryKeyID,
		RetiringKeyID: command.RetiringKeyID,
	})
	if err != nil {
		return mailRekeyOutcome(err)
	}
	if err = validateMailRekeyProof(proof, command); err != nil {
		return job.PermanentFailure("mail.rekey.invariant_failed", err)
	}
	if proof.NonPrimaryReferences != 0 || !proof.RetirementSafe {
		return job.RetryableFailure("mail.rekey.incomplete", errors.New("active mail payloads still use a non-primary key"))
	}

	result, err := json.Marshal(mail.RekeyResult{
		PrimaryKeyID:         proof.PrimaryKeyID,
		RetiringKeyID:        proof.RetiringKeyID,
		Processed:            checkpoint.Processed,
		Reencrypted:          checkpoint.Reencrypted,
		NonPrimaryReferences: proof.NonPrimaryReferences,
		RetiringReferences:   proof.RetiringReferences,
		RetirementSafe:       proof.RetirementSafe,
	})
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: result, Err: err}
}

func (h mailRekeyHandler) reencryptTarget(
	target store.MailRekeyTarget,
	jobID model.JobID,
	primaryKeyID string,
) (*store.MailRekeyReplacement, error) {
	purpose, ok := mailRekeyTargetPurpose(target.Kind)
	if !ok || !model.IsValidOpaqueID(target.ID) || len(target.EncryptedPayload) == 0 {
		return nil, errors.New("mail rekey target identity is invalid")
	}

	var envelope secretseal.Envelope
	if json.Unmarshal(target.EncryptedPayload, &envelope) != nil || envelope.KeyID != target.KeyID {
		return nil, errors.New("mail rekey envelope is invalid")
	}

	binding := secretseal.Binding{Purpose: purpose, Owner: target.ID}
	plaintext, err := h.sealer.Open(binding, envelope)
	if err != nil {
		return nil, err
	}

	defer clear(plaintext)
	rotated, err := h.sealer.Seal(binding, plaintext)
	if err != nil || rotated.KeyID != primaryKeyID {
		return nil, errors.New("mail rekey could not use the fenced primary key")
	}

	document, err := json.Marshal(rotated)
	if err != nil {
		return nil, err
	}

	return &store.MailRekeyReplacement{
		JobID:            jobID,
		Kind:             target.Kind,
		ID:               target.ID,
		ExpectedKeyID:    target.KeyID,
		PrimaryKeyID:     primaryKeyID,
		EncryptedPayload: document,
	}, nil
}

func checkpointMailRekey(ctx context.Context, execution job.Execution, checkpoint mail.RekeyCheckpoint, total int64) error {
	document, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if total <= 0 || checkpoint.Processed > total {
		return errors.New("mail rekey progress is invalid")
	}
	return execution.Checkpoint(ctx, job.CheckpointValue{Document: document,
		Progress: &model.JobProgress{Current: checkpoint.Processed, Total: total, Stage: "reencrypting"}})
}

func validateMailRekeyProof(proof *store.MailRekeyProof, command mail.RekeyCommand) error {
	if proof == nil ||
		proof.PrimaryKeyID != command.PrimaryKeyID ||
		proof.RetiringKeyID != command.RetiringKeyID ||
		proof.NonPrimaryReferences < 0 ||
		proof.RetiringReferences < 0 ||
		proof.RetiringReferences > proof.NonPrimaryReferences ||
		proof.RetirementSafe != (proof.RetiringReferences == 0) {
		return errors.New("mail rekey proof is invalid")
	}

	return nil
}

func mailRekeyTargetAfter(target store.MailRekeyTarget, checkpoint mail.RekeyCheckpoint) bool {
	if !mail.ValidRekeyTargetKind(target.Kind) || !model.IsValidOpaqueID(target.ID) {
		return false
	}
	if checkpoint.AfterKind == "" {
		return true
	}
	return target.Kind > checkpoint.AfterKind || (target.Kind == checkpoint.AfterKind && target.ID > checkpoint.AfterID)
}

func mailRekeyTargetPurpose(kind store.MailRekeyTargetKind) (string, bool) {
	switch kind {
	case store.MailRekeyTargetDelivery:
		return mail.DeliverySealingPurpose, true
	case store.MailRekeyTargetFanoutBundle:
		return mailFanoutBundleSealPurpose, true
	default:
		return "", false
	}
}

func mailRekeyOutcome(err error) job.Outcome {
	if store.IsConflict(err) {
		return job.PermanentFailure("mail.rekey.fenced", err)
	}
	return job.RetryableFailure("dependency.unavailable", err)
}

func mailRekeyDescriptor(handler job.Handler) job.Descriptor {
	return job.Descriptor{
		Type:           model.JobTypeMailRekey,
		Checkpoints:    true,
		ProgressStages: []string{"reencrypting"},
		PublicErrorCodes: []string{
			"dependency.unavailable",
			"job.checkpoint.invalid",
			"mail.rekey.fenced",
			"mail.rekey.incomplete",
			"mail.rekey.invariant_failed",
			"mail.rekey.payload_unavailable",
			"mail.rekey.primary_mismatch",
		},
		Timeout:           5 * time.Minute,
		Concurrency:       1,
		MaximumAttempts:   10,
		LeaseDuration:     time.Minute,
		HeartbeatInterval: 15 * time.Second,
		BaseRetryDelay:    time.Minute,
		MaximumRetryDelay: 30 * time.Minute,
		Visibility:        job.VisibilityOperator,
		SuccessRetention:  90 * 24 * time.Hour,
		FailureRetention:  180 * 24 * time.Hour,
		Handler:           handler,
	}
}

// NewMailRekeyDescriptor registers the re-sealing of stored mail payloads
// under a new primary key.
func NewMailRekeyDescriptor(mail store.MailStore, sealer *secretseal.Sealer) job.Descriptor {
	return mailRekeyDescriptor(mailRekeyHandler{mail: mail, sealer: sealer})
}
