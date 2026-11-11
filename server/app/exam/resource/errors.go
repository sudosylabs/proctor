// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

// publicFailures presents every Exam Resource use-case failure: unexpected errors
// become exam.resource.unavailable, and protected targets conceal denial and absence.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.resource.unavailable",
	Concealed:   []apperror.Code{"exam.resource.not_found"},
}

var storeErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
		"exam_archived":            "exam.archived",
		"exam_draft_revision":      "exam.draft.revision_conflict",
		"exam_resource_limit":      "exam.resource.limit",
		"exam_resource_no_changes": "exam.resource.no_changes",
		"exam_resource_order":      "exam.resource.order_invalid",
		"exam_resource_upload":     "exam.resource.upload_invalid",
		"exam_resource_changed":    "exam.resource.revision_conflict",
	},
	NotFound:     "exam.resource.not_found",
	Conflict:     "exam.resource.conflict",
	Invalid:      "exam.resource.invalid",
	InvalidField: "value",
	Unavailable:  "exam.resource.unavailable",
}

type invalidContentError interface {
	InvalidExamResourceContent()
}

type workCapacityError interface{ WorkCapacityExceeded() }

func mapContentError(err error) error {
	var capacity workCapacityError
	if errors.As(err, &capacity) {
		return apperror.New("service.busy").Wrap(err)
	}
	var invalidContent invalidContentError
	if errors.As(err, &invalidContent) {
		return apperror.New("exam.resource.content_invalid").Wrap(err)
	}
	return apperror.New("exam.resource.unavailable").Wrap(err)
}

func (s *Service) failAudit(ctx context.Context, auditID model.AuditEventID, err error) error {
	mapped := storeErrors.Translate(err)
	code := apperror.CodeOf(mapped, "exam.resource.unavailable")
	if auditErr := s.auditor.Fail(ctx, auditID, code); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}
