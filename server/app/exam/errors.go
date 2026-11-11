// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"github.com/sudosylabs/proctor/server/app/apperror"
)

// publicFailures presents every Exam authoring, publication, records, and export use-case failure: unexpected errors
// become exam.unavailable, and protected targets conceal denial and absence.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.unavailable",
	Concealed:   []apperror.Code{"exam.not_found"},
}

var examStoreErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
		"exam_archived":                   "exam.archived",
		"exam_draft_revision":             "exam.draft.revision_conflict",
		"exam_revision":                   "exam.revision_conflict",
		"exam_draft_no_changes":           "exam.draft.no_changes",
		"exam_revision_no_changes":        "exam.revision.no_changes",
		"exam_revision_capacity":          "exam.revision.capacity_exceeded",
		"exam_manager_exists":             "exam.manager.exists",
		"exam_manager_missing":            "exam.manager.not_found",
		"exam_manager_ineligible":         "exam.manager.ineligible",
		"exam_manager_candidate_conflict": "exam.manager.candidate_conflict",
		"exam_owner_manager":              "exam.manager.owner_protected",
		"exam_owner_no_changes":           "exam.owner.no_changes",
	},
	NotFound:    "exam.not_found",
	Conflict:    "exam.conflict",
	Invalid:     "exam.invalid",
	Unavailable: "exam.unavailable",
}

func (a *Authoring) recordOperational(event string, err error) {
	if a.operational != nil {
		a.operational.RecordOperational("exam", event, err)
	}
}

func (p *Publication) recordOperational(event string, err error) {
	if p.operational != nil {
		p.operational.RecordOperational("exam", event, err)
	}
}

// examRecordsStoreErrors conceals an authorization refusal discovered by the
// Store as an absent Exam.
var examRecordsStoreErrors = examStoreErrors.With(
	apperror.StoreRule{Kind: apperror.StoreConflict, Entity: "authorization", Code: "exam.not_found"})

var examExportStoreErrors = apperror.StoreTable{
	Rules: append([]apperror.StoreRule{{Kind: apperror.StoreConflict, Entity: "authorization", Code: "exam.export.not_found"}},
		apperror.Conflicts("exam_export", map[string]apperror.Code{
			"policy_unconfigured": "exam.export.policy_unconfigured",
			"limit":               "exam.export.limit",
			"source_retired":      "exam.export.source_retired",
			"scope_changed":       "exam.export.scope_changed",
			"expired":             "exam.export.expired",
			"claim_lost":          "exam.export.claim_lost",
		})...),
	NotFound:    "exam.export.not_found",
	Conflict:    "exam.export.conflict",
	Unavailable: "exam.export.unavailable",
}
