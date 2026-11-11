// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testifyPackages maps the testify import paths to the only names tests use
// for them.
var testifyPackages = map[string]string{
	"github.com/stretchr/testify/require": "require",
	"github.com/stretchr/testify/assert":  "assert",
}

// TestRecordedCallLogsAreAssertedWhole keeps a fake's recorded call log
// (a variable or field named events, order, or calls) asserted as one exact
// sequence. A membership or subsequence check lets an extra write, effect, or
// audit failure completion after a commit pass unnoticed.
func TestRecordedCallLogsAreAssertedWhole(t *testing.T) {
	t.Parallel()

	inspectRepositoryTestCode(t, func(fset *token.FileSet, path string, file *ast.File) {
		for _, violation := range recordedCallLogViolations(fset, path, file) {
			assert.Fail(t, violation)
		}
	})
}

func TestRecordedCallLogRuleRejectsPlantedViolations(t *testing.T) {
	t.Parallel()

	const source = `package fixture

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

type fake struct{ order *[]string; calls []string }

func TestFixture(t *testing.T) {
	events := []string{"authorize", "audit-begin", "store-create"}
	f := fake{order: &events}
	require.Contains(t, events, "store-create")
	require.NotContains(t, *f.order, "audit-fail")
	require.Subset(t, f.calls, []string{"store-create"})
	require.ElementsMatch(t, []string{"store-create"}, f.calls)
	require.True(t, slices.Contains(events, "store-create"))
	require.Equal(t, []string{"authorize", "audit-begin", "store-create"}, events)
	require.Contains(t, f.calls[0], "store")
	require.Len(t, f.calls, 1)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture_test.go", source, parser.SkipObjectResolution)
	require.NoError(t, err)
	require.Equal(t, []string{
		"fixture_test.go:15 asserts recorded call log events with require.Contains; assert the whole sequence with require.Equal",
		"fixture_test.go:16 asserts recorded call log f.order with require.NotContains; assert the whole sequence with require.Equal",
		"fixture_test.go:17 asserts recorded call log f.calls with require.Subset; assert the whole sequence with require.Equal",
		"fixture_test.go:18 asserts recorded call log f.calls with require.ElementsMatch; assert the whole sequence with require.Equal",
		"fixture_test.go:19 asserts recorded call log events with slices.Contains; assert the whole sequence with require.Equal",
	}, recordedCallLogViolations(fset, "fixture_test.go", file))
}

// recordedCallLogNames are the names fakes give the log of calls they record.
var recordedCallLogNames = map[string]bool{"events": true, "order": true, "calls": true}

// partialLogAssertions are the testify assertions that inspect only part of a
// slice: membership, subset, or content regardless of order.
var partialLogAssertions = map[string]bool{
	"Contains": true, "NotContains": true, "Subset": true, "NotSubset": true, "ElementsMatch": true,
}

func recordedCallLogViolations(fset *token.FileSet, path string, file *ast.File) []string {
	names := testifyNames(file)
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		function := strings.TrimSuffix(selector.Sel.Name, "f")
		var candidates []ast.Expr
		switch {
		case names[pkg.Name] && function == "ElementsMatch" && len(call.Args) > 2:
			candidates = call.Args[1:3]
		case names[pkg.Name] && partialLogAssertions[function] && len(call.Args) > 1:
			candidates = call.Args[1:2]
		case pkg.Name == "slices" && (function == "Contains" || function == "Index") && len(call.Args) > 0:
			candidates = call.Args[:1]
		}
		for _, candidate := range candidates {
			if name := recordedCallLogName(candidate); name != "" {
				violations = append(violations, fmt.Sprintf("%s asserts recorded call log %s with %s.%s; assert the whole sequence with require.Equal",
					position(fset, path, call), name, pkg.Name, selector.Sel.Name))
				break
			}
		}
		return true
	})
	return violations
}

// recordedCallLogName returns the spelled name of a recorded call log
// expression, or "" when the expression is something else, such as one
// element of the log.
func recordedCallLogName(expression ast.Expr) string {
	expression = unparen(expression)
	if star, ok := expression.(*ast.StarExpr); ok {
		expression = star.X
	}
	if call, ok := expression.(*ast.CallExpr); ok && len(call.Args) == 0 {
		if selector, isSelector := call.Fun.(*ast.SelectorExpr); isSelector && selector.Sel.Name == "snapshot" {
			expression = selector.X
		}
	}
	var last string
	switch typed := expression.(type) {
	case *ast.Ident:
		last = typed.Name
	case *ast.SelectorExpr:
		last = typed.Sel.Name
	default:
		return ""
	}
	if !recordedCallLogNames[last] {
		return ""
	}
	return types.ExprString(expression)
}

// testifyModules are the modules whose tests assert through testify;
// packages/mail uses plain testing on purpose.
var testifyModules = []string{"server", "packages/cache", "packages/vfs"}

// inspectRepositoryTestCode parses every Go file of the testify modules that
// can hold test code: _test.go files and files that import "testing".
func inspectRepositoryTestCode(t *testing.T, inspect func(fset *token.FileSet, path string, file *ast.File)) {
	t.Helper()
	repositoryRoot := filepath.Clean("../..")
	for _, module := range testifyModules {
		root := filepath.Join(repositoryRoot, filepath.FromSlash(module))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata" || entry.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			if !strings.HasSuffix(path, "_test.go") && !importsPath(file, "testing") {
				return nil
			}
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			inspect(fset, filepath.ToSlash(relative), file)
			return nil
		})
		require.NoError(t, err)
	}
}

func position(fset *token.FileSet, path string, node ast.Node) string {
	return path + ":" + strconv.Itoa(fset.Position(node.Pos()).Line)
}

func importsPath(file *ast.File, importPath string) bool {
	for _, spec := range file.Imports {
		if path, err := strconv.Unquote(spec.Path.Value); err == nil && path == importPath {
			return true
		}
	}
	return false
}

// testifyNames returns the local names of the imported testify packages.
func testifyNames(file *ast.File) map[string]bool {
	names := make(map[string]bool)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name, ok := testifyPackages[path]
		if !ok {
			continue
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = true
	}
	return names
}

func unparen(expression ast.Expr) ast.Expr {
	for {
		paren, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = paren.X
	}
}
