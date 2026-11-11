// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package sitting schedules Exam Sittings and moves them through their
// lifecycle: reschedule, cancel, pause, resume, extend and close. Managers
// drive it through commands, and lifecycle Jobs drive it when a scheduled
// time arrives. It also offers the Sitting authorization checks (such as
// AuthorizeSittingView) that other use cases reuse.
//
// Ordinary actions need a current Exam Manager who belongs to the Exam's
// Academic Unit and holds the role permission; administrators use separate,
// audited override actions. The Store has the final say on revision,
// lifecycle and Academic Period checks. The events that announce a committed
// change are built in effects.go and published only after it commits.
//
// The package builds lifecycle Job requests and the Store saves each one with
// its Sitting change, but it does not run Jobs. Attempts and Submission
// sealing belong to attempt. It imports only model, store, app/access,
// app/apperror, app/invocation, app/idempotency, app/realtime and
// app/exam/manageraccess. Start with Service in service.go.
package sitting
