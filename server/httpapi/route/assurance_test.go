// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestRestrictedPrincipalAssuranceByRoute(t *testing.T) {
	// Stored instants carry microseconds; Linux clocks also report nanoseconds.
	now := model.TimeUTC(time.Now())
	base := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: now}
	enrollment, recovery := base, base
	enrollment.MFAEnrollmentRequired = true
	recovery.MFARecoveryRequired, recovery.AuthenticationGeneration = true, 1
	for _, test := range []struct {
		name        string
		principal   model.Principal
		requirement AuthRequirement
		admitted    bool
	}{
		{"ordinary session reaches the challenge", base, AuthMFAEnrollmentSessionRequired, true},
		{"enrollment reaches the challenge", enrollment, AuthMFAEnrollmentSessionRequired, true},
		{"enrollment reaches recovery routes", enrollment, AuthMFARecoverySessionRequired, true},
		{"enrollment is refused ordinary routes", enrollment, AuthSessionRequired, false},
		{"enrollment is refused principal routes", enrollment, AuthPrincipalRequired, false},
		{"recovery is refused the challenge", recovery, AuthMFAEnrollmentSessionRequired, false},
		{"recovery reaches recovery routes", recovery, AuthRecentMFARecoverySessionRequired, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := requirePrincipalAssurance(test.principal, test.requirement, now, time.Hour)
			if test.admitted {
				require.NoError(t, err)
				return
			}
			require.Equal(t, "authentication.token_invalid", applicationErrorCode(err))
		})
	}
}
