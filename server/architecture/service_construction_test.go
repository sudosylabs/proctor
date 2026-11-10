// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// TestServicesTakeOneDependencies keeps service constructors from growing
// positional parameter lists, in app and its child modules: a New or new
// function that builds, by pointer, a type of its own package holding
// collaborators (interfaces, functions, or pointers to types with methods)
// takes exactly one parameter, a struct named Dependencies or <X>Dependencies
// declared beside it.
func TestServicesTakeOneDependencies(t *testing.T) {
	t.Parallel()
	for _, violation := range serviceConstructionViolations(typedServerPackages(t), serverModule) {
		assert.Fail(t, violation)
	}
}

func TestServiceConstructionRuleRejectsPlantedViolations(t *testing.T) {
	t.Parallel()
	loaded := loadFixturePackages(t, map[string]string{
		"app/services.go": `package app

import "time"

type reader interface{ Read() error }

type positional struct {
	reader reader
	now    func() time.Time
}

func newPositional(reader reader, now func() time.Time) *positional {
	return &positional{reader: reader, now: now}
}

type configured struct{ reader reader }

type Config struct{ Reader reader }

func newConfigured(config Config) *configured { return &configured{reader: config.Reader} }

type keptDependencies struct{ Reader reader }

type kept struct{ reader reader }

func newKept(deps keptDependencies) *kept { return &kept{reader: deps.Reader} }

type page struct{ items []string }

func newPage(items []string, limit int) *page { return &page{items: items[:limit]} }

type App struct{ Kept *kept }

func New(deps keptDependencies) *App { return &App{Kept: newKept(deps)} }
`,
		"app/area/area.go": `package area

type reader interface{ Read() error }

type Dependencies struct{ Reader reader }

type Service struct{ reader reader }

func New(deps Dependencies) (*Service, error) { return &Service{reader: deps.Reader}, nil }

type helper struct{ reader reader }

func newHelper(reader reader, limit int) *helper { return &helper{reader: reader} }
`,
	})
	require.Equal(t, []string{
		"area.go:13: fixture/app/area.newHelper takes 2 parameters; take one Dependencies struct",
		"services.go:12: fixture/app.newPositional takes 2 parameters; take one Dependencies struct",
		"services.go:20: fixture/app.newConfigured takes Config; take one struct named Dependencies or <X>Dependencies",
	}, serviceConstructionViolations(loaded, "fixture"))
}

func serviceConstructionViolations(loaded []*packages.Package, module string) []string {
	var violations []string
	for _, pkg := range loaded {
		if pkg.PkgPath != module+"/app" && !strings.HasPrefix(pkg.PkgPath, module+"/app/") {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			constructor, ok := scope.Lookup(name).(*types.Func)
			if !ok || !(strings.HasPrefix(name, "New") || strings.HasPrefix(name, "new")) {
				continue
			}
			signature := constructor.Type().(*types.Signature)
			if constructedService(pkg.Types, signature) == nil {
				continue
			}
			position := sourcePosition(pkg, constructor.Pos())
			qualified := pkg.PkgPath + "." + name
			if signature.Params().Len() != 1 {
				violations = append(violations, fmt.Sprintf("%s: %s takes %d parameters; take one Dependencies struct",
					position, qualified, signature.Params().Len()))
				continue
			}
			dependencies, ok := signature.Params().At(0).Type().(*types.Named)
			if !ok || dependencies.Obj().Pkg() != pkg.Types || !strings.HasSuffix(dependencies.Obj().Name(), "Dependencies") {
				violations = append(violations, fmt.Sprintf("%s: %s takes %s; take one struct named Dependencies or <X>Dependencies",
					position, qualified, types.TypeString(signature.Params().At(0).Type(), types.RelativeTo(pkg.Types))))
			}
		}
	}
	sort.Strings(violations)
	return violations
}

// constructedService returns the struct type a constructor builds by pointer
// when that type holds at least one collaborator.
func constructedService(owner *types.Package, signature *types.Signature) *types.Named {
	if signature.Results().Len() == 0 || signature.TypeParams().Len() > 0 {
		return nil
	}
	pointer, ok := signature.Results().At(0).Type().(*types.Pointer)
	if !ok {
		return nil
	}
	named, ok := pointer.Elem().(*types.Named)
	if !ok || named.Obj().Pkg() != owner {
		return nil
	}
	structure, ok := named.Underlying().(*types.Struct)
	if !ok || !holdsCollaborator(structure, 0) {
		return nil
	}
	return named
}

func holdsCollaborator(structure *types.Struct, depth int) bool {
	errorType := types.Universe.Lookup("error").Type()
	for index := range structure.NumFields() {
		fieldType := structure.Field(index).Type()
		switch underlying := fieldType.Underlying().(type) {
		case *types.Interface:
			if !types.Identical(fieldType, errorType) {
				return true
			}
		case *types.Signature:
			return true
		case *types.Pointer:
			if named, ok := underlying.Elem().(*types.Named); ok && named.NumMethods() > 0 {
				return true
			}
		case *types.Struct:
			if depth == 0 && structure.Field(index).Name() == "deps" && holdsCollaborator(underlying, depth+1) {
				return true
			}
		}
	}
	return false
}
