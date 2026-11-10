// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// academicRealtimeEffects publishes the realtime effects of academic changes
// through the application's realtime service. Invitation acceptance in the
// onboarding area uses its Class Membership effects as well.
type academicRealtimeEffects struct {
	sittings store.ExamSittingStore
	realtime *RealtimeService
}

func (e academicRealtimeEffects) Created(ctx context.Context, unitID model.AcademicUnitID) error {
	return e.publishAcademicUnit(ctx, "academic_unit_created", unitID.String())
}

func (e academicRealtimeEffects) Updated(ctx context.Context, unitID model.AcademicUnitID) error {
	return e.publishAcademicUnit(ctx, "academic_unit_updated", unitID.String())
}

func (e academicRealtimeEffects) Archived(ctx context.Context, unitID model.AcademicUnitID) error {
	return e.publishAcademicUnit(ctx, "academic_unit_archived", unitID.String())
}

func (e academicRealtimeEffects) publishAcademicUnit(ctx context.Context, event string, unitID string) error {
	return e.realtime.PublishRealtimeEvent(ctx, realtime.RealtimeEvent{
		Name:   event,
		Action: model.ActionAcademicUnitView,
		Resource: model.Resource{
			Type: model.ResourceAcademicUnit,
			ID:   unitID,
		},
	})
}

func (e academicRealtimeEffects) Changed(ctx context.Context, userID model.UserID) error {
	return e.realtime.InvalidateCurrentUserContext(ctx, userID)
}

func (e academicRealtimeEffects) MembershipChanged(
	ctx context.Context,
	candidateID model.UserID,
	classIDs []model.ClassID,
) error {
	if e.sittings == nil || e.realtime == nil {
		return errors.New("Class Membership realtime dependencies are invalid")
	}
	candidateEvent, err := realtime.NewCandidateExamActivityChangedEvent(candidateID)
	if err != nil {
		return err
	}
	errs := []error{e.realtime.PublishRealtimeEvent(ctx, candidateEvent),
		e.realtime.InvalidateCurrentUserContext(ctx, candidateID)}
	seen := make(map[model.ClassID]struct{}, len(classIDs))
	for _, classID := range classIDs {
		if !classID.IsValid() {
			errs = append(errs, errors.New("Class Membership invalidation requires a valid Class identity"))
			continue
		}
		if _, exists := seen[classID]; exists {
			continue
		}
		seen[classID] = struct{}{}
		var after model.ExamSittingID
		for {
			targets, listErr := e.sittings.ListInvalidationTargetsByClass(ctx, classID, after, 200)
			if listErr != nil {
				errs = append(errs, listErr)
				break
			}
			for _, target := range targets {
				event, eventErr := realtime.NewManagerSittingBoardChangedEvent(target.ExamID, target.SittingID)
				if eventErr != nil {
					errs = append(errs, eventErr)
					continue
				}
				errs = append(errs, e.realtime.PublishRealtimeEvent(ctx, event))
			}
			if len(targets) < 200 {
				break
			}
			after = targets[len(targets)-1].SittingID
		}
	}
	return errors.Join(errs...)
}

func (e academicRealtimeEffects) InvalidateAuthorization(ctx context.Context, userID model.UserID) {
	e.realtime.InvalidateAuthorization(ctx, userID)
}

func (e academicRealtimeEffects) Report(ctx context.Context, operation string, err error) {
	e.realtime.reportTransientFailure(ctx, operation, err)
}

var (
	_ academic.Effects             = academicRealtimeEffects{}
	_ onboarding.MembershipEffects = academicRealtimeEffects{}
	_ academic.Authorizer          = (*access.Authorizer)(nil)
	_ academic.Auditor             = access.MutationAuditAdapter{}
)
