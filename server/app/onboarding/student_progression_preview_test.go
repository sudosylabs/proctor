// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestStudentProgressionPreviewRecoversUnknownCommittedCompletion(t *testing.T) {
	t.Parallel()
	id := model.NewOnboardingImportID()
	service := &Service{imports: onboardingImportPersistenceFake{value: &store.OnboardingImport{
		ID: id, Mode: model.OnboardingImportStudentProgression, State: model.OnboardingImportPreviewReady,
	}}}
	require.NoError(t, service.previewProgression(context.Background(), id))
}

func TestStudentProgressionRosterInstantUsesEffectiveTimeForSamePeriod(t *testing.T) {
	t.Parallel()
	periodID := model.NewAcademicPeriodID()
	effectiveAt := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	period := &model.AcademicPeriod{ID: periodID, EndsAt: effectiveAt.AddDate(0, 2, 0)}
	value := &store.OnboardingImport{SourcePeriodID: periodID, DestinationPeriodID: periodID, EffectiveAt: effectiveAt}
	got := studentProgressionRosterInstant(value, period)
	require.True(t, got.Equal(effectiveAt), "same-Period roster instant = %s", got)
	value.DestinationPeriodID = model.NewAcademicPeriodID()
	got, want := studentProgressionRosterInstant(value, period), period.EndsAt.Add(-time.Millisecond)
	require.True(t, got.Equal(want), "cross-Period roster instant = %s", got)
}

func TestStudentProgressionPreviewRejectsUnexecutableTransferAndMissingAffiliation(t *testing.T) {
	t.Parallel()
	effectiveAt := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	member := &model.ClassMember{StartsAt: effectiveAt}
	require.True(t, progressionTransferDateConflict(member, effectiveAt), "transfer beginning at the effective instant was accepted")
	member.StartsAt = effectiveAt.Add(-time.Hour)
	member.EndsAt = model.OptionalTimeFrom(effectiveAt.Add(time.Hour))
	require.True(t, progressionTransferDateConflict(member, effectiveAt), "bounded source transfer was accepted")
	member.EndsAt = model.OptionalTime{}
	require.False(t, progressionTransferDateConflict(member, effectiveAt), "open earlier source transfer was rejected")
	require.False(t, studentProgressionEligibleAffiliation([]*model.Affiliation{{Kind: model.AffiliationStaff}}), "non-Student affiliation was accepted")
	require.True(t, studentProgressionEligibleAffiliation([]*model.Affiliation{{Kind: model.AffiliationStudent}}), "Student affiliation was rejected")
	require.False(t, studentProgressionEligibleAffiliation([]*model.Affiliation{{Kind: model.AffiliationStudent, EndsAt: model.OptionalTimeFrom(effectiveAt.Add(time.Hour))}}), "bounded Student affiliation was accepted")
}
