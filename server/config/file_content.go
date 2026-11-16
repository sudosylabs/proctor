// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

// FileContent bounds concurrent content validation and rendition work on one
// node. Changing it needs a restart.
type FileContent struct {
	MaximumConcurrentOperations int `json:"MaximumConcurrentOperations"`
}
