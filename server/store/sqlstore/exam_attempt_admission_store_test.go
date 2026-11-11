// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamAttemptAdmissionConflictNamesEachRefusal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		refusal    error
		resource   string
		constraint string
	}{
		{"membership", model.ErrAttemptAdmissionMembership, "exam_attempt", "exam_attempt_membership"},
		{"sitting state", model.ErrAttemptAdmissionSittingState, "exam_sitting", "exam_sitting_state"},
		{"management authority", model.ErrAttemptAdmissionManagementAuthority, "exam_attempt", "exam_attempt_management_authority"},
		{"deadline reached", model.ErrAttemptAdmissionDeadlineReached, "exam_sitting", "exam_sitting_deadline_reached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var conflict *store.ErrConflict
			require.ErrorAs(t, examAttemptAdmissionConflict(tc.refusal), &conflict)
			require.Equal(t, tc.resource, conflict.Resource)
			require.Equal(t, tc.constraint, conflict.Constraint)
		})
	}

	t.Run("an unmapped refusal is not a conflict", func(t *testing.T) {
		t.Parallel()
		unmapped := errors.New("unmapped")
		err := examAttemptAdmissionConflict(unmapped)
		var conflict *store.ErrConflict
		require.NotErrorAs(t, err, &conflict)
		require.ErrorIs(t, err, unmapped)
	})
}
