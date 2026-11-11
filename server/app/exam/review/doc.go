// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package review holds what happens after a Submission: managers inspect its
// Integrity flags, evidence and late discrepancies, record decisions and
// draft remarks, finalize the Review, and release the result to the student.
// Decisions are fenced by the Review's revision, and a finalized Review does
// not change. The student sees only the released result.
//
// Collecting Integrity evidence and sealing Submissions belong to attempt.
// The package does not grade, speak HTTP, or run SQL. A committed Review
// change is announced by the events built in effects.go. It imports only
// model, store, app/access, app/apperror, app/invocation, app/idempotency,
// app/realtime, app/mail and app/exam/safemarkdown; it reaches the Sitting
// area through the SittingAuthorizer port. Start with Service in review.go;
// manager_authorization.go holds who may view, review and release a
// Submission, and result_release_mail.go prepares the release notice.
package review
