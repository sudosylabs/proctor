// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package timerlayer times every store operation without seeing its
// arguments. It never changes a result or error.
//
//go:generate go run ../storetest/layergen -layer timer -source .. -output forwarding_gen.go
package timerlayer
