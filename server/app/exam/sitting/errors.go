// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

// publicFailures presents every Sitting use-case failure: unexpected errors
// become exam.sitting.unavailable, and protected targets conceal denial and absence.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.sitting.unavailable",
	Concealed:   []apperror.Code{"exam.sitting.not_found"},
}

var storeErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
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
	},
	NotFound:     "exam.sitting.not_found",
	Conflict:     "exam.sitting.conflict",
	Invalid:      "exam.sitting.invalid",
	InvalidField: "value",
	Unavailable:  "exam.sitting.unavailable",
}

func (s *Service) failAudit(ctx context.Context, auditID model.AuditEventID, err error) error {
	mapped := storeErrors.Translate(err)
	code := apperror.CodeOf(mapped, "exam.sitting.unavailable")
	if auditErr := s.auditor.Fail(ctx, auditID, code); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}

func jobFactoryUnavailable(message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	} else {
		cause = errors.Join(errors.New(message), cause)
	}
	return apperror.New("exam.sitting.unavailable").Wrap(cause)
}

func (s *Service) recordOperational(event string, err error) {
	if s.operational != nil {
		s.operational.RecordOperational("exam_sitting", event, err)
	}
}
