// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package manageraccess decides which action a caller needs for an Exam: the
// ordinary action, for a current Exam Manager who is also a current member
// of the Exam's own Academic Unit, or the explicit override action for anyone
// else. Being an Exam Manager grants no permission by itself, and using an
// override never makes someone a manager.
//
// SelectAction only picks the action. The caller still authorizes it, and
// keeps error handling, audit and any mutation. The package imports only
// model and store and caches nothing.
package manageraccess
