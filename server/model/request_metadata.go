// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

// RequestMetadata is bounded, transport-neutral context for security audit.
// IPAddress is the direct peer; forwarded headers are not trusted until
// deployment configuration names trusted proxies.
type RequestMetadata struct {
	RequestID string
	IPAddress string
	UserAgent string
}
