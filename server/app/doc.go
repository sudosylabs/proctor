// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package app holds Proctor's use cases: what each user may do, and what
// happens when they do it. A use case checks authorization against current
// state, records audit, calls a named Store operation, and publishes
// follow-up effects only after the change commits. HTTP and WebSocket wire
// formats live in httpapi and websocket, SQL in store/sqlstore, and stored
// file bytes in filecontent.
//
// App, built once by New, is the application's public surface. It names
// each service in its own field and embeds none, so each use case is
// declared once, as a method on its service. Each area lives in its own
// child package; this package keeps only the services that span areas
// (search, administration counts, realtime publication and admission, Job
// and mail operations).
//
// Where to look:
//   - app.go: App
//   - new.go: New, which builds the graph top to bottom; new_exam.go and
//     new_jobs.go build the Exam services and the Job runtime
//   - dependencies.go: what the server package must supply
//   - leaf packages: access (authorization and access.RunAudited, the shared
//     begin, mutate and audit sequence most audited changes use), paging,
//     apperror (the one application error type), invocation
//     (the call context every use case takes), idempotency, job (the durable
//     Job engine), jobhandler, mail, realtime and exam
//   - area packages: academic, user, identity (sign-in, Sessions, MFA,
//     tokens, Desktop authorization, account recovery and account state),
//     administration (Roles, Role Bindings, the Access Policy, retention and
//     the audit listings) and onboarding (Invitations, public registration,
//     academic administration batches, onboarding imports and Student
//     Progression)
package app
