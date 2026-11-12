// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Service) executeImport(ctx context.Context, id model.OnboardingImportID, startAfter int, checkpoint func(int, int) error) error {
	current, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return err
	}

	if current.State == model.OnboardingImportCompleted ||
		current.State == model.OnboardingImportCompletedWithErrors ||
		current.State == model.OnboardingImportCanceled {
		return nil
	}

	if current.State != model.OnboardingImportExecuting {
		return store.NewErrConflict("onboarding_import", "state", nil)
	}

	actorInvocation := invocation.New(current.Principal, model.RequestMetadata{})
	after, processed := startAfter, 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		latest, getErr := s.imports.GetOnboardingImport(ctx, id)
		if getErr != nil {
			return getErr
		}
		if latest.State == model.OnboardingImportCanceled {
			return nil
		}

		page, pageErr := s.imports.ListOnboardingImportRows(ctx, id, after, store.OnboardingImportPageSize)
		if pageErr != nil {
			return pageErr
		}

		// A filter preview's rows are checked against the frozen caller's
		// directory visibility, resolved once for each page it executes.
		var guard *academicAdministrationFilterRowGuard
		if current.Mode == model.OnboardingImportAcademicAdministrationFilter {
			if guard, err = s.newAcademicAdministrationFilterRowGuard(ctx, actorInvocation, current); err != nil {
				return err
			}
		}

		for _, row := range page.Rows {
			after = row.RowNumber
			if row.Status != model.OnboardingImportRowPending {
				continue
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			latest, getErr = s.imports.GetOnboardingImport(ctx, id)
			if getErr != nil {
				return getErr
			}
			if latest.State == model.OnboardingImportCanceled {
				return nil
			}
			if latest.State != model.OnboardingImportExecuting {
				return store.NewErrConflict("onboarding_import", "state", nil)
			}

			status, invitationID, resourceID, code, executeErr := s.executeImportRow(ctx, actorInvocation, latest, row, guard)
			if executeErr != nil {
				return executeErr
			}

			if status == model.OnboardingImportRowFailed {
				if err = requireOnboardingImportRowCode(code); err != nil {
					return err
				}

				if _, err = s.imports.CompleteOnboardingImportRow(ctx, &store.OnboardingImportRowCompletion{
					ID:           id,
					RowNumber:    row.RowNumber,
					Status:       status,
					InvitationID: invitationID,
					ResourceID:   resourceID,
					PublicCode:   code,
					At:           model.TimeUTC(s.now()),
				}); err != nil {
					return err
				}
			}

			processed++
			if checkpoint != nil {
				if err = checkpoint(after, processed); err != nil {
					return err
				}
			}
		}

		if !page.More {
			_, err = s.imports.FinishOnboardingImport(ctx, &store.OnboardingImportFinish{ID: id, At: model.TimeUTC(s.now())})
			return err
		}
	}
}

