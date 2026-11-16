// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package manageraccess picks which action a caller needs for an exam: the
// ordinary one for a current manager who is also a current member of the
// exam's own academic unit, the override one for anyone else. Being a manager
// grants no permission by itself, and picking an action authorizes nothing;
// the caller still authorizes it.
package manageraccess
