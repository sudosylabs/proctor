// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package correction changes the instructions, browser policy or resources of
// an open or paused sitting while it runs. New resource content is staged and
// verified first; applying the correction then publishes a new immutable exam
// revision for the sitting instead of editing the old one. Showing candidates
// the correction and recording their acknowledgement belongs to attempt.
package correction
