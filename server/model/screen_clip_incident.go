// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "time"

// IncidentScreenClipRange is the range an incident clip covers around at.
func IncidentScreenClipRange(at time.Time) ScreenClipRange {
	at = TimeUTC(at)
	return ScreenClipRange{Start: at.Add(-ScreenClipIncidentMargin), End: at.Add(ScreenClipIncidentMargin)}
}

// NewIncidentScreenClip records the server's request around the moment an
// Integrity Flag opened.
func NewIncidentScreenClip(id ScreenClipID, attemptID ExamAttemptID, flagID IntegrityFlagID, at time.Time) (*ScreenClip, error) {
	return newScreenClip(ScreenClip{
		ID: id, AttemptID: attemptID, Source: ScreenClipSourceIncident, FlagID: flagID,
		Range: IncidentScreenClipRange(at),
	}, at)
}
