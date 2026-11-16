// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package filecontent reads and writes the bytes of the files Proctor manages,
// under private file-store keys derived from record IDs. It does not decide who
// may read a file or whether it exists: the use case records that in
// PostgreSQL, and bytes written here stay invisible until that record commits.
//
// Validation and image work are bounded per node; a call over the limit fails
// with ErrWorkCapacity at once instead of queueing.
package filecontent
