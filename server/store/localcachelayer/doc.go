// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package localcachelayer caches a handwritten allowlist of store reads in
// bounded process memory. PostgreSQL stays authoritative: a cache failure is
// a miss, and security state is never cached here.
//
//go:generate go run ../storetest/layergen -layer localcache -source .. -output forwarding_gen.go
package localcachelayer
