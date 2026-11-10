// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

// RequireStrongRecentSession is the application-layer assurance guard for
// sensitive administrative operations. Transport declarations remain an
// early rejection only; every owning use case calls this guard itself.
func RequireStrongRecentSession(
	principal model.Principal,
	now time.Time,
	recentAuthenticationTTL time.Duration,
) error {
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return access.InvalidTokenAppError()
	}
	if !principal.HasStrongAuthentication() {
		return apperror.New("authentication.strong_required")
	}
	if !principal.IsRecentlyAuthenticated(now, recentAuthenticationTTL) {
		return apperror.New("authentication.reauthentication_required")
	}
	return nil
}
