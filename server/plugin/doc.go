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
// given: an IdentityProviderType, an ExportFormat or an EventHandler. The
// Host grows one typed method per capability, added when a plugin needs it.
// There is no broad API that every plugin can reach.
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
//   - Commit order. Event handlers run after the change they describe has
//     committed, never inside it, so they cannot block or alter it.
//   - Minimal events. By default an Event carries identifiers, not answers,
//     Workspace content or integrity evidence. A handler that needs more
//     asks for it through a capability that authorizes and audits the
//     request.
//
// These are defaults for the capabilities that exist today. A future
// capability that lets a plugin act on Attempt content does so through the
// same use cases as a person, authorized and audited in the plugin's name.
package plugin
