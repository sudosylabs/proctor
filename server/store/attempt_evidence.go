// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"encoding/json"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// ExamAttemptEvidenceAppend appends one Attempt evidence record through the
// environment access Access names; the record's author is that access's
// author. EvidenceID is the proposed identity of a new record. Sequence
// numbers the author's stream of Kind.
type ExamAttemptEvidenceAppend struct {
	Access     ExamAttemptWorkspaceEnvironmentAccess
	EvidenceID model.IntegrityEvidenceID
	Kind       model.AttemptEvidenceKind
	Sequence   int64
	Detail     json.RawMessage
	ObservedAt time.Time
}

// ExamAttemptEvidenceAppendResult is the stored record, or, when
// Refused, nothing stored: the append was over the Attempt's bounds and only
// counted. Repeated reports that the same claim was already stored, and
// Evidence is that record.
type ExamAttemptEvidenceAppendResult struct {
	Evidence model.AttemptEvidence
	Repeated bool
	Refused  bool
}

// Consistent reports whether the result answers input: a refusal carries no
// record, and a stored or repeated record is valid, names the Attempt, the
// access's author, the kind and sequence, and repeats the claim.
func (r *ExamAttemptEvidenceAppendResult) Consistent(input *ExamAttemptEvidenceAppend) bool {
	if r == nil || input == nil {
		return false
	}
	if r.Refused {
		return !r.Repeated && r.Evidence.ID.IsZero()
	}
	claim := model.AttemptEvidence{Kind: input.Kind, Author: input.Access.WorkspaceAuthor(), Sequence: input.Sequence,
		Detail: input.Detail, ObservedAt: r.Evidence.ObservedAt}
	return r.Evidence.Validate() == nil &&
		r.Evidence.AttemptID == input.Access.AttemptID &&
		(r.Repeated || r.Evidence.ID == input.EvidenceID) &&
		!r.Evidence.ObservedAt.After(input.ObservedAt.UTC().Truncate(time.Millisecond)) &&
		r.Evidence.SameClaim(claim)
}

// ExamAttemptTerminalInputAppend appends one execution.input record from
// the node that serves a terminal, which holds no runtime lease. The
// record's author is the environment incarnation and the terminal's tag;
// Sequence numbers that terminal's input stream. EvidenceID is the proposed
// identity of a new record.
type ExamAttemptTerminalInputAppend struct {
	AttemptID   model.ExamAttemptID
	Incarnation int64
	Tag         model.AttemptEnvironmentProcessTag
	EvidenceID  model.IntegrityEvidenceID
	Sequence    int64
	Detail      json.RawMessage
	ObservedAt  time.Time
}

// Author is the record's author: the incarnation and the terminal.
func (a *ExamAttemptTerminalInputAppend) Author() model.AttemptWorkspaceAuthor {
	return model.NewEnvironmentWorkspaceAuthor(a.Incarnation, a.Tag)
}

// Valid reports whether every field is well formed.
func (a *ExamAttemptTerminalInputAppend) Valid() bool {
	return a != nil && a.AttemptID.IsValid() && a.Incarnation >= 0 && a.Tag.IsValid() &&
		a.EvidenceID.IsValid() && model.ValidAttemptEvidenceClaim(model.AttemptEvidenceInput, a.Author(), a.Sequence, a.Detail) &&
		!a.ObservedAt.IsZero()
}

// AttemptEvidenceListOptions selects one page of a sealed Submission's
// Attempt evidence for review.
type AttemptEvidenceListOptions struct {
	SubmissionID model.SubmissionID
	AfterID      model.IntegrityEvidenceID
	Limit        int
}

// AttemptEvidencePage is one page of Attempt evidence in ID order.
type AttemptEvidencePage struct {
	Items   []model.AttemptEvidence
	HasMore bool
}

// AttemptEvidenceReviewMaximum bounds one review page.
const AttemptEvidenceReviewMaximum = 100
