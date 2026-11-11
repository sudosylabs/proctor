// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package mail prepares the transactional mail Proctor sends, such as
// Invitations, password resets and Sitting notices, and gives operators control
// over what was prepared.
//
// A use case asks the Composer for a message. The Composer renders it, encrypts
// the stored payload and returns a record that the caller commits in the same
// Store operation as the change that caused the mail. Nothing is sent while a
// request runs: the mail delivery Job in app/jobs sends committed records
// later. Start with composer.go, and catalog.go for the per-message settings.
//
// Operations (operations.go, rekey.go, rekey_status.go) is the operator
// service: list, inspect, cancel and retry deliveries, send a test message,
// read metrics and rotate the payload key. health.go, maintenance.go and
// delivery_relevance.go hold what the delivery Jobs share: the mail health
// state and whether a queued message should still be sent. telemetry.go
// declares the metrics the module root records.
//
// The package never imports app, app/jobs or a transport.
package mail
