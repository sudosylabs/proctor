// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (r *Records) authorizeScope(
	ctx context.Context,
	inv invocation.Invocation,
	scope model.RetentionHoldScope,
	ordinary, override model.Action,
	excludeSelf bool,
) (*store.ExamRecordsScope, model.Action, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess || scope.Validate() != nil {
		return nil, "", apperror.Invalid("exam.invalid", "scope")
	}

	resolved, err := r.records.Resolve(ctx, scope)
	if err != nil {
		return nil, "", examRecordsStoreErrors.Translate(err)
	}

	if excludeSelf && principal.UserID == resolved.CandidateUserID {
		if err = r.authorizer.RecordDenial(ctx, inv, ordinary, scope.Resource(), resolved.AcademicUnitID); err != nil {
			return nil, "", err
		}
		return nil, "", apperror.New("exam.not_found")
	}

	action := ordinary
	if override != "" {
		examAccess, accessErr := r.exams.Access(ctx, scope.ExamID, principal.UserID)
		if accessErr != nil {
			return nil, "", examRecordsStoreErrors.Translate(accessErr)
		}

		action, err = manageraccess.SelectAction(
			ctx,
			r.academicUnitMembers,
			principal.UserID,
			examAccess,
			model.TimeUTC(r.now()),
			ordinary,
			override,
		)
		if err != nil {
			return nil, "", apperror.New("exam.unavailable").Wrap(err)
		}
	}

	if err = r.authorizer.Authorize(ctx, inv, action, scope.Resource()); err != nil {
		return nil, "", err
	}

	return resolved, action, nil
}

func (r *Records) beginMutation(
	ctx context.Context,
	inv invocation.Invocation,
	scope *store.ExamRecordsScope,
	action model.Action,
	operation string,
	data map[string]any,
) (store.ExamRecordsMutation, error) {
	data["exam_id"] = scope.Scope.ExamID.String()
	if scope.Scope.SittingID.IsValid() {
		data["exam_sitting_id"] = scope.Scope.SittingID.String()
	}
	if scope.Scope.SubmissionID.IsValid() {
		data["submission_id"] = scope.Scope.SubmissionID.String()
	}

	id, err := r.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   scope.Scope.Resource(),
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    scope.AcademicUnitID.String(),
		Operation:  operation,
		Value:      data,
	})
	if err != nil {
		return store.ExamRecordsMutation{}, err
	}

	return store.ExamRecordsMutation{
		Scope:        scope.Scope,
		Principal:    inv.Principal(),
		Action:       action,
		AuditEventID: id,
		AuditAt:      model.TimeUTC(r.now()),
	}, nil
}

func (r *Records) failMutation(ctx context.Context, mutation store.ExamRecordsMutation, err error) error {
	mapped := examRecordsStoreErrors.Translate(err)
	if auditErr := r.auditor.Fail(ctx, mutation.AuditEventID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
		return errors.Join(mapped, auditErr)
	}
	return mapped
}
