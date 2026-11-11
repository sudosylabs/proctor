// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type examAuthorizationAdapter struct{ authorization *access.Authorizer }

func (a examAuthorizationAdapter) Authorize(
	ctx context.Context,
	invocation invocation.Invocation,
	action model.Action,
	resource model.Resource,
) error {
	return a.authorization.Authorize(ctx, invocation, action, resource)
}

func (a examAuthorizationAdapter) AuthorizeList(
	ctx context.Context,
	invocation invocation.Invocation,
	_ model.AcademicUnitID,
) (store.ExamListVisibility, error) {
	principal := invocation.Principal()
	decisionAt := model.TimeUTC(a.authorization.Now())
	ordinary, err := a.authorization.GrantedScopesAt(ctx, principal, model.ActionExamView, model.ResourceExam, decisionAt)
	if err != nil {
		return store.ExamListVisibility{}, err
	}

	override, err := a.authorization.GrantedScopesAt(ctx, principal, model.ActionExamViewOverride, model.ResourceExam, decisionAt)
	if err != nil {
		return store.ExamListVisibility{}, err
	}

	hasOrdinary := ordinary.InstitutionWide || len(ordinary.AcademicUnitRootIDs) > 0
	hasOverride := override.InstitutionWide || len(override.AcademicUnitRootIDs) > 0
	institution, err := a.authorization.Institution(ctx)
	if err != nil {
		return store.ExamListVisibility{}, access.ResourceError("institution", err)
	}

	action := model.ActionExamView
	if hasOverride {
		action = model.ActionExamViewOverride
	}

	resource := model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}
	if err := a.authorization.RecordAuthorizationDecision(
		ctx,
		principal,
		action,
		resource,
		model.RoleScopeInstitution,
		institution.ID.String(),
		invocation.RequestMetadata(),
		hasOrdinary || hasOverride,
	); err != nil {
		return store.ExamListVisibility{}, err
	}

	if !hasOrdinary && !hasOverride {
		return store.ExamListVisibility{}, access.DeniedError()
	}

	return store.ExamListVisibility{
		ActorUserID:                 principal.UserID,
		OrdinaryMembershipAt:        decisionAt,
		OrdinaryInstitutionWide:     ordinary.InstitutionWide,
		OrdinaryAcademicUnitRootIDs: append([]model.AcademicUnitID(nil), ordinary.AcademicUnitRootIDs...),
		OverrideInstitutionWide:     override.InstitutionWide,
		OverrideAcademicUnitRootIDs: append([]model.AcademicUnitID(nil), override.AcademicUnitRootIDs...),
	}, nil
}

var _ exam.Authorizer = examAuthorizationAdapter{}
var _ exam.Auditor = access.MutationAuditAdapter{}
