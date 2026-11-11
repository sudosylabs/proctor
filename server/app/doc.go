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
// This package itself only assembles the application. App, built once by
// New, is the application's public surface: it names each service in its own
// field and embeds none, so each use case is declared once, as a method on
// its service, in the child package that owns it. A reader looking for
// behavior opens that child package; what is here is the App type, its
// construction and the Dependencies the server package supplies.
//
// Where to look:
//   - app.go: App
//   - new.go: New, which builds the graph top to bottom; new_exam.go and
//     new_jobs.go build the Exam services and the Job runtime
//   - dependencies.go: what the server package must supply
//   - leaf packages: access (authorization and access.RunAudited, the shared
//     begin, mutate and audit sequence most audited changes use), paging,
//     apperror (the one application error type), invocation
//     (the call context every use case takes), idempotency, realtime and exam
//   - job (the durable Job engine and job.Operations, the operator Job
//     service), jobs (the Job handlers) and mail (mail composition and
//     mail.Operations, the operator mail service, with the mail telemetry
//     contracts, health and delivery relevance policy)
//   - area packages: academic, user, identity (sign-in, Sessions, MFA,
//     tokens, Desktop authorization, account recovery and account state),
//     administration (Roles, Role Bindings, the Access Policy, retention,
//     the audit listings and the administration counts), onboarding
//     (Invitations, public registration, academic administration batches,
//     onboarding imports and Student Progression) and search (one search
//     across Exams, Classes and Users)
//
// Besides assembly, a few things still live here. realtime_admission.go holds
// WebSocket admission: it composes the identity, Exam and Access Control
// services, which the app/realtime child may not import.
// realtime_attempt_targets.go answers that child's one Attempt lookup from the
// Store. health.go (node readiness), operational_metrics.go (the operational
// event seam the module root records) and version.go (build information) are
// what the module root and transports read from the application as a whole.
package app
