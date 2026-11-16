// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package mail prepares the transactional mail Proctor sends and gives
// operators control over it.
//
// Nothing is sent while a request runs. The composer renders and seals a
// message into a record the caller commits in the same store operation as
// the change that caused it; the delivery job in app/jobs sends it later.
// The package never imports app, app/jobs or a transport.
package mail
