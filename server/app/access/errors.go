// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package access

import "github.com/sudosylabs/proctor/server/app/apperror"

// InvalidTokenAppError is the one definition of authentication.token_invalid,
// shared by every area that rejects a caller's principal or token.
func InvalidTokenAppError() error {
	return apperror.New("authentication.token_invalid")
}

// InstitutionStoreErrors presents institution store failures; the academic
// institution use cases share it.
var InstitutionStoreErrors = apperror.AdministrationStoreTable("institution", "institution.conflict", "institution.invalid", nil)
