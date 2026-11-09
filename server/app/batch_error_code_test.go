// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
)

func TestBatchItemErrorCodesComeOnlyFromTheirCatalogues(t *testing.T) {
	t.Parallel()
	for _, code := range AcademicAdministrationBatchErrorCodes() {
		require.Equal(t, code, academicAdministrationBatchPublicErrorCode(apperror.New(apperror.Code(code))))
	}
	for _, code := range InvitationBatchErrorCodes() {
		require.Equal(t, code, invitationBatchPublicErrorCode(apperror.New(apperror.Code(code))))
	}
	// Onboarding import rows keep the progression conflicts they share this mapping for.
	require.Equal(t, "student_progression.conflict", invitationBatchPublicErrorCode(apperror.New("student_progression.conflict")))
	require.Equal(t, "administration.unavailable", academicAdministrationBatchPublicErrorCode(apperror.New("job.unavailable")))
	require.Equal(t, "invitation.unavailable", invitationBatchPublicErrorCode(apperror.New("job.unavailable")))
	require.Equal(t, "invitation.unavailable", invitationBatchPublicErrorCode(errors.New("private failure")))
}
