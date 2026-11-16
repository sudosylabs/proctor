// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/operational"
)

func TestPublicFailuresConcealDenialAndAbsence(t *testing.T) {
	t.Parallel()
	want := apperror.Concealment{Unavailable: "exam.sitting.unavailable", Concealed: []apperror.Code{"exam.sitting.not_found"}}
	require.Equal(t, want, publicFailures, "publicFailures")
	for _, code := range append([]apperror.Code{"authorization.denied"}, want.Concealed...) {
		cause := apperror.New(code)
		err := publicFailures.Conceal(cause)
		require.True(t, apperror.Is(err, "resource.not_found"), "Conceal(%s)", code)
		require.ErrorIs(t, err, cause, "Conceal(%s)", code)
	}
	safe := apperror.Invalid("request.invalid", "field")
	err := publicFailures.Conceal(safe)
	require.True(t, apperror.Is(err, "request.invalid"), "Conceal(request.invalid)")
	require.ErrorIs(t, err, safe, "Conceal(request.invalid)")
	dependency := errors.New("dependency failed")
	err = publicFailures.Conceal(dependency)
	require.True(t, apperror.Is(err, want.Unavailable), "Conceal(dependency) = %v, want wrapped %s", err, want.Unavailable)
	require.ErrorIs(t, err, dependency, "Conceal(dependency) = %v, want wrapped %s", err, want.Unavailable)
}

type operationalRecorderFake struct{ events []operational.Event }

func (f *operationalRecorderFake) RecordOperationalEvent(event operational.Event) {
	f.events = append(f.events, event)
}

func TestObservedUseCaseRecordsItsConcealedOutcome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	recorder := &operationalRecorderFake{}
	f.service.operational = recorder
	_, err := f.service.PauseExamSitting(t.Context(), f.stranger, ManagerTransitionCommand{
		ExamID: f.examID, SittingID: f.sittingID, ExpectedRevision: 1,
		PrivateReason: "Candidate requested clarification", IdempotencyKey: "test-key",
	})
	require.True(t, apperror.Is(err, "resource.not_found"), "PauseExamSitting error = %v, want concealed resource.not_found", err)
	require.Len(t, recorder.events, 1, "recorded events")
	recorded := recorder.events[0]
	require.Equal(t, "exam_sitting", recorded.Subsystem(), "recorded subsystem")
	require.Equal(t, "pause", recorded.Event(), "recorded event")
	require.Equal(t, "not_found", recorded.Outcome(), "recorded outcome, want the concealed resource.not_found")
}
