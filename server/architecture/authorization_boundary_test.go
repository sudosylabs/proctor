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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransportCannotOwnApplicationAuthorizationCompatibility keeps
// authorization decisions out of the transports: no function, field or call
// in httpapi or websocket carries an authorization-decision name, so a
// handler cannot decide access that its use case owns.
func TestTransportCannotOwnApplicationAuthorizationCompatibility(t *testing.T) {
	t.Parallel()

	inspectProductionGoFiles(t, []string{"httpapi", "websocket"}, func(path string, file *ast.File) {
		for _, name := range transportAuthorizationNames(file) {
			assert.Fail(t, fmt.Sprintf("transport-owned authorization name %q in %s", name, path))
		}
	})
}

func TestTransportAuthorizationNamesRejectsPlantedViolations(t *testing.T) {
	t.Parallel()

	source := `package httpapi

type handler struct {
	CheckAccess func() error
}

func requirePermission() {}

func (h handler) serve(service usecase) {
	_ = service.Authorize()
	_ = service.PrincipalHasPermissionToClass()
	_ = service.Rename()
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "planted.go", source, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"CheckAccess", "requirePermission", "Authorize", "PrincipalHasPermissionToClass"}, transportAuthorizationNames(file))
}

func transportAuthorizationNames(file *ast.File) []string {
	var names []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch candidate := node.(type) {
		case *ast.FuncDecl:
			if transportAuthorizationName(candidate.Name.Name) {
				names = append(names, candidate.Name.Name)
			}
		case *ast.Field:
			for _, name := range candidate.Names {
				if transportAuthorizationName(name.Name) {
					names = append(names, name.Name)
				}
			}
		case *ast.SelectorExpr:
			if transportAuthorizationName(candidate.Sel.Name) {
				names = append(names, candidate.Sel.Name)
			}
		}
		return true
	})
	return names
}

// Every use case is declared once, on the focused service that owns it. App
// names each service in an exported field (Academic *academic.Service,
// Search *search.Service) and embeds nothing, so it declares no method of its
// own beyond the runtime handle composition borrows, and holds no other
// field.
func TestApplicationSurfaceIsNamedServices(t *testing.T) {
	t.Parallel()

	allowedMethods := map[string]struct{}{"JobEngine": {}}
	allowedFields := map[string]struct{}{"jobEngine": {}}
	inspectProductionGoFiles(t, []string{"app"}, func(path string, file *ast.File) {
		for _, declaration := range file.Decls {
			switch candidate := declaration.(type) {
			case *ast.FuncDecl:
				if !appMethod(candidate) {
					continue
				}
				_, allowed := allowedMethods[candidate.Name.Name]
				assert.True(t, allowed, "App declares method %s in %s; declare the use case on its owning service", candidate.Name.Name, path)
			case *ast.GenDecl:
				for _, specification := range candidate.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if !ok || typeSpec.Name.Name != "App" {
						continue
					}
					structure, ok := typeSpec.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range structure.Fields.List {
						assert.NotEmpty(t, field.Names, "App embeds %s in %s; name the service in an exported field", types.ExprString(field.Type), path)
						for _, name := range field.Names {
							_, allowed := allowedFields[name.Name]
							allowed = allowed || (name.IsExported() && isServicePointer(field.Type))
							assert.True(t, allowed, "App holds field %s in %s; hold only exported service pointers", name.Name, path)
						}
					}
				}
			}
		}
	})
}

// isServicePointer reports a field type of the form *<area>.<Service> or
// *<Service>: a pointer to an exported service type of an area package or of
// the parent. A Store contract is not a service, so App cannot become a
// persistence locator.
func isServicePointer(expression ast.Expr) bool {
	pointer, ok := expression.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch target := pointer.X.(type) {
	case *ast.Ident:
		return target.IsExported()
	case *ast.SelectorExpr:
		qualifier, packageQualified := target.X.(*ast.Ident)
		return packageQualified && qualifier.Name != "store" && target.Sel.IsExported()
	default:
		return false
	}
}

// appMethod reports whether function is a method on the application App.
func appMethod(function *ast.FuncDecl) bool {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return false
	}
	receiverType := function.Recv.List[0].Type
	if pointer, ok := receiverType.(*ast.StarExpr); ok {
		receiverType = pointer.X
	}
	identifier, ok := receiverType.(*ast.Ident)
	return ok && identifier.Name == "App"
}

func transportAuthorizationName(name string) bool {
	switch name {
	case "Can", "Authorize", "CheckAccess", "CheckPermission", "HasPermission", "requirePermission":
		return true
	default:
		return strings.HasPrefix(name, "PrincipalHasPermission") ||
			strings.HasPrefix(name, "AuthorizePrincipalTo")
	}
}

func inspectProductionGoFiles(
	t *testing.T,
	directories []string,
	inspect func(path string, file *ast.File),
) {
	t.Helper()
	for _, directory := range directories {
		root := filepath.Join("..", directory)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			inspect(path, file)
			return nil
		})
		require.NoError(t, err, "inspect production Go files under %s", directory)
	}
}
