// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package retrylayer retries an allowlist of idempotent store operations
// after a failure the adapter classifies as transient. Every other method,
// including every transaction, runs exactly once.
//
//go:generate go run ../storetest/layergen -layer retry -source .. -allowlist allowlist.txt -output forwarding_gen.go
package retrylayer
