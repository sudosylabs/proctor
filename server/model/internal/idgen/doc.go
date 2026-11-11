// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package main is the generator behind model/id_gen.go. For each ID type listed
// in catalog.go it writes the repetitive functions: the constructor, the
// parser, the validity checks, and text and JSON conversion. The ID types
// themselves are declared by hand in model/id.go. Run it with
// make -C server generate.
package main
