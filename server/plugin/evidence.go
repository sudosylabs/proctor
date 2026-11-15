// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// MaxEvidenceDetailBytes bounds one evidence record's Detail.
const MaxEvidenceDetailBytes = 64 << 10

// The errors Evidence answers with, besides ErrUnavailable once the plugin
// has failed or been turned off.
var (
	// ErrEvidenceInvalid reports a kind, sequence, Detail or binding outside
	// the grammar and bounds Evidence documents.
	ErrEvidenceInvalid = errors.New("plugin evidence record is invalid")
	// ErrBindingNotCurrent reports that the binding has ended, does not match
	// its Attempt's live binding, or its Attempt no longer accepts work: its
	// generation ended or its lease expired, the Attempt is not Active, the
	// Sitting is not Open or Paused before its deadline, or the Submission is
	// sealed. Nothing was stored.
	ErrBindingNotCurrent = errors.New("plugin evidence binding is not current")
	// ErrEvidenceSequenceConflict reports that the binding already holds a
	// different record of this kind under this sequence.
	ErrEvidenceSequenceConflict = errors.New("plugin evidence sequence holds another record")
	// ErrEvidenceBudgetExhausted reports that the Attempt holds as much
	// Attempt evidence as the core keeps for one Attempt. The record was
	// not stored; the core counted the refusal, which reviewers see.
	ErrEvidenceBudgetExhausted = errors.New("plugin evidence budget for the Attempt is exhausted")
)

// EvidenceRecord is one record a plugin appends for an Attempt.
//
// Kind is the record's kind within the plugin; the core stores it as
// "<plugin-id>.<kind>", which must be one of the core's Attempt evidence
// kinds, such as "process" for the execution plugin's command log or "gap"
// for a record that says coverage is missing. Sequence, from 1, numbers the
// stream of that kind by the binding's environment incarnation: appending
// the same record again under the same sequence is recognized and stored
// once.
//
// Detail is one JSON object of at most MaxEvidenceDetailBytes, which the
// core stores and shows to reviewers but does not read.
type EvidenceRecord struct {
	Kind       string
	Sequence   int64
	ObservedAt time.Time
	Detail     json.RawMessage
}

// EvidenceReceipt is what the core stored. Repeated is true when the record
// was already stored under its sequence; ID and RecordedAt are then the
// stored record's.
type EvidenceReceipt struct {
	ID         string
	RecordedAt time.Time
	Repeated   bool
}

// Evidence appends Attempt evidence for an Attempt through an execution
// environment binding, made with AttemptWorkspaces.BindEnvironment. Host.Evidence
// returns it.
//
// The core stores a record only while the binding the BoundEnvironment names
// is current (see ErrBindingNotCurrent), attributes it to the binding's Participation, stamps
// it with its own time and fixes it into the Attempt's Submission when that
// is sealed. A record never raises a Flag, suspends or warns: it is evidence
// reviewers read beside the Flags, and a gap is a record of kind "gap". The
// core runs no plugin code while it stores the record, and Append returns
// only after the record has committed, so the plugin may then acknowledge
// its source. Records are kept within per-Attempt bounds; past them Append
// fails with ErrEvidenceBudgetExhausted.
type Evidence interface {
	Append(ctx context.Context, environment BoundEnvironment, record EvidenceRecord) (EvidenceReceipt, error)
}
