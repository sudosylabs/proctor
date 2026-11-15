// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package plugin holds the contracts Proctor's optional features are built
// against. A plugin is a first-party Go package compiled into the server
// that an installation can turn on or off, such as the CAS and OIDC sign-in
// protocols or the zip Exam export. Proctor never loads code from outside
// its own binary.
//
// A plugin implements Plugin. While the server starts, it activates each
// enabled plugin, and the plugin registers what it offers on the Host it is
// given: an IdentityProviderType or an ExportFormat. The Host grows one
// typed method per capability, added when a plugin needs it. There is no
// broad API that every plugin can reach, and no plugin touches the database.
//
// Plugins are edge adapters that never act on an Attempt. Features that
// touch admission, the Attempt Workspace, Submission or evidence, such as
// code execution, are part of the core.
//
// Because plugins are trusted, reviewed code, they run in process and are
// called through plain Go interfaces. There is no separate process, RPC,
// signing or manifest file. What the server keeps instead:
//
//   - Containment. A panic in a plugin becomes an error. After repeated
//     panics the plugin is marked failed and is not called again. A plugin
//     that fails to activate is left out. Either way the server keeps
//     serving.
//   - Authority. The core keeps the decisions it owns. It resolves accounts
//     and opens Sessions for an identity provider's Assertion, and it
//     chooses, bounds and verifies every file an export format writes.
package plugin
