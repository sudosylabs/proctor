// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import "github.com/sudosylabs/proctor/server/model"

// NormalizePath returns the canonical POSIX-relative form of value. Commands
// reject any path that is not already in this form.
func NormalizePath(value string) (string, error) { return model.NormalizeStarterWorkspacePath(value) }
