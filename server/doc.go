// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package server builds and runs one Proctor node. It is the composition
// root: the only package that picks concrete infrastructure and wires it into
// the application and its transports; business rules live in package app.
// New never binds a listener or starts background work. Run does, and Close
// or a failure during Run shuts every part down once, in one order. Tests
// build the same graph through NewForTesting; there is no second path.
package server
