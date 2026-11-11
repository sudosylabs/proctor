// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package job is the single generic engine for durable, finite application
// Jobs and their runtime lifecycle. It owns immutable descriptor registration, database-backed claiming,
// leases and heartbeats, fenced state transitions, checkpoints, work budgets,
// retry and cancellation observation, recurrence timing, panic containment,
// safe operator projections, non-overlapping periodic runtime maintenance, and
// bounded shutdown. Periodic tasks create no Job, Job Attempt, or occurrence
// ledger; their named application/Store operation owns cross-node convergence.
//
// Execution is at-least-once. A successful claim is not an exactly-once
// guarantee, so handlers must make their domain effects idempotent and rely on
// the appropriate durable commit boundary. The engine owns execution
// mechanics, not the meaning of a Job type, command, checkpoint, progress
// stage, result, public error code, or recurring occurrence.
//
// Domain-specific handlers, commands, and recurrence proposers live in the
// sibling app/jobhandler package. Parent app supplies narrow use-case adapters and
// constructs Engine from the immutable app/jobhandler catalog plus domain-owned
// PeriodicTasks. The module-root
// server retains the constructed Engine and owns when it starts and closes;
// construction itself is inert.
//
// Operator inspection and descriptor-governed transition mechanics live in
// operations.go, as Engine methods. Operations, in operator.go, is the
// operator Job service built on them: the use cases that list and inspect
// Jobs and their attempts and cancel or retry a Job. It authorizes the caller
// on the Institution, names who started each Job and what it is about, orders
// the durable audit before the transition, and translates engine and Store
// failures into application errors. DecodeStrictUniqueDocument, in
// document.go, is the strict decoder Job documents are read with.
//
// Start with engine.go for execution, registry.go for what a handler
// declares, and operator.go for what an operator may do.
//
// The engine depends inward only on logging, model and store.JobStore.
// Operations adds app/access, app/apperror and app/invocation. This package
// never imports its parent, an area package, app/jobhandler, app/mail,
// transports, platform services, or concrete persistence adapters.
//
// Tests at this boundary cover registration, execution, fencing, recovery,
// recurrence, projections, controls, operator authorization and audit order,
// races, and shutdown. Concrete handler
// tests live in app/jobhandler, while PostgreSQL and root-lifecycle tests provide
// cross-component recovery and wiring evidence.
package job
