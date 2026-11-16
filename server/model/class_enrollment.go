// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

// ClassEnrollment is the membership an enrollment or transfer created and,
// when one existed, the prior membership the same transaction closed.
type ClassEnrollment struct {
	Membership *ClassMember
	Previous   *ClassMember
}

func (e *ClassEnrollment) Auditable() map[string]any {
	result := map[string]any{}
	if e != nil && e.Membership != nil {
		result["membership"] = e.Membership.Auditable()
	}
	if e != nil && e.Previous != nil {
		result["previous"] = e.Previous.Auditable()
	}
	return result
}
