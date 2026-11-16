// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package review holds what happens after a submission is sealed: managers
// inspect its integrity flags and evidence, record decisions and draft
// remarks, finalize the review and release the result. Every write is fenced
// by the review's revision. The candidate who made a submission never reviews
// it, and sees nothing until the result is released. Collecting evidence and
// sealing submissions belong to attempt; the package does not grade.
package review
