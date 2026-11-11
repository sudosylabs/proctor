// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

// publicFailures presents every live-correction use-case failure: unexpected errors
// become exam.sitting.correction.unavailable, and protected targets conceal denial and absence.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.sitting.correction.unavailable",
	Concealed:   []apperror.Code{"exam.sitting.correction.not_found"},
}

var storeErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
		"exam_archived":                     "exam.archived",
		"exam_sitting_revision":             "exam.sitting.revision_conflict",
		"exam_sitting_state":                "exam.sitting.state_conflict",
		"exam_sitting_revision_selection":   "exam.sitting.revision_conflict",
		"exam_sitting_deadline_reached":     "exam.sitting.deadline_reached",
		"exam_correction_base_revision":     "exam.sitting.revision_conflict",
		"exam_correction_no_changes":        "exam.sitting.correction.no_changes",
		"exam_correction_limit":             "exam.sitting.correction.limit",
		"exam_correction_resource_limit":    "exam.resource.limit",
		"exam_correction_resource_manifest": "exam.sitting.correction.manifest_conflict",
		"exam_correction_resource_stage":    "exam.sitting.correction.stage_conflict",
	},
	NotFound:     "exam.sitting.correction.not_found",
	Conflict:     "exam.sitting.correction.conflict",
	Invalid:      "exam.sitting.correction.invalid",
	InvalidField: "value",
	Unavailable:  "exam.sitting.correction.unavailable",
}

type invalidContentError interface{ InvalidExamResourceContent() }

type workCapacityError interface{ WorkCapacityExceeded() }

func mapContentError(err error) error {
	var capacity workCapacityError
	if errors.As(err, &capacity) {
		return apperror.New("service.busy").Wrap(err)
	}
	var invalidContent invalidContentError
	if errors.As(err, &invalidContent) {
		return apperror.New("exam.sitting.correction.content_invalid").Wrap(err)
	}
	return apperror.New("exam.sitting.correction.unavailable").Wrap(err)
}

func (s *Service) failAudit(ctx context.Context, id model.AuditEventID, err error) error {
	mapped := storeErrors.Translate(err)
	code := apperror.CodeOf(mapped, "exam.sitting.correction.unavailable")
	if auditErr := s.auditor.Fail(ctx, id, code); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}
