// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"github.com/sudosylabs/proctor/server/app/apperror"
)

// publicFailures presents every Starter Workspace use-case failure: unexpected errors
// become exam.starter_workspace.unavailable, and protected targets conceal denial and absence.
var publicFailures = apperror.Concealment{
	Unavailable: "exam.starter_workspace.unavailable",
	Concealed:   []apperror.Code{"exam.starter_workspace.not_found"},
}

var storeErrors = apperror.StoreTable{
	Constraints: map[string]apperror.Code{
		"exam_archived":                 "exam.archived",
		"exam_draft_revision":           "exam.draft.revision_conflict",
		"workspace_path_collision":      "exam.starter_workspace.path_conflict",
		"workspace_parent_missing":      "exam.starter_workspace.parent_not_found",
		"workspace_directory_not_empty": "exam.starter_workspace.directory_not_empty",
		"workspace_entry_limit":         "exam.starter_workspace.entry_limit",
		"workspace_total_size":          "exam.starter_workspace.total_size_limit",
		"workspace_object_expired":      "exam.starter_workspace.upload_expired",
		"workspace_object_state":        "exam.starter_workspace.object_conflict",
		"workspace_no_changes":          "exam.starter_workspace.no_changes",
		"workspace_descendant_move":     "exam.starter_workspace.move_conflict",
		"workspace_path_limit":          "exam.starter_workspace.move_conflict",
		"workspace_entry_kind":          "exam.starter_workspace.entry_kind",
		"workspace_content_version":     "exam.starter_workspace.content_conflict",
	},
	NotFound:    "exam.starter_workspace.not_found",
	Conflict:    "exam.starter_workspace.conflict",
	Invalid:     "exam.starter_workspace.invalid",
	Unavailable: "exam.starter_workspace.unavailable",
}
