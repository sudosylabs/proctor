// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"testing"

	"github.com/sudosylabs/proctor/server/store"
)

func TestStoreConflictsHaveStableFaults(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"exam_sitting_revision":                "exam.sitting.revision_conflict",
		"exam_sitting_no_changes":              "exam.sitting.no_changes",
		"exam_sitting_state":                   "exam.sitting.state_conflict",
		"exam_sitting_extension_not_later":     "exam.sitting.extension_not_later",
		"exam_sitting_deadline_reached":        "exam.sitting.deadline_reached",
		"exam_sitting_class_lineage":           "exam.sitting.class_ineligible",
		"exam_sitting_period_containment":      "exam.sitting.schedule_outside_period",
		"exam_sitting_schedule_outside_period": "exam.sitting.schedule_outside_period",
		"exam_sitting_not_future":              "exam.sitting.schedule_not_future",
		"exam_sitting_revision_lineage":        "exam.sitting.not_found",
		"exam_archived":                        "exam.archived",
	}
	for constraint, want := range tests {
		constraint, want := constraint, want
		t.Run(constraint, func(t *testing.T) {
			t.Parallel()
			assertFaultCode(t, storeErrors.Translate(store.NewErrConflict("exam_sitting", constraint, nil)), want)
		})
	}
}
