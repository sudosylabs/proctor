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

// classMembershipSittingPage is how many Sittings of one Class a Class
// Membership change reads at a time.
const classMembershipSittingPage = 200

func (s *Service) publishAcademicUnitCreated(ctx context.Context, unitID model.AcademicUnitID) error {
	return s.publishAcademicUnit(ctx, "academic_unit_created", unitID)
}

func (s *Service) publishAcademicUnitUpdated(ctx context.Context, unitID model.AcademicUnitID) error {
	return s.publishAcademicUnit(ctx, "academic_unit_updated", unitID)
}

func (s *Service) publishAcademicUnitArchived(ctx context.Context, unitID model.AcademicUnitID) error {
	return s.publishAcademicUnit(ctx, "academic_unit_archived", unitID)
}

// publishAcademicUnit publishes a payload-free event to everyone who may view
// the Academic Unit.
func (s *Service) publishAcademicUnit(ctx context.Context, name string, unitID model.AcademicUnitID) error {
	return s.publisher.PublishRealtimeEvent(ctx, realtime.RealtimeEvent{
		Name:   name,
		Action: model.ActionAcademicUnitView,
		Resource: model.Resource{
			Type: model.ResourceAcademicUnit,
			ID:   unitID.String(),
		},
	})
}

// PublishClassMembershipChanged tells a candidate that their Exam activity
// and current-user context changed, then tells the managers of every Sitting
// of each Class to refresh their Sitting board. A Class named twice is
// published once. Every failure is collected and returned joined, so one
// failed publication never stops the rest.
//
// It is a function over the two collaborators rather than a Service method
// because Invitation acceptance in app/onboarding grants a Class Membership
// through its own Store operation and owes the same publications.
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

	errs := []error{publisher.PublishRealtimeEvent(ctx, candidateEvent),
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

				errs = append(errs, publisher.PublishRealtimeEvent(ctx, event))
			}

			if len(targets) < classMembershipSittingPage {
				break
			}

			after = targets[len(targets)-1].SittingID
		}
	}

	return errors.Join(errs...)
}
