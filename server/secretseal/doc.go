// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package secretseal encrypts small recoverable secrets into a versioned
// envelope bound to a purpose and an owner. It does only the cryptography;
// callers own persistence, rotation and retention.
package secretseal
