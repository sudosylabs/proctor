// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package exam holds the exam use cases that are not about one sitting or
// attempt: creating exams and editing their draft, the catalog, archiving,
// managers, publishing revisions, records completion and exports. The draft is
// an exam's only mutable state; sittings deliver published revisions, which
// never change. Nothing in the server grades a submission, and an integrity
// flag is a reason to review, not a finding.
package exam
