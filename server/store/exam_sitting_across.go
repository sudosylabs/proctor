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

// ExamSittingAcrossListOptions selects one page of sittings across every
// exam Visibility reaches, under exam.sitting.view, in ascending
// (ScheduledStartAt, SittingID) order after the keyset. StartsBefore and
// EndsAfter are each optional. Open and paused sittings on the page are also
// counted as the candidate board counts them, without the reader.
type ExamSittingAcrossListOptions struct {
	Visibility            ExamListVisibility
	ReaderUserID          model.UserID
	States                []model.ExamSittingState
	StartsBefore          time.Time
	EndsAfter             time.Time
	AcademicUnitID        model.AcademicUnitID
	AfterScheduledStartAt time.Time
	AfterSittingID        model.ExamSittingID
	Limit                 int
}

// ExamSittingAcrossItem is one sitting with its exam and class. Counts is
// set for an open or paused sitting.
type ExamSittingAcrossItem struct {
	SittingID        model.ExamSittingID
	ExamID           model.ExamID
	ExamTitle        string
	AcademicUnitID   model.AcademicUnitID
	ClassID          model.ClassID
	ClassDisplayName string
	State            model.ExamSittingState
	Revision         int64
	ScheduledStartAt time.Time
	ScheduledEndAt   time.Time
	Counts           *SittingCandidateStatusCounts
}

// ExamSittingAcrossPage is one page, read at ServerTime.
type ExamSittingAcrossPage struct {
	ServerTime time.Time
	Items      []ExamSittingAcrossItem
}