// executeImportRow runs one pending row. guard is the page's filter row
// guard for an academic administration filter preview and nil otherwise.
func (s *Service) executeImportRow(
	ctx context.Context,
	inv invocation.Invocation,
	current *store.OnboardingImport,
	row store.OnboardingImportRow,
	guard *academicAdministrationFilterRowGuard,
) (model.OnboardingImportRowStatus, model.InvitationID, string, string, error) {
	if err := s.validatePrincipal(ctx, inv.Principal()); err != nil {
		if onboardingImportRetryableError(err) {
			return "", "", "", "", err
		}
		return model.OnboardingImportRowFailed, "", "", "authentication.token_invalid", nil
	}

	filterMode := current != nil && current.Mode == model.OnboardingImportAcademicAdministrationFilter
	if filterMode {
		// Every failure of the check is a dependency failure, so the row is
		// retried rather than recorded.
		code, err := s.checkAcademicAdministrationFilterRow(ctx, current, guard, row)
		if err != nil {
			return "", "", "", "", err
		}
		if code != "" {
			return model.OnboardingImportRowFailed, "", "", code, nil
		}
	}

	if current == nil || current.Mode != model.OnboardingImportStudentProgression {
		if err := s.revalidateFrozenTarget(ctx, row); err != nil {
			if onboardingImportRetryableError(err) {
				return "", "", "", "", err
			}
			return model.OnboardingImportRowFailed, "", "", invitationBatchPublicErrorCode(err), nil
		}
	}

	if operation := AcademicAdministrationBatchOperation(row.Operation); operation.IsValid() {
		relationshipID := row.RelationshipID
		progression := current != nil &&
			current.Mode == model.OnboardingImportStudentProgression &&
			(operation == AcademicAdministrationClassEnroll || operation == AcademicAdministrationClassTransfer)
		if progression && operation == AcademicAdministrationClassEnroll {
			relationshipID = ""
		}

		batchItem := AcademicAdministrationBatchItemCommand{
			IdempotencyKey:  strconv.Itoa(row.RowNumber),
			UserID:          row.UserID,
			RelationshipID:  relationshipID,
			RoleID:          row.RoleID,
			AffiliationKind: row.AffiliationKind,
			StartAt:         row.StartsAt.Millis(),
			EndAt:           row.EndsAt.Millis(),
		}
		if filterMode {
			batchItem = academicAdministrationFilterRowItem(row)
		}

		run := func(ctx context.Context, sourceAuditID, destinationAuditID model.AuditEventID) (AcademicAdministrationBatchResult, error) {
			if progression {
				if err := s.authorizeImport(ctx, inv, current, true); err != nil {
					return AcademicAdministrationBatchResult{}, err
				}
			}

			return s.RunAcademicAdministrationBatch(ctx, inv, RunAcademicAdministrationBatchCommand{
				Operation:                     operation,
				ScopeType:                     row.ScopeType,
				ScopeID:                       row.ScopeID,
				IdempotencyKey:                row.ImportID.String(),
				onboardingImportID:            row.ImportID,
				onboardingImportRowNumber:     row.RowNumber,
				progression:                   progression,
				progressionSourceAuditID:      sourceAuditID,
				progressionDestinationAuditID: destinationAuditID,
				Items:                         []AcademicAdministrationBatchItemCommand{batchItem},
			})
		}
		var result AcademicAdministrationBatchResult
		var runErr error
		if progression {
			result, runErr = runProgressionAudited(
				ctx,
				s.auditor,
				inv,
				"student_progression.execute_row",
				row.ImportID.String(),
				current.SourceClassID,
				current.DestinationClassID,
				model.TimeUTC(s.now()),
				func(ctx context.Context, destinationReference, sourceReference access.MutationReference) (AcademicAdministrationBatchResult, error) {
					return run(ctx, sourceReference.ID, destinationReference.ID)
				},
				func(err error) error { return err },
			)
		} else {
			result, runErr = run(ctx, "", "")
		}

		if runErr != nil || len(result.Items) != 1 {
			if runErr != nil && onboardingImportRetryableError(runErr) {
				return "", "", "", "", runErr
			}
			return model.OnboardingImportRowFailed, "", "", academicAdministrationBatchPublicErrorCode(runErr), nil
		}

		item := result.Items[0]
		if item.Status == InvitationBatchItemSucceeded {
			return model.OnboardingImportRowSucceeded, "", item.ResourceID, "", nil
		}
		if item.Status == InvitationBatchItemNoOp {
			return model.OnboardingImportRowNoOp, "", item.ResourceID, "", nil
		}
		if onboardingImportRetryablePublicCode(item.ErrorCode) {
			return "", "", "", "", apperror.New(onboardingImportRetryableCodes[item.ErrorCode])
		}

		return model.OnboardingImportRowFailed, "", "", item.ErrorCode, nil
	}

	result, err := s.RunInvitationBatch(ctx, inv, RunInvitationBatchCommand{
		Operation:                 InvitationBatchOperation(row.Operation),
		ScopeType:                 row.ScopeType,
		ScopeID:                   row.ScopeID,
		onboardingImportID:        row.ImportID,
		onboardingImportRowNumber: row.RowNumber,
		IdempotencyKey:            row.ImportID.String(),
		Items: []InvitationBatchItemCommand{{
			IdempotencyKey:       strconv.Itoa(row.RowNumber),
			TargetEmail:          row.Email,
			RoleID:               row.RoleID,
			IntendedStartsAt:     row.StartsAt.Millis(),
			IntendedEndsAt:       row.EndsAt.Millis(),
			SuggestedUsername:    row.Username,
			SuggestedDisplayName: row.DisplayName,
			SuggestedFirstName:   row.FirstName,
			SuggestedLastName:    row.LastName,
			SuggestedLocale:      row.Locale,
			SuggestedTimezone:    row.Timezone,
		}},
	})
	if err != nil || len(result.Items) != 1 {
		if err != nil && onboardingImportRetryableError(err) {
			return "", "", "", "", err
		}
		return model.OnboardingImportRowFailed, "", "", invitationBatchPublicErrorCode(err), nil
	}

	item := result.Items[0]
	if item.Status == InvitationBatchItemSucceeded {
		return model.OnboardingImportRowSucceeded, item.InvitationID, "", "", nil
	}
	if item.Status == InvitationBatchItemNoOp {
		return model.OnboardingImportRowNoOp, item.InvitationID, "", "", nil
	}
	if onboardingImportRetryablePublicCode(item.ErrorCode) {
		return "", "", "", "", apperror.New(onboardingImportRetryableCodes[item.ErrorCode])
	}

	return model.OnboardingImportRowFailed, "", "", item.ErrorCode, nil
}

