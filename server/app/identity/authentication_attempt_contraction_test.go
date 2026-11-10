// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticationAttemptAccountingHasOneProductionOwner(t *testing.T) {
	t.Parallel()

	accountingType := reflect.TypeOf((*AttemptAccounting)(nil))
	for name, service := range map[string]reflect.Type{
		"local login":             reflect.TypeOf(authenticationService{}),
		"account recovery":        reflect.TypeOf(accountTokenService{}),
		"external authentication": reflect.TypeOf(externalAuthenticationService{}),
		"installation bootstrap":  reflect.TypeOf(bootstrapService{}),
	} {
		deps, ok := service.FieldByName("deps")
		require.True(t, ok, "%s keeps no deps field", name)
		field, ok := deps.Type.FieldByName("Attempts")
		require.True(t, ok, "%s does not retain the shared private attempt accounting", name)
		require.Equal(t, accountingType, field.Type, "%s does not retain the shared private attempt accounting", name)
	}

	// The parent app package builds the one shared instance and hands it to
	// this package, to the onboarding area's Invitation and registration
	// flows, and to its own mail flows.
	constructorCalls := 0
	for _, directory := range []string{".", ".."} {
		for name, file := range parseProductionGoFiles(t, directory) {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && selector.Sel.Name == "NewAttemptAccounting" {
					constructorCalls++
					assert.Equal(t, "../new.go", filepath.Join(directory, name))
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if ok && identifier.Name == "NewAttemptAccounting" {
					constructorCalls++
					assert.Fail(t, "identity constructs its own attempt accounting", name)
				}
				return true
			})
		}
	}
	require.Equal(t, 1, constructorCalls, "production constructor calls")
}

func TestMigratedAuthenticationFlowsDoNotReclaimAttemptMechanics(t *testing.T) {
	t.Parallel()

	files := parseProductionGoFiles(t, ".")
	for _, name := range []string{
		"authentication.go",
		"account_recovery.go",
		"account_recovery_ports.go",
		"external_authentication.go",
		"bootstrap.go",
	} {
		file := files[name]
		require.NotNil(t, file, "production source %s was not parsed", name)
		forbiddenNamespaces := []string{
			"authentication/attempts/",
			"authentication/login/",
			"authentication/recovery/",
			"authentication/external/",
			"authentication/bootstrap/",
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch candidate := node.(type) {
			case *ast.CallExpr:
				selector, ok := candidate.Fun.(*ast.SelectorExpr)
				if ok &&
					selector.Sel.Name == "Add" {
					assert.NotEqual(t, 4, len(candidate.Args), "%s performs four-argument counter Add outside authentication_attempt.go", name)
				}
			case *ast.BasicLit:
				if candidate.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(candidate.Value)
				if err != nil {
					return true
				}
				for _, prefix := range forbiddenNamespaces {
					assert.False(t, strings.HasPrefix(value, prefix), "%s constructs attempt-accounting key namespace %q", name, prefix)
				}
			}
			return true
		})
	}
}

func parseProductionGoFiles(t *testing.T, directory string) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	files := make(map[string]*ast.File)
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fileSet, filepath.Join(directory, name), nil, 0)
		require.NoError(t, parseErr, "parse production source %s", name)
		files[name] = file
	}
	return files
}
