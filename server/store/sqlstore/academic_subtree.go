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

// academicSubtreeCTE returns the recursive common table expression name,
// holding the id of every unarchived listed root and of every unarchived
// Academic Unit beneath one through unarchived parents, and the argument its
// one placeholder binds. The statement that defines it starts WITH RECURSIVE;
// name is a fixed identifier chosen by the caller, never input.
func academicSubtreeCTE(name string, roots []model.AcademicUnitID) (string, any) {
	return name + ` AS (
		SELECT id FROM academic_units WHERE id = ANY(?) AND archived_at IS NULL
		UNION ALL SELECT child.id FROM academic_units child
		JOIN ` + name + ` parent ON child.parent_id = parent.id
		WHERE child.archived_at IS NULL
	)`, pq.Array(idStrings(roots))
}
