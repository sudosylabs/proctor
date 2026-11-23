// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// classMembershipSittingPage is how many sittings of one class a membership
// change reads at a time.
const classMembershipSittingPage = 200

func (s *Service) publishAcademicUnitCreated(ctx context.Context, unit *model.AcademicUnit) error {
	return s.publishAcademicUnit(ctx, realtime.NewAcademicUnitCreatedEvent, unit)
}

func (s *Service) publishAcademicUnitUpdated(ctx context.Context, unit *model.AcademicUnit) error {
	return s.publishAcademicUnit(ctx, realtime.NewAcademicUnitUpdatedEvent, unit)
}

func (s *Service) publishAcademicUnitArchived(ctx context.Context, unit *model.AcademicUnit) error {
	return s.publishAcademicUnit(ctx, realtime.NewAcademicUnitArchivedEvent, unit)
}

// publishAcademicUnit tells everyone who may view the unit that it changed,
// naming it and its parent.
func (s *Service) publishAcademicUnit(
	ctx context.Context,
	newEvent func(model.AcademicUnitID, model.AcademicUnitID) (realtime.Event, error),
	unit *model.AcademicUnit,
) error {
	if unit == nil {
		return errors.New("Academic Unit event requires the unit")
	}
	event, err := newEvent(unit.ID, unit.ParentID)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}

// publishClassesChanged reads each Class's unit and reports a failed
// publication without failing the use case.
func (s *Service) publishClassesChanged(ctx context.Context, classIDs ...model.ClassID) {
	if s.publisher == nil {
		return
	}
	if err := realtime.PublishClassesChanged(ctx, s.publisher, s.classes, classIDs...); err != nil {
		s.publisher.Report(ctx, "class.changed", err)
	}
}

// publishClassChanged reports a failed publication without failing the use
// case.
func (s *Service) publishClassChanged(ctx context.Context, classID model.ClassID, unitID model.AcademicUnitID) {
	if s.publisher == nil {
		return
	}
	event, err := realtime.NewClassChangedEvent(classID, unitID)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, "class.changed", err)
	}
}

// publishAcademicPeriodChanged reports a failed publication without failing
// the use case.
func (s *Service) publishAcademicPeriodChanged(ctx context.Context, period *model.AcademicPeriod) {
	if period == nil || s.publisher == nil {
		return
	}
	event, err := realtime.NewAcademicPeriodChangedEvent(period.ID, period.Owner)
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, "academic_period.changed", err)
	}
}

// publishUserChanged reports a failed publication without failing the use
// case.
func (s *Service) publishUserChanged(ctx context.Context, userID model.UserID) {
	if s.publisher == nil {
		return
	}
	if err := realtime.PublishUsersChanged(ctx, s.publisher, userID); err != nil {
		s.publisher.Report(ctx, "user.changed", err)
	}
}

// PublishClassMembershipChanged invalidates the candidate's socket permissions,
// tells the candidate's viewers and each class's viewers to refetch them,
// then tells the managers of every sitting
// of each class to refresh their board. One failed publication never stops
// the rest; failures are returned joined.
//
// It is a function rather than a Service method because invitation
// acceptance in app/onboarding grants a class membership through its own
// store operation and owes the same publications.
func PublishClassMembershipChanged(
	ctx context.Context,
	publisher realtime.Publisher,
	sittings store.ExamSittingStore,
	classes store.ClassStore,
	candidateID model.UserID,
	classIDs []model.ClassID,
) error {
	if sittings == nil || publisher == nil || classes == nil {
		return errors.New("Class Membership realtime dependencies are invalid")
	}

	if !candidateID.IsValid() {
		return errors.New("Class Membership realtime requires a valid user")
	}

	publisher.InvalidateAuthorization(ctx, candidateID)
	errs := []error{
		realtime.PublishUsersChanged(ctx, publisher, candidateID),
		realtime.PublishClassesChanged(ctx, publisher, classes, classIDs...)}
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
			targets, listErr := sittings.ListInvalidationTargetsByClass(ctx, classID, after, classMembershipSittingPage)
			if listErr != nil {
				errs = append(errs, listErr)
				break
			}

			for _, target := range targets {
				errs = append(errs, realtime.PublishSittingBoardChanged(ctx, publisher, target.ExamID, target.SittingID))
			}

			if len(targets) < classMembershipSittingPage {
				break
			}

			after = targets[len(targets)-1].SittingID
		}
	}

	return errors.Join(errs...)
}
