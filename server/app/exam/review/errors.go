// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

// publicFailures presents every Integrity Review use-case failure: unexpected errors
// become exam.integrity_review.unavailable, and protected targets conceal denial and absence.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.integrity_review.unavailable",
	Concealed:   []apperror.Code{"exam.integrity_review.not_found"},
}

var storeErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
		"integrity_review_revision":   "exam.integrity_review.revision_conflict",
		"integrity_decision_revision": "exam.integrity_review.revision_conflict",
		"integrity_review_incomplete": "exam.integrity_review.incomplete",
		"integrity_review_too_large":  "exam.integrity_review.too_large",
	},
	NotFound:     "exam.integrity_review.not_found",
	Conflict:     "exam.integrity_review.state_conflict",
	Invalid:      "exam.integrity_review.invalid",
	InvalidField: "value",
	Unavailable:  "exam.integrity_review.unavailable",
}

func (s *Service) failAudit(ctx context.Context, auditID model.AuditEventID, err error) error {
	mapped := storeErrors.Translate(err)
	code := apperror.CodeOf(mapped, "exam.integrity_review.unavailable")
	if auditErr := s.auditor.Fail(ctx, auditID, code); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}
