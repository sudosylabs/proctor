// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RunInvitationBatch runs one Invitation operation over up to
// MaximumInvitationBatchItems rows after one batch authorization at the scope.
func (s *Service) RunInvitationBatch(
	ctx context.Context,
	inv invocation.Invocation,
	command RunInvitationBatchCommand,
) (InvitationBatchResult, error) {
	command.ScopeID = strings.TrimSpace(command.ScopeID)
	if !command.Operation.IsValid() ||
		!model.IsValidOpaqueID(command.ScopeID) ||
		command.IdempotencyKey == "" ||
		len(command.Items) < 1 ||
		len(command.Items) > MaximumInvitationBatchItems ||
		!invitationBatchScopeMatchesOperation(command.Operation, command.ScopeType) {
		return InvitationBatchResult{}, apperror.New("request.invalid")
	}

	resource, err := s.invitationBatchAuthorizationResource(ctx, command.ScopeType, command.ScopeID)
	if err != nil {
		return InvitationBatchResult{}, err
	}
	if err = s.authorize(ctx, inv, model.ActionOnboardingBatchManage, resource); err != nil {
		return InvitationBatchResult{}, err
	}

	if command.Operation == InvitationBatchAcademicUnitRoleCreate || command.Operation == InvitationBatchInstitutionRoleCreate {
		if err = access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
			return InvitationBatchResult{}, err
		}
	}

	result := InvitationBatchResult{Operation: command.Operation, Items: make([]InvitationBatchItemResult, 0, len(command.Items))}
	itemKeyCounts := make(map[string]int, len(command.Items))
	itemsByKey := make(map[string]InvitationBatchItemCommand, len(command.Items))
	for _, item := range command.Items {
		itemKeyCounts[item.IdempotencyKey]++
		itemsByKey[item.IdempotencyKey] = item
	}

	duplicateWinners := invitationBatchDuplicateWinners(command.Operation, command.Items, itemKeyCounts)
	for index, item := range command.Items {
		outcome := InvitationBatchItemResult{Index: index}
		if err = validateInvitationBatchItem(command.Operation, item); err != nil {
			outcome.Status, outcome.ErrorCode = InvitationBatchItemFailed, "request.invalid"
			result.append(outcome)
			continue
		}

		if itemKeyCounts[item.IdempotencyKey] != 1 {
			outcome.Status, outcome.ErrorCode = InvitationBatchItemFailed, "request.invalid"
			result.append(outcome)
			continue
		}

		duplicateKey := invitationBatchDuplicateKey(command.Operation, item)
		itemKey := invitationBatchItemIdempotencyKey(command.IdempotencyKey, item.IdempotencyKey)
		winner := duplicateWinners[duplicateKey]
		duplicate := duplicateKey != "" && winner != item.IdempotencyKey
		canonicalKey := invitationBatchItemIdempotencyKey(command.IdempotencyKey, winner)
		var canonicalFingerprint [sha256.Size]byte
		if duplicate && (command.Operation == InvitationBatchResend || command.Operation == InvitationBatchRevoke) {
			canonicalItem := itemsByKey[winner]
			canonical, canonicalErr := idempotency.Prepare(
				inv,
				invitationBatchLifecycleOperation(command.Operation),
				canonicalKey,
				invitationLifecycleIdempotencySemantic{
					ID:       strings.TrimSpace(canonicalItem.InvitationID.String()),
					Revision: canonicalItem.ExpectedRevision,
				},
			)
			if canonicalErr != nil || canonical == nil {
				outcome.Status, outcome.ErrorCode = InvitationBatchItemFailed, "request.invalid"
				result.append(outcome)
				continue
			}

			canonicalFingerprint = canonical.Fingerprint
		}

		outcome.InvitationID, outcome.Status, err = s.runInvitationBatchItem(
			ctx,
			inv,
			command,
			item,
			itemKey,
			duplicate,
			canonicalKey,
			canonicalFingerprint,
		)
		if err != nil {
			outcome.Status, outcome.ErrorCode = InvitationBatchItemFailed, invitationBatchPublicErrorCode(err)
		}

		result.append(outcome)
	}

	return result, nil
}

