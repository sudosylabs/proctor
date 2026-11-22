// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import "github.com/sudosylabs/proctor/server/model"

// sameMailRecipient reports whether the locked User still matches the one a
// mail was prepared for, in everything the mail was written from: its address,
// name, language and time zone, and whether the account may receive it.
//
// It deliberately ignores the User's revision. A profile picture change, such
// as the default picture attached just after an account is created, moves the
// revision without changing a word of the mail, and must not refuse it.
func sameMailRecipient(current, prepared *model.User) bool {
	return current != nil &&
		prepared != nil &&
		current.ID == prepared.ID &&
		current.Email == prepared.Email &&
		current.EmailVerified == prepared.EmailVerified &&
		current.DisplayName == prepared.DisplayName &&
		current.Locale == prepared.Locale &&
		current.Timezone == prepared.Timezone &&
		current.ArchivedAt == prepared.ArchivedAt &&
		current.DisabledAt == prepared.DisabledAt
}
