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

// ExamAttemptEnvironmentEvidenceAppend appends one capability evidence record
// for the Attempt through the environment binding Access names. EvidenceID is
// the proposed identity of a new record. Kind is the plugin's own
// "<plugin-id>.<kind>"; Sequence numbers its stream for that kind within the
// binding.
type ExamAttemptEnvironmentEvidenceAppend struct {
	Access     ExamAttemptWorkspaceEnvironmentAccess
	EvidenceID model.IntegrityEvidenceID
	Kind       model.IntegrityPolicyKind
	Sequence   int64
	Gap        bool
	Detail     json.RawMessage
	ObservedAt time.Time
}

// ExamAttemptEnvironmentEvidenceAppendResult is the stored record, or, when
// Refused, nothing stored: the append was over the Attempt's bounds and only
// counted. Repeated reports that the same claim was already stored, and
// Evidence is that record.
type ExamAttemptEnvironmentEvidenceAppendResult struct {
	Evidence model.CapabilityEvidence
	Repeated bool
	Refused  bool
}

// Consistent reports whether the result answers input: a refusal carries no
// record, and a stored or repeated record is valid, names the Attempt,
// binding, generation, kind and sequence, and repeats the claim.
func (r *ExamAttemptEnvironmentEvidenceAppendResult) Consistent(input *ExamAttemptEnvironmentEvidenceAppend) bool {
	if r == nil || input == nil {
		return false
	}
	if r.Refused {
		return !r.Repeated && r.Evidence.ID.IsZero()
	}
	claim := model.CapabilityEvidence{BindingID: input.Access.BindingID, Kind: input.Kind, Sequence: input.Sequence,
		Gap: input.Gap, Detail: input.Detail, ObservedAt: r.Evidence.ObservedAt}
	return r.Evidence.Validate() == nil &&
		r.Evidence.AttemptID == input.Access.AttemptID &&
		r.Evidence.Generation == input.Access.Generation &&
		(r.Repeated || r.Evidence.ID == input.EvidenceID) &&
		!r.Evidence.ObservedAt.After(input.ObservedAt.UTC().Truncate(time.Millisecond)) &&
		r.Evidence.SameClaim(claim)
}

// CapabilityEvidenceListOptions selects one page of a sealed Submission's
// capability evidence for review.
type CapabilityEvidenceListOptions struct {
	SubmissionID model.SubmissionID
	AfterID      model.IntegrityEvidenceID
	Limit        int
}

// CapabilityEvidencePage is one page of capability evidence in ID order.
type CapabilityEvidencePage struct {
	Items   []model.CapabilityEvidence
	HasMore bool
}

// CapabilityEvidenceReviewMaximum bounds one review page.
const CapabilityEvidenceReviewMaximum = 100