func invitationBatchDuplicateWinners(
	operation InvitationBatchOperation,
	items []InvitationBatchItemCommand,
	itemKeyCounts map[string]int,
) map[string]string {
	winners := make(map[string]string, len(items))
	for _, item := range items {
		if validateInvitationBatchItem(operation, item) != nil || itemKeyCounts[item.IdempotencyKey] != 1 {
			continue
		}

		duplicateKey := invitationBatchDuplicateKey(operation, item)
		if duplicateKey == "" {
			continue
		}
		if winner, exists := winners[duplicateKey]; !exists || item.IdempotencyKey < winner {
			winners[duplicateKey] = item.IdempotencyKey
		}
	}

	return winners
}

func (r *InvitationBatchResult) append(item InvitationBatchItemResult) {
	r.Items = append(r.Items, item)
	switch item.Status {
	case InvitationBatchItemSucceeded:
		r.Succeeded++
	case InvitationBatchItemNoOp:
		r.NoOp++
	case InvitationBatchItemFailed:
		r.Failed++
	}
}

func invitationBatchScopeMatchesOperation(operation InvitationBatchOperation, scopeType model.RoleScopeType) bool {
	switch operation {
	case InvitationBatchStudentClassCreate:
		return scopeType == model.RoleScopeClass
	case InvitationBatchTeacherAcademicUnitCreate, InvitationBatchAcademicUnitRoleCreate:
		return scopeType == model.RoleScopeAcademicUnit
	case InvitationBatchInstitutionRoleCreate:
		return scopeType == model.RoleScopeInstitution
	case InvitationBatchResend, InvitationBatchRevoke:
		return scopeType == model.RoleScopeInstitution || scopeType == model.RoleScopeAcademicUnit || scopeType == model.RoleScopeClass
	default:
		return false
	}
}

func (s *Service) invitationBatchAuthorizationResource(
	ctx context.Context,
	scopeType model.RoleScopeType,
	scopeID string,
) (model.Resource, error) {
	switch scopeType {
	case model.RoleScopeInstitution:
		return model.Resource{Type: model.ResourceInstitution, ID: scopeID}, nil
	case model.RoleScopeAcademicUnit:
		return model.Resource{Type: model.ResourceAcademicUnit, ID: scopeID}, nil
	case model.RoleScopeClass:
		return model.Resource{Type: model.ResourceClass, ID: scopeID}, nil
	default:
		return model.Resource{}, apperror.New("request.invalid")
	}
}

func invitationBatchItemIdempotencyKey(batchKey, itemKey string) string {
	return fmt.Sprintf("%d:%s:%s", len(batchKey), batchKey, itemKey)
}

func invitationBatchLifecycleOperation(operation InvitationBatchOperation) string {
	if operation == InvitationBatchResend {
		return "invitation.resend"
	}
	if operation == InvitationBatchRevoke {
		return "invitation.revoke"
	}
	return ""
}

func (s *Service) recordInvitationCommandDuplicate(
	ctx context.Context,
	commandIdempotency *store.CommandIdempotency,
	attempt access.Mutation,
	input store.InvitationBatchDuplicate,
) (*store.InvitationBatchCommandResult, error) {
	return access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationBatchCommandResult, error) {
			input.AuditEventID, input.AuditAt = reference.ID, reference.MutationAt
			return s.invitations.RecordBatchDuplicate(ctx, &input, commandIdempotency)
		},
		invitationStoreErrors.Translate,
	)
}

func (s *Service) recordDuplicateInvitationView(
	ctx context.Context,
	commandIdempotency *store.CommandIdempotency,
	attempt access.Mutation,
	input store.InvitationBatchDuplicate,
	canonicalKey string,
) (InvitationView, error) {
	if commandIdempotency == nil || canonicalKey == "" {
		return InvitationView{}, apperror.New("request.invalid")
	}

	input.ActorUserID = attempt.Invocation.Principal().UserID
	input.CanonicalOperation = commandIdempotency.Operation
	input.CanonicalKeyDigest = sha256.Sum256([]byte(canonicalKey))
	result, err := s.recordInvitationCommandDuplicate(ctx, commandIdempotency, attempt, input)
	if err != nil {
		return InvitationView{}, err
	}
	if result.Duplicate {
		return InvitationView{}, apperror.New("onboarding_batch.duplicate")
	}
	if !result.InvitationID.IsValid() {
		return InvitationView{}, apperror.New("invitation.unavailable")
	}

	return InvitationView{ID: result.InvitationID, Replayed: true}, nil
}

