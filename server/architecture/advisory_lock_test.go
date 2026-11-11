// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"go/ast"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSQLStoreAdvisoryLocksUseOneHelper keeps named advisory locks on the
// sqlstore helpers so every key shares one hash function and prefix.
func TestSQLStoreAdvisoryLocksUseOneHelper(t *testing.T) {
	t.Parallel()

	inspectProductionGoFiles(t, []string{"store/sqlstore"}, func(path string, file *ast.File) {
		if filepath.Base(path) == "advisory_lock.go" {
			return
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok {
				return true
			}
			value, isString := stringLiteral(literal)
			if isString {
				assert.NotContains(t, value, "pg_advisory", "%s spells an advisory lock query; use lockAdvisory, lockAdvisoryShared, or lockCommandIdempotency", path)
			}
			return true
		})
	})
}
