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

// ExamSittingHistoryMaximum bounds the manager actions a sitting read returns,
// newest first.
const ExamSittingHistoryMaximum = 20

// ExamSittingHistoryKind is a manager action on a sitting that carries a
// private reason.
type ExamSittingHistoryKind string

const (
	ExamSittingHistoryCanceled ExamSittingHistoryKind = "canceled"
	ExamSittingHistoryPaused   ExamSittingHistoryKind = "paused"
	ExamSittingHistoryResumed  ExamSittingHistoryKind = "resumed"
	ExamSittingHistoryExtended ExamSittingHistoryKind = "extended"
	ExamSittingHistoryClosed   ExamSittingHistoryKind = "closed"
)

// ExamSittingHistoryEntry is one manager action and the manager's private
// reason. It is for the sitting's managers only.
type ExamSittingHistoryEntry struct {
	Kind        ExamSittingHistoryKind
	At          time.Time
	ActorUserID model.UserID
	Reason      string
}

// ExamSittingCorrectionSummary is one live correction of a sitting and how
// many of the Attempts it applies to have acknowledged it. RequiredCount is
// zero for a correction that asks for no acknowledgement.
type ExamSittingCorrectionSummary struct {
	RevisionID              model.ExamRevisionID
	Number                  int64
	EffectiveAt             time.Time
	AcknowledgementRequired bool
	AcknowledgedCount       int
	RequiredCount           int
}

// SittingCandidateDetailOptions names the candidates of one page of a
// sitting's board; at most 200.
type SittingCandidateDetailOptions struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	CandidateUserIDs []model.UserID
}

// SittingCandidateDetail is what the board shows about one candidate beyond
// their status. DesktopRegistration is the candidate's newest unrevoked
// Desktop registration. DeviceName is the display name of the registration
// behind the latest participation, empty when there is none or it has no
// name. ChangedFileCount counts workspace entries added, changed or removed
// against the Starter Workspace. CurrentCorrectionAcknowledgedAt is set when
// the sitting's current revision is a live correction the candidate has
// acknowledged.
type SittingCandidateDetail struct {
	CandidateUserID                 model.UserID
	DesktopRegistration             *model.DesktopBuildTuple
	DeviceName                      string
	ChangedFileCount                int64
	FocusLossCount                  int64
	CurrentCorrectionAcknowledgedAt model.OptionalTime
}

// ExamAttemptManagerEnd is who ended an Attempt for the sitting's managers,
// and their private reason.
type ExamAttemptManagerEnd struct {
	AttemptID   model.ExamAttemptID
	ActorUserID model.UserID
	Reason      string
	EndedAt     time.Time
}
