// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"encoding/json"
	"errors"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RekeyCommand is the immutable command of the mail rekey Job.
type RekeyCommand struct {
	PrimaryKeyID  string `json:"primary_key_id"`
	RetiringKeyID string `json:"retiring_key_id"`
}

// RekeyCheckpoint is the resumable checkpoint of the mail rekey Job.
type RekeyCheckpoint struct {
	AfterKind   store.MailRekeyTargetKind `json:"after_kind,omitempty"`
	AfterID     string                    `json:"after_id,omitempty"`
	Processed   int64                     `json:"processed"`
	Reencrypted int64                     `json:"reencrypted"`
}

// RekeyResult is the safe result of the mail rekey Job.
type RekeyResult struct {
	PrimaryKeyID         string `json:"primary_key_id"`
	RetiringKeyID        string `json:"retiring_key_id"`
	Processed            int64  `json:"processed"`
	Reencrypted          int64  `json:"reencrypted"`
	NonPrimaryReferences int64  `json:"non_primary_references"`
	RetiringReferences   int64  `json:"retiring_references"`
	RetirementSafe       bool   `json:"retirement_safe"`
}

// DecodeRekeyCommand strictly decodes and validates a mail rekey Job command.
func DecodeRekeyCommand(document json.RawMessage) (RekeyCommand, error) {
	var value RekeyCommand
	err := job.DecodeStrictUniqueDocument(document, &value)
	return value, err
}

// DecodeRekeyResult strictly decodes and validates a mail rekey Job result.
func DecodeRekeyResult(document json.RawMessage) (RekeyResult, error) {
	var value RekeyResult
	err := job.DecodeStrictUniqueDocument(document, &value)
	return value, err
}

// DecodeRekeyCheckpoint strictly decodes and validates a mail rekey Job
// checkpoint. An empty document is the checkpoint of a Job that has not
// started.
func DecodeRekeyCheckpoint(document json.RawMessage) (RekeyCheckpoint, error) {
	var checkpoint RekeyCheckpoint
	if len(document) == 0 {
		return checkpoint, nil
	}

	if job.DecodeStrictUniqueDocument(document, &checkpoint) != nil ||
		checkpoint.Processed < 0 ||
		checkpoint.Reencrypted < 0 ||
		checkpoint.Reencrypted > checkpoint.Processed ||
		checkpoint.AfterID == "" ||
		!model.IsValidOpaqueID(checkpoint.AfterID) ||
		!ValidRekeyTargetKind(checkpoint.AfterKind) {
		return checkpoint, errors.New("mail rekey checkpoint is invalid")
	}

	return checkpoint, nil
}

// ValidRekeyTargetKind reports whether kind names a payload the mail rekey
// Job re-encrypts.
func ValidRekeyTargetKind(kind store.MailRekeyTargetKind) bool {
	return kind == store.MailRekeyTargetDelivery || kind == store.MailRekeyTargetFanoutBundle
}
