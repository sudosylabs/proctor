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
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Service) previewProgression(ctx context.Context, id model.OnboardingImportID) error {
	current, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return err
	}

	if current.Mode == model.OnboardingImportStudentProgression &&
		(current.State == model.OnboardingImportPreviewReady ||
			current.State == model.OnboardingImportCanceled ||
			current.State == model.OnboardingImportFailed) {
		return nil
	}

	if current.Mode != model.OnboardingImportStudentProgression || current.State != model.OnboardingImportParsing {
		return store.NewErrConflict("student_progression", "state", nil)
	}

	invocation := invocation.New(current.Principal, model.RequestMetadata{})
	if err = s.validatePrincipal(ctx, current.Principal); err != nil {
		return err
	}

	for _, classID := range []model.ClassID{current.SourceClassID, current.DestinationClassID} {
		resource := model.Resource{Type: model.ResourceClass, ID: classID.String()}
		for _, action := range []model.Action{model.ActionAcademicProgressionManage, model.ActionClassMembersManage} {
			if err = s.authorize(ctx, invocation, action, resource); err != nil {
				return err
			}
		}
	}

	sourcePeriod, err := s.periods.Get(ctx, current.SourcePeriodID)
	if err != nil {
		return err
	}
	if sourcePeriod.Revision != current.SourcePeriodRevision {
		return apperror.New("student_progression.target_conflict")
	}

	destinationPeriod, err := s.periods.Get(ctx, current.DestinationPeriodID)
	if err != nil {
		return err
	}
	if destinationPeriod.Revision != current.DestinationPeriodRevision {
		return apperror.New("student_progression.target_conflict")
	}

	sourceClass, err := s.classes.Get(ctx, current.SourceClassID)
	if err != nil {
		return err
	}
	if sourceClass.Revision != current.SourceClassRevision {
		return apperror.New("student_progression.target_conflict")
	}

	destinationClass, err := s.classes.Get(ctx, current.DestinationClassID)
	if err != nil {
		return err
	}
	if destinationClass.Revision != current.DestinationClassRevision {
		return apperror.New("student_progression.target_conflict")
	}

	rosterAt := studentProgressionRosterInstant(current, sourcePeriod)
	members, err := s.imports.ListStudentProgressionRoster(ctx, sourceClass.ID, rosterAt, store.OnboardingImportMaximumRows+1)
	if err != nil {
		return err
	}
	if len(members) > store.OnboardingImportMaximumRows {
		return apperror.New("student_progression.roster_too_large")
	}

	rows := make([]store.OnboardingImportRow, 0, len(members))
	for index, member := range members {
		row, rowErr := s.previewProgressionRow(ctx, id, current, destinationClass, index, member)
		if rowErr != nil {
			return rowErr
		}

		rows = append(rows, row)
	}

	document, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if err = requireOnboardingImportPreviewCodes(rows); err != nil {
		return err
	}

	digest := sha256.Sum256(document)
	_, err = s.imports.CompleteOnboardingImportPreview(ctx, &store.OnboardingImportPreviewCompletion{
		ID:               id,
		ExpectedRevision: current.Revision,
		Digest:           hex.EncodeToString(digest[:]),
		Rows:             rows,
		At:               model.TimeUTC(s.now()),
	})
	return err
}

// previewProgressionRow builds the preview row for one source roster member, classifying the member's eligibility
// and any existing destination membership.
func (s *Service) previewProgressionRow(
	ctx context.Context,
	id model.OnboardingImportID,
	current *store.OnboardingImport,
	destinationClass *model.Class,
	index int,
	member *model.ClassMember,
) (store.OnboardingImportRow, error) {
	row := store.OnboardingImportRow{
		ImportID:             id,
		RowNumber:            index + 1,
		Reference:            member.ID.String(),
		ScopeType:            model.RoleScopeClass,
		ScopeID:              destinationClass.ID.String(),
		TargetRevision:       destinationClass.Revision,
		UserID:               member.UserID,
		RelationshipID:       member.ID.String(),
		RelationshipRevision: member.Revision,
		StartsAt:             model.OptionalTimeFrom(current.EffectiveAt),
		PreviewStatus:        model.OnboardingImportRowValid,
		Status:               model.OnboardingImportRowValid,
		UpdatedAt:            current.CreatedAt,
	}
	if current.SourcePeriodID == current.DestinationPeriodID {
		row.Operation = string(AcademicAdministrationClassTransfer)
		if progressionTransferDateConflict(member, current.EffectiveAt) {
			row.PreviewStatus, row.Status, row.PreviewCode =
				model.OnboardingImportRowInvalid,
				model.OnboardingImportRowInvalid,
				"student_progression.effective_date_conflict"
			return row, nil
		}
	} else {
		row.Operation = string(AcademicAdministrationClassEnroll)
	}

	user, userErr := s.users.Get(ctx, member.UserID)
	if userErr != nil {
		return store.OnboardingImportRow{}, userErr
	}

	if !user.IsActive() {
		row.PreviewStatus, row.Status, row.PreviewCode =
			model.OnboardingImportRowInvalid,
			model.OnboardingImportRowInvalid,
			"student_progression.ineligible_target"
		return row, nil
	}

	affiliations, affiliationErr := s.affiliations.ListActiveByUser(ctx, member.UserID, current.EffectiveAt)
	if affiliationErr != nil {
		return store.OnboardingImportRow{}, affiliationErr
	}

	if !studentProgressionEligibleAffiliation(affiliations) {
		row.PreviewStatus, row.Status, row.PreviewCode =
			model.OnboardingImportRowInvalid,
			model.OnboardingImportRowInvalid,
			"student_progression.ineligible_target"
		return row, nil
	}

	active, activeErr := s.classMembers.ListActiveByUser(ctx, member.UserID, current.EffectiveAt)
	if activeErr != nil {
		return store.OnboardingImportRow{}, activeErr
	}

	for _, existing := range active {
		if existing.AcademicPeriodID != current.DestinationPeriodID {
			continue
		}

		if existing.ClassID == current.DestinationClassID {
			row.Operation = string(AcademicAdministrationClassEnroll)
			row.PreviewCode = "student_progression.destination_exists"
			row.DestinationRelationshipID = existing.ID.String()
			row.DestinationRelationshipRevision = existing.Revision
		} else if existing.ID != member.ID {
			row.PreviewStatus, row.Status, row.PreviewCode =
				model.OnboardingImportRowInvalid,
				model.OnboardingImportRowInvalid,
				"student_progression.destination_conflict"
		}

		break
	}

	return row, nil
}

func progressionTransferDateConflict(member *model.ClassMember, effectiveAt time.Time) bool {
	return member == nil || member.EndsAt.Valid || !effectiveAt.After(member.StartsAt)
}

func studentProgressionEligibleAffiliation(affiliations []*model.Affiliation) bool {
	for _, affiliation := range affiliations {
		if affiliation != nil && affiliation.Kind == model.AffiliationStudent && !affiliation.EndsAt.Valid {
			return true
		}
	}
	return false
}

func studentProgressionRosterInstant(value *store.OnboardingImport, sourcePeriod *model.AcademicPeriod) time.Time {
	if value != nil && sourcePeriod != nil && value.SourcePeriodID == value.DestinationPeriodID {
		return value.EffectiveAt
	}
	if sourcePeriod == nil {
		return time.Time{}
	}
	return sourcePeriod.EndsAt.Add(-time.Millisecond)
}