func (s *Service) recoverInvitationIssueOutcome(
	ctx context.Context,
	commandIdempotency *store.CommandIdempotency,
	attempt access.Mutation,
) (InvitationView, bool, error) {
	if commandIdempotency == nil {
		return InvitationView{}, false, nil
	}

	found, err := s.invitations.FindCommandOutcome(ctx, commandIdempotency)
	if store.IsNotFound(err) {
		return InvitationView{}, false, nil
	}
	if err == nil && found.Invitation != nil {
		attempt.Value = found.Invitation.Auditable()
	}

	replayed, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationCommandResult, error) {
			value, replayErr := s.invitations.ReplayIssue(
				ctx,
				&store.InvitationReplay{AuditEventID: reference.ID, AuditAt: reference.MutationAt},
				commandIdempotency,
			)
			if replayErr == nil && value.Duplicate {
				return nil, apperror.New("onboarding_batch.duplicate")
			}

			return value, replayErr
		},
		invitationStoreErrors.Translate,
	)
	if err != nil {
		return InvitationView{}, true, err
	}

	view := invitationView(replayed.Invitation)
	view.Replayed = true
	return view, true, nil
}

func (s *Service) recoverInvitationAdministrationOutcome(
	ctx context.Context,
	commandIdempotency *store.CommandIdempotency,
	attempt access.Mutation,
) (InvitationAdministrationView, bool, error) {
	if commandIdempotency == nil {
		return InvitationAdministrationView{}, false, nil
	}

	found, err := s.invitations.FindCommandOutcome(ctx, commandIdempotency)
	if store.IsNotFound(err) {
		return InvitationAdministrationView{}, false, nil
	}
	if err == nil && found.Invitation != nil {
		attempt.Value = found.Invitation.Auditable()
	}

	replayed, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationAdministrationCommandResult, error) {
			value, replayErr := s.invitations.ReplayAdministration(
				ctx,
				&store.InvitationReplay{AuditEventID: reference.ID, AuditAt: reference.MutationAt},
				commandIdempotency,
			)
			if replayErr == nil && value.Duplicate {
				return nil, apperror.New("onboarding_batch.duplicate")
			}

			return value, replayErr
		},
		invitationStoreErrors.Translate,
	)
	if err != nil {
		return InvitationAdministrationView{}, true, err
	}

	view := invitationAdministrationView(replayed.Record)
	view.Replayed = true
	return view, true, nil
}

// InvitationBatchErrorCodes is the closed catalogue of safe failure codes an
// Invitation batch item reports. Any other failure is reported as
// invitation.unavailable.
func InvitationBatchErrorCodes() []string {
	return []string{
		"request.invalid",
		"resource.not_found",
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"audit.unavailable",
		"administration.unavailable",
		"authentication.token_invalid",
		"authentication.strong_required",
		"authentication.reauthentication_required",
		"invitation.invalid",
		"invitation.class_period_conflict",
		"invitation.conflict",
		"invitation.role_not_delegable",
		"invitation.mail_unavailable",
		"invitation.unavailable",
		"idempotency.conflict",
		"idempotency.in_progress",
		"onboarding_batch.duplicate",
	}
}

// invitationRowProgressionErrorCodes are the further codes an onboarding
// import row keeps when its frozen progression target changed; Invitation use
// cases never mint them, so a batch item cannot report them.
var invitationRowProgressionErrorCodes = []string{"student_progression.conflict", "student_progression.target_conflict"}

// invitationRowErrorCodes are the codes an onboarding import row of an
// Invitation batch may record: the batch catalogue plus the progression
// target codes.
func invitationRowErrorCodes() []string {
	return append(InvitationBatchErrorCodes(), invitationRowProgressionErrorCodes...)
}

func invitationBatchPublicErrorCode(err error) string {
	if failure, ok := apperror.As(err); ok &&
		(slices.Contains(InvitationBatchErrorCodes(), failure.Code()) || slices.Contains(invitationRowProgressionErrorCodes, failure.Code())) {
		return failure.Code()
	}
	return "invitation.unavailable"
}

func invitationPurposeRequiresInteractiveBatch(purpose model.InvitationPurpose) bool {
	return purpose == model.InvitationPurposeAcademicUnitRole || purpose == model.InvitationPurposeInstitutionRole
}
