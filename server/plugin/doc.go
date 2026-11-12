// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package plugin holds the contracts Proctor's optional features are built
// against. A plugin is a first-party feature that an installation can turn on
// or leave off, such as a code execution environment, live session streaming
// or an AI agent integration. Proctor does not load third-party plugins: an
// examination platform cannot let outside code near its guarantees.
//
// A plugin can provide an identity provider or an export format, and can
// receive domain events through an EventHandler after the change they
// describe has committed. The server's own built-ins already use these
// contracts: the zip export is an ExportFormat, and CAS and OIDC are
// IdentityProviders.
//
// Plugins extend Proctor without weakening it. No plugin code runs before
// the commit of an admission, Submission, integrity evidence, policy,
// authorization or audit decision; events carry identifiers, never answers,
// Workspace content or evidence; and the core verifies whatever a plugin
// produces, such as every file an export format writes.
//
// Read IdentityProvider, ExportFormat or EventHandler for the capability you
// are building. How a feature registers itself and is turned on is decided
// with the server's plugin host, which is not built yet.
package plugin
