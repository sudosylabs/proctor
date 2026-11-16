// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package attempt holds the candidate side of an exam, from joining a sitting
// to sealing a submission, and lets managers inspect attempts and submissions.
// Participation leases are renewed and expired against database time.
//
// Collected evidence is never rewritten: a focus-loss report that arrives
// after the attempt ended becomes a discrepancy for review and leaves the
// attempt unchanged.
package attempt
