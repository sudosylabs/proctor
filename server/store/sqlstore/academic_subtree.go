// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
)

// academicSubtreeCTE returns a WITH RECURSIVE body naming every unarchived
// root and every unarchived unit beneath one through unarchived parents, and
// the argument for its one placeholder. name is a fixed identifier chosen by
// the caller, never input.
func academicSubtreeCTE(name string, roots []model.AcademicUnitID) (string, any) {
	return name + ` AS (
		SELECT id FROM academic_units WHERE id = ANY(?) AND archived_at IS NULL
		UNION ALL SELECT child.id FROM academic_units child
		JOIN ` + name + ` parent ON child.parent_id = parent.id
		WHERE child.archived_at IS NULL
	)`, pq.Array(idStrings(roots))
}