func onboardingImportRetryableError(err error) bool {
	appErr, ok := apperror.As(err)
	return ok && onboardingImportRetryablePublicCode(appErr.Code())
}

// onboardingImportRetryableCodes are the public failure codes after which a
// row is retried instead of recorded as failed. A recorded batch item code is
// re-presented through this table so every presented code stays a constant.
var onboardingImportRetryableCodes = map[string]apperror.Code{
	"administration.unavailable":    "administration.unavailable",
	"audit.unavailable":             "audit.unavailable",
	"authentication.internal":       "authentication.internal",
	"authorization.unavailable":     "authorization.unavailable",
	"idempotency.in_progress":       "idempotency.in_progress",
	"invitation.mail_unavailable":   "invitation.mail_unavailable",
	"invitation.unavailable":        "invitation.unavailable",
	"mail.unavailable":              "mail.unavailable",
	"onboarding_import.unavailable": "onboarding_import.unavailable",
}

func onboardingImportRetryablePublicCode(code string) bool {
	_, ok := onboardingImportRetryableCodes[code]
	return ok
}

func (s *Service) revalidateFrozenTarget(ctx context.Context, row store.OnboardingImportRow) error {
	if operation := AcademicAdministrationBatchOperation(row.Operation); operation.IsValid() {
		_, revision, err := s.academicAdministrationImportScope(ctx, row.ScopeType, row.ScopeID)
		if err != nil {
			return err
		}
		if revision != row.TargetRevision {
			return apperror.New("onboarding_import.conflict")
		}

		if row.RelationshipRevision > 0 {
			relationship, relationshipErr := s.classMembers.Get(ctx, model.ClassMemberID(row.RelationshipID))
			if relationshipErr != nil {
				return academic.ClassMemberStoreErrors.Translate(relationshipErr)
			}
			if relationship.Revision != row.RelationshipRevision || relationship.UserID != row.UserID {
				return apperror.New("student_progression.conflict")
			}
		}

		if operation == AcademicAdministrationRoleBindingCreate {
			role, roleErr := s.roles.Get(ctx, row.RoleID)
			if roleErr != nil {
				return administration.RoleBindingStoreErrors.Translate(roleErr)
			}
			if role.UpdatedAt.UnixMicro() != row.RoleRevision || role.IsArchived() {
				return apperror.New("onboarding_import.conflict")
			}
		}

		return nil
	}

	switch InvitationBatchOperation(row.Operation) {
	case InvitationBatchStudentClassCreate:
		value, err := s.classes.Get(ctx, model.ClassID(row.ScopeID))
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}
		if value.Revision != row.TargetRevision || value.IsArchived() {
			return apperror.New("invitation.conflict")
		}
	case InvitationBatchTeacherAcademicUnitCreate, InvitationBatchAcademicUnitRoleCreate:
		value, err := s.academicUnits.Get(ctx, model.AcademicUnitID(row.ScopeID))
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}
		if value.Revision != row.TargetRevision || value.IsArchived() {
			return apperror.New("invitation.conflict")
		}

		role, err := s.roles.Get(ctx, row.RoleID)
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}
		if role.UpdatedAt.UnixMicro() != row.RoleRevision || role.IsArchived() {
			return apperror.New("invitation.conflict")
		}
	case InvitationBatchInstitutionRoleCreate:
		value, err := s.institutions.Get(ctx, model.InstitutionID(row.ScopeID))
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}
		if value.Revision != row.TargetRevision || value.IsArchived() {
			return apperror.New("invitation.conflict")
		}

		role, err := s.roles.Get(ctx, row.RoleID)
		if err != nil {
			return invitationStoreErrors.Translate(err)
		}
		if role.UpdatedAt.UnixMicro() != row.RoleRevision || role.IsArchived() {
			return apperror.New("invitation.conflict")
		}
	default:
		return apperror.New("request.invalid")
	}

	return nil
}
