// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package search finds exams, classes and users in one call. It owns no data
// and no visibility rule: each type goes through its own list use case or
// authorization, so it is authorized, audited and scoped exactly as its own
// list is.
package search
