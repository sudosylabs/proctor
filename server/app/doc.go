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
// App, built once by New, is the application's public surface. It embeds one
// service per area, so each use case is declared once, as a method on its
// service. Each area is moving into its own child package, which App will
// expose as a named field; until an area moves, its services live in this
// package. Exam use cases already live in app/exam and its children.
//
// Where to look:
//   - app.go: App and New
//   - dependencies.go: what the server package must supply
//   - construction_*.go: how each group of services is built
//   - mutation_attempt.go: the shared begin, mutate and audit sequence most
//     audited changes use
//   - child packages: apperror (the one application error type), invocation
//     (the call context every use case takes), idempotency, job (the durable
//     Job engine), jobhandler, mail, realtime and exam
package app
