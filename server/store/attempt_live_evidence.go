// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// LiveEvidenceReadMaximum bounds one page of a live evidence read.
const LiveEvidenceReadMaximum = 200

// LiveEvidenceScope names an attempt for the live evidence reads, read at
// AsOf. Submitted reports a sealed Submission, after which the attempt adds
// no evidence of its own; Retired reports that retention removed its
// integrity data.
type LiveEvidenceScope struct {
	AttemptID       model.ExamAttemptID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
	Submitted       bool
	Retired         bool
	AsOf            time.Time
}

// LiveEvidenceWindow keeps records at or after From and before To; a zero
// bound is open.
type LiveEvidenceWindow struct {
	From time.Time
	To   time.Time
}

// LiveFlagListOptions pages an attempt's flags by ID, by when they opened.
type LiveFlagListOptions struct {
	AttemptID   model.ExamAttemptID
	Window      LiveEvidenceWindow
	AfterFlagID model.IntegrityFlagID
	Limit       int
}

// LiveEvidenceListOptions pages one flag's evidence by ID, by when it was
// observed.
type LiveEvidenceListOptions struct {
	AttemptID       model.ExamAttemptID
	FlagID          model.IntegrityFlagID
	Window          LiveEvidenceWindow
	AfterEvidenceID model.IntegrityEvidenceID
	Limit           int
}

// LiveNativeConditionListOptions pages an attempt's native condition records
// by ID, by when their rule changed.
type LiveNativeConditionListOptions struct {
	AttemptID model.ExamAttemptID
	Window    LiveEvidenceWindow
	AfterID   model.IntegrityEvidenceID
	Limit     int
}

// LiveTimelineKind is what one timeline entry is.
type LiveTimelineKind string

const (
	LiveTimelineFlag            LiveTimelineKind = "flag"
	LiveTimelineEvidence        LiveTimelineKind = "evidence"
	LiveTimelineNativeCondition LiveTimelineKind = "native_condition"
)

// LiveTimelineCursor is the keyset of the last entry seen.
type LiveTimelineCursor struct {
	At   time.Time
	Kind LiveTimelineKind
	ID   string
}

// LiveTimelineListOptions pages an attempt's flags, evidence and native
// condition records together, in (At, Kind, ID) order.
type LiveTimelineListOptions struct {
	AttemptID model.ExamAttemptID
	Window    LiveEvidenceWindow
	After     *LiveTimelineCursor
	Limit     int
}

// LiveTimelineItem is one entry; exactly the member its Kind names is set.
type LiveTimelineItem struct {
	Kind            LiveTimelineKind
	At              time.Time
	Flag            *ExamIntegrityFlagSummary
	Evidence        *model.IntegrityEvidence
	NativeCondition *model.NativeConditionEvidence
}

// LiveTimelinePage is one page; HasMore reports that another follows.
type LiveTimelinePage struct {
	Items   []LiveTimelineItem
	HasMore bool
}
