// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package attempt holds the candidate side of an Exam: everything from
// joining a Sitting to sealing a Submission. It admits candidates, fences
// each Participation and Connection, renews and expires Participations
// against database time, evaluates Focus Loss, suspends and re-allows
// Attempts, shows candidates the protected Exam, and stores their Attempt
// Workspace. It also seals Submissions, both when a candidate submits and when
// a closing Sitting seals the remaining Attempts, and lets managers inspect
// Attempts and Submissions.
//
// Collected evidence is never rewritten. A Focus Loss report that arrives
// after the Attempt ended is recorded as a discrepancy for review and leaves
// the Attempt unchanged.
//
// The package does not speak HTTP or WebSocket, run SQL, choose a file store,
// or run Jobs; the Sitting-close Job calls into it. It imports only model,
// store, app/access, app/apperror, app/invocation, app/idempotency,
// app/realtime, app/mail and app/exam/safemarkdown; it reaches the Sitting
// area through the SittingAuthorizer port. Start with attempt.go for Service
// and its dependencies, then the file named for the use case (connection.go,
// workspace.go, submission.go, ...). manager_authorization.go holds who may
// act as a manager on a Sitting's Attempts and Submissions, and
// submission_mail.go prepares the candidate's receipt.
package attempt
