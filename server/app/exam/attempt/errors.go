// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// publicFailures presents every Attempt use-case failure: unexpected errors
// become exam.attempt.unavailable, and protected targets conceal denial and absence.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.attempt.unavailable",
	Concealed:   []apperror.Code{"exam.attempt.not_found", "exam.attempt.continuity_invalid"},
}

var storeErrors = apperror.StoreTable{
	Rules: slices.Concat(
		apperror.Conflicts("browser_delivery", deliveryConflictCodes),
		apperror.Conflicts("security_preflight", map[string]apperror.Code{
			"attempt_not_ready":         "exam.security.attempt_not_ready",
			"configuration_unsupported": "exam.security.configuration_unsupported",
			"policy_changed":            "exam.security.policy_changed",
			"posture_blocked":           "exam.security.posture_blocked",
			"preflight_consumed":        "exam.security.preflight_consumed",
			"preflight_expired":         "exam.security.preflight_expired",
			"preflight_rate_limited":    "exam.security.preflight_rate_limited",
			"preflight_report_changed":  "exam.security.preflight_report_changed",
			"preflight_required":        "exam.security.preflight_required",
			"preflight_superseded":      "exam.security.preflight_superseded",
			"resume_required":           "exam.security.resume_required",
			"session_changed":           "exam.security.session_changed",
		}),
	),
	Constraints: map[string]apperror.Code{
		"control_conflict":                         "exam.security.control_conflict",
		"exam_attempt_membership":                  "exam.attempt.not_found",
		"exam_sitting_state":                       "exam.attempt.sitting_not_open",
		"exam_sitting_deadline_reached":            "exam.attempt.sitting_not_open",
		"exam_attempt_state":                       "exam.attempt.state_conflict",
		"exam_attempt_management_authority":        "exam.attempt.management_authority_conflict",
		"desktop_compatibility_policy_revision":    "exam.attempt.desktop_incompatible",
		"exam_attempt_revision":                    "exam.attempt.revision_conflict",
		"exam_sitting_revision_selection":          "exam.attempt.revision_conflict",
		"exam_correction_acknowledgement_target":   "exam.attempt.correction_conflict",
		"exam_correction_acknowledgement_order":    "exam.attempt.correction_conflict",
		"exam_correction_acknowledgement_required": "exam.attempt.correction_conflict",
		"browser_source_fence":                     "exam.attempt.browser_activity_conflict",
		"browser_source_conflict":                  "exam.attempt.browser_activity_conflict",
		"browser_source_current":                   "exam.attempt.browser_activity_conflict",
		"browser_source_predecessor":               "exam.attempt.browser_activity_conflict",
		"browser_source_policy":                    "exam.attempt.browser_activity_conflict",
		"browser_policy_disabled":                  "exam.attempt.browser_activity_conflict",
		"browser_source_limit":                     "exam.browser.source_budget_exhausted",
		"browser_activity_sequence":                "exam.attempt.browser_activity_conflict",
		"browser_activity_policy_semantics":        "exam.attempt.browser_activity_conflict",
		"browser_activity_accounting":              "exam.attempt.browser_activity_conflict",
		"exam_submission_causal_selector":          "idempotency.conflict",
		"attempt_configuration_required":           "exam.attempt.configuration_required",
		"attempt_configuration_stale":              "exam.attempt.configuration_stale",
		"attempt_configuration_frozen":             "exam.attempt.configuration_frozen",
		"attempt_configuration_unsupported":        "exam.attempt.configuration_unsupported",
		"attempt_suspension_active":                "exam.attempt.suspension_conflict",
		"attempt_participation_credential":         "exam.attempt.continuity_invalid",
		"attempt_participation_generation":         "exam.attempt.connection_closed",
		"attempt_participation_sequence":           "exam.attempt.renewal_conflict",
		"focus_loss_sequence":                      "exam.attempt.focus_loss_conflict",
		"attempt_participation_expired":            "exam.attempt.connection_lost",
		"attempt_connection_open":                  "exam.attempt.already_connected",
		"attempt_connection_closed":                "exam.attempt.connection_closed",
		"attempt_workspace_path":                   "exam.attempt.workspace.path_conflict",
		"attempt_workspace_entry":                  "exam.attempt.workspace.entry_conflict",
		"attempt_workspace_content_version":        "exam.attempt.workspace.content_conflict",
		"attempt_workspace_not_empty":              "exam.attempt.workspace.directory_not_empty",
		"attempt_workspace_entry_limit":            "exam.attempt.workspace.entry_limit",
		"attempt_workspace_size_limit":             "exam.attempt.workspace.size_limit",
		"attempt_workspace_object_state":           "exam.attempt.workspace.object_conflict",
		"attempt_workspace_cursor":                 "exam.attempt.workspace.cursor_conflict",
	},
	NotFound:     "exam.attempt.not_found",
	Conflict:     "exam.attempt.conflict",
	Invalid:      "exam.attempt.invalid",
	InvalidField: "value",
	Unavailable:  "exam.attempt.unavailable",
}

