// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScreenRecordingDisclosureExistsExactlyWhenTheExamRecords(t *testing.T) {
	t.Parallel()

	now := NowUTC()
	retention := &RetentionPolicy{InstitutionID: NewInstitutionID(), Revision: 3, CreatedAt: now, UpdatedAt: now}
	retention.IntegrityRetentionDays = 400
	require.NoError(t, retention.Validate())
	on, off := NativeScreenRecordingPolicy{Enabled: true}, NativeScreenRecordingPolicy{}

	none, err := NewScreenRecordingDisclosure(off, retention)
	require.NoError(t, err)
	require.Nil(t, none)
	require.NoError(t, ValidateScreenRecordingDisclosure(none, off))
	require.Error(t, ValidateScreenRecordingDisclosure(none, on), "a recording Exam must disclose it")

	disclosure, err := NewScreenRecordingDisclosure(on, retention)
	require.NoError(t, err)
	require.Equal(t, int64(3), disclosure.RetentionPolicyRevision)
	require.Equal(t, 400, disclosure.ClipRetentionDays, "clips follow the integrity retention period")
	require.NoError(t, ValidateScreenRecordingDisclosure(disclosure, on))
	require.Error(t, ValidateScreenRecordingDisclosure(disclosure, off), "a notice for an Exam that does not record is wrong")

	for name, change := range map[string]func(*ScreenRecordingDisclosure){
		"notice":    func(d *ScreenRecordingDisclosure) { d.NoticeID = "other" },
		"audience":  func(d *ScreenRecordingDisclosure) { d.Audience = "everyone" },
		"recording": func(d *ScreenRecordingDisclosure) { d.LocalRecording = "kept" },
		"uploads":   func(d *ScreenRecordingDisclosure) { d.Uploads = "everything" },
		"revision":  func(d *ScreenRecordingDisclosure) { d.RetentionPolicyRevision = 0 },
		"days":      func(d *ScreenRecordingDisclosure) { d.ClipRetentionDays = -1 },
		"anchor":    func(d *ScreenRecordingDisclosure) { d.RetentionAnchor = "upload" },
	} {
		changed := *disclosure
		change(&changed)
		require.Error(t, ValidateScreenRecordingDisclosure(&changed, on), name)
	}

	_, err = NewScreenRecordingDisclosure(on, nil)
	require.Error(t, err)
}
