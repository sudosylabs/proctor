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

func (s *Service) publishAcademicUnitCreated(ctx context.Context, unitID model.AcademicUnitID) error {
	return s.publishAcademicUnit(ctx, realtime.NewAcademicUnitCreatedEvent, unitID)
}

func (s *Service) publishAcademicUnitUpdated(ctx context.Context, unitID model.AcademicUnitID) error {
	return s.publishAcademicUnit(ctx, realtime.NewAcademicUnitUpdatedEvent, unitID)
}

func (s *Service) publishAcademicUnitArchived(ctx context.Context, unitID model.AcademicUnitID) error {
	return s.publishAcademicUnit(ctx, realtime.NewAcademicUnitArchivedEvent, unitID)
}

// publishAcademicUnit publishes a payload-free event to everyone who may view
// the unit.
func (s *Service) publishAcademicUnit(
	ctx context.Context,
	newEvent func(model.AcademicUnitID) (realtime.Event, error),
	unitID model.AcademicUnitID,
) error {
	event, err := newEvent(unitID)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}

// PublishClassMembershipChanged tells a candidate their exam activity and
// current-user context changed, then tells the managers of every sitting of
// each class to refresh their board. One failed publication never stops the
// rest; failures are returned joined.
//
// It is a function rather than a Service method because invitation
// acceptance in app/onboarding grants a class membership through its own
// store operation and owes the same publications.
func PublishClassMembershipChanged(
	ctx context.Context,
	publisher realtime.Publisher,
	sittings store.ExamSittingStore,
	candidateID model.UserID,
	classIDs []model.ClassID,
) error {
	if sittings == nil || publisher == nil {
		return errors.New("Class Membership realtime dependencies are invalid")
	}

	candidateEvent, err := realtime.NewCandidateExamActivityChangedEvent(candidateID)
	if err != nil {
		return err
	}

	errs := []error{publisher.Publish(ctx, candidateEvent),
		publisher.InvalidateCurrentUserContext(ctx, candidateID)}
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
				event, eventErr := realtime.NewManagerSittingBoardChangedEvent(target.ExamID, target.SittingID)
				if eventErr != nil {
					errs = append(errs, eventErr)
					continue
				}

				errs = append(errs, publisher.Publish(ctx, event))
			}

			if len(targets) < classMembershipSittingPage {
				break
			}

			after = targets[len(targets)-1].SittingID
		}
	}

	return errors.Join(errs...)
}
