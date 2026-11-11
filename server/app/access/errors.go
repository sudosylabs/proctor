// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import "github.com/sudosylabs/proctor/server/app/apperror"

// InvalidTokenAppError reports a Principal, credential or token that is not a
// valid caller. It is the one definition of that failure: the identity and
// onboarding areas and the parent app package return it too.
func InvalidTokenAppError() error {
	return apperror.New("authentication.token_invalid")
}

// InstitutionStoreErrors translates Institution Store failures. The academic
// Institution use cases translate through it too.
var InstitutionStoreErrors = apperror.AdministrationStoreTable("institution", "institution.conflict", "institution.invalid", nil)
