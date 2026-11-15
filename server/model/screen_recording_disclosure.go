// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

// ScreenRecordingDisclosure tells the candidate, before Start, what the
// screen recorder does when the Exam records the screen: Desktop records the
// whole session on the device and deletes it at submission, uploads only the
// ranges the Sitting's Exam Managers ask for or a Flag asks for, and the
// uploaded clips are kept as integrity evidence. Desktop words it from these
// identifiers. Like the Browser activity notice, it is neither consent nor a
// grant of access. Zero days denotes no configured automatic expiry.
type ScreenRecordingDisclosure struct {
	NoticeID                string `json:"notice_id"`
	Audience                string `json:"audience"`
	LocalRecording          string `json:"local_recording"`
	Uploads                 string `json:"uploads"`
	RetentionPolicyRevision int64  `json:"retention_policy_revision"`
	ClipRetentionDays       int    `json:"clip_retention_days"`
	RetentionAnchor         string `json:"retention_anchor"`
}

const (
	screenRecordingNoticeID       = "screen_recording"
	screenRecordingAudience       = "exam_managers_of_the_sitting"
	screenRecordingLocalRecording = "whole_session_deleted_at_submission"
	screenRecordingUploads        = "requested_and_flagged_ranges_only"
	screenRecordingAnchor         = "eligible_closed_sitting_records_completion"
)

// NewScreenRecordingDisclosure returns the notice for an Exam whose policy
// records the screen, or nil when it does not. Clips are integrity evidence,
// so they follow the integrity retention period.
func NewScreenRecordingDisclosure(recording NativeScreenRecordingPolicy, retention *RetentionPolicy) (*ScreenRecordingDisclosure, error) {
	if retention == nil || retention.Validate() != nil {
		return nil, errRetentionPolicyInvalid
	}
	if !recording.Enabled {
		return nil, nil
	}
	return &ScreenRecordingDisclosure{
		NoticeID:                screenRecordingNoticeID,
		Audience:                screenRecordingAudience,
		LocalRecording:          screenRecordingLocalRecording,
		Uploads:                 screenRecordingUploads,
		RetentionPolicyRevision: retention.Revision,
		ClipRetentionDays:       retention.IntegrityRetentionDays,
		RetentionAnchor:         screenRecordingAnchor,
	}, nil
}

// ValidateScreenRecordingDisclosure checks that disclosure is present exactly
// when recording is on, and is well formed.
func ValidateScreenRecordingDisclosure(disclosure *ScreenRecordingDisclosure, recording NativeScreenRecordingPolicy) error {
	if disclosure == nil {
		if recording.Enabled {
			return errRetentionPolicyInvalid
		}
		return nil
	}
	if !recording.Enabled ||
		disclosure.NoticeID != screenRecordingNoticeID ||
		disclosure.Audience != screenRecordingAudience ||
		disclosure.LocalRecording != screenRecordingLocalRecording ||
		disclosure.Uploads != screenRecordingUploads ||
		disclosure.RetentionPolicyRevision < 1 ||
		!securitySafeInt(disclosure.RetentionPolicyRevision) ||
		disclosure.ClipRetentionDays < 0 ||
		disclosure.ClipRetentionDays > 36500 ||
		disclosure.RetentionAnchor != screenRecordingAnchor {
		return errRetentionPolicyInvalid
	}
	return nil
}
