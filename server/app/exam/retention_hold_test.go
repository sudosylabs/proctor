// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestRecordsHoldReleaseRequiresStrongRecentProof(t *testing.T) {
	for _, tc := range []struct {
		name           string
		strong, recent bool
		want           string
	}{
		{"single factor", false, true, "authentication.strong_required"},
		{"stale strong proof", true, false, "authentication.reauthentication_required"},
		{"fresh strong proof", true, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, records, persistence, _ := newRecordsFixture(t)
			principal := f.inv.Principal()
			principal.AuthenticationStrength = model.AuthenticationSingleFactor
			if tc.strong {
				principal.AuthenticationStrength = model.AuthenticationMultiFactor
			}
			principal.AuthenticatedAt = fixedPublicationTime().Add(-time.Minute)
			if !tc.recent {
				principal.AuthenticatedAt = principal.AuthenticatedAt.Add(-time.Hour)
			}
			inv := invocation.New(principal, model.RequestMetadata{})
			_, err := records.ReleaseRetentionHold(context.Background(), inv, ReleaseRetentionHoldCommand{Scope: persistence.scope.Scope,
				HoldID: model.NewRetentionHoldID(), ExpectedRevision: 1, ReasonCode: "case_closed", PrivateReason: "Case resolved.", IdempotencyKey: "release-key"})
			if tc.want == "" {
				require.NoError(t, err, "release")
				require.NotNil(t, persistence.release, "release")
				require.Equal(t, 5*time.Minute, persistence.release.RecentAuthenticationTTL, "release")
				return
			}
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault, "release")
			require.Equal(t, tc.want, fault.Code(), "release")
			require.Nil(t, persistence.release, "release")
		})
	}
}