// deliveryConflictCodes presents delivery conflicts, whose constraint names
// are meaningful only on the native_delivery and browser_delivery entities.
var deliveryConflictCodes = map[string]apperror.Code{
	"append_rate_limited":     "exam.delivery.append_rate_limited",
	"batch_conflict":          "exam.delivery.batch_conflict",
	"declaration_conflict":    "exam.delivery.declaration_conflict",
	"detail_budget_exhausted": "exam.delivery.detail_budget_exhausted",
	"pending_capacity":        "exam.delivery.pending_capacity",
	"replay_window_exceeded":  "exam.delivery.replay_window_exceeded",
	"summary_rate_limited":    "exam.delivery.summary_rate_limited",
}

// storeError presents an Attempt Store failure. Typed refusals that carry
// safe detail are presented before the declarative table.
func storeError(table apperror.StoreTable, err error) error {
	var nativeRefusal *store.NativeDeliveryRefusal
	var browserRefusal *store.BrowserDeliveryRefusal
	var capacity *model.DeliveryMetadataCapacity
	switch {
	case errors.Is(err, store.ErrInvalidState):
		return apperror.New("exam.attempt.unavailable").Wrap(err)
	case errors.As(err, &nativeRefusal):
		return apperror.New(deliveryRefusalCode(nativeRefusal.Reason)).Wrap(err)
	case errors.As(err, &browserRefusal):
		return apperror.New(deliveryRefusalCode(browserRefusal.Reason)).Wrap(err)
	case errors.As(err, &capacity):
		return apperror.New("exam.delivery.metadata_capacity").WithFields(map[string]string{
			"control_metadata_bytes":       strconv.FormatInt(capacity.ControlMetadataBytes, 10),
			"control_metadata_limit_bytes": strconv.FormatInt(capacity.ControlMetadataLimitBytes, 10),
			"required_reservation_bytes":   strconv.FormatInt(capacity.RequiredReservationBytes, 10),
			"remaining_reservable_bytes":   strconv.FormatInt(capacity.RemainingReservableBytes, 10),
		}).Wrap(err)
	default:
		return table.Translate(err)
	}
}

// deliveryRefusalCode presents the bounded reason of a committed delivery
// refusal.
func deliveryRefusalCode(reason string) apperror.Code {
	switch reason {
	case "sequence_limit":
		return "exam.delivery.sequence_limit"
	case "detail_budget_exhausted":
		return "exam.delivery.detail_budget_exhausted"
	default:
		return "exam.attempt.unavailable"
	}
}

func (s *Service) failAudit(ctx context.Context, auditID model.AuditEventID, err error) error {
	mapped := err
	code := "exam.attempt.unavailable"
	var fault *apperror.Error
	if !errors.As(mapped, &fault) {
		mapped = storeError(storeErrors, err)
	}
	if errors.As(mapped, &fault) {
		code = fault.Code()
	}
	if auditErr := s.auditor.Fail(ctx, auditID, code); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}

func (s *Service) recordOperational(event string, err error) {
	if s.operational != nil {
		s.operational.RecordOperational("exam_attempt", event, err)
	}
}
