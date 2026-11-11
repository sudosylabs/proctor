// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package access owns the authorization decisions and the audit trail every
// application area uses. Authorizer resolves a Resource to its Institution,
// Academic Unit and Class scopes, evaluates current Role Bindings, and records
// each decision before it returns; Auditor writes attempt, decision and
// terminal audit events; RunAudited is the shared attempt protocol around one
// named Store mutation.
//
// It owns no use case, transport, or post-commit effect. It may import only
// model, store, app/apperror and app/invocation; it never imports app itself,
// an area package, a transport, or a concrete adapter.
package access
