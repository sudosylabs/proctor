// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package access owns the authorization decisions and audit trail every
// application area uses. The Authorizer records each decision before it
// returns. The package may import only model, store, app/apperror and
// app/invocation, so every area can depend on it without a cycle.
package access
