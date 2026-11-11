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

// TestAuthorizationHelpersShareOneShape keeps every authorization helper in
// app and its child modules (an exported function, or an exported method,
// concrete or on a port, named Authorize or Authorize<Concept>) in the shape
// of the canonical decision: it takes (context.Context,
// invocation.Invocation, ...), so the decision is made for, and recorded
// against, the caller's Principal and request; and it reports the decision
// as its error, returning error or (R, error) with R a named struct. A helper
// that returns (bool, error) fails open when a caller checks only the error,
// and one without the Invocation decides for nobody in particular.
func TestAuthorizationHelpersShareOneShape(t *testing.T) {
	t.Parallel()
	for _, violation := range authorizationHelperViolations(typedServerPackages(t), serverModule) {
		assert.Fail(t, violation)
	}
}

func TestAuthorizationHelperRuleRejectsPlantedViolations(t *testing.T) {
	t.Parallel()
	loaded := loadFixturePackages(t, map[string]string{
		"app/invocation/invocation.go": "package invocation\n\ntype Invocation struct{}\n\ntype System struct{}\n",
		"app/app.go": `package app

import (
	"context"

	"fixture/app/invocation"
)

type Scope struct{ ID string }

type authorizer interface {
	Authorize(context.Context, invocation.Invocation, string) error
	AuthorizeScope(context.Context, invocation.Invocation, string) (Scope, error)
	AuthorizeManage(context.Context, invocation.Invocation, string) (bool, error)
	AuthorizeWithScope(context.Context, invocation.Invocation, string) (string, string, error)
	AuthorizeRead(context.Context, string) error
	AuthorizeJob(context.Context, invocation.System, string) error
	CanDelegate(context.Context, invocation.Invocation, string) (bool, error)
}

type service struct{}

func (service) AuthorizeView(ctx context.Context, inv invocation.Invocation, id string) error { return nil }

func (service) AuthorizeEdit(ctx context.Context, id string) (bool, error) { return true, nil }

func (service) authorizeLocally(context.Context, string) bool { return true }

func AuthorizeExport(ctx context.Context, id string) bool { return true }

func authorizeQuietly(context.Context, string) bool { return true }
`,
	})
	require.Equal(t, []string{
		"app.go:14: fixture/app.authorizer.AuthorizeManage returns (bool, error); return error, or (R, error) with R a named struct result",
		"app.go:15: fixture/app.authorizer.AuthorizeWithScope returns (string, string, error); return error, or (R, error) with R a named struct result",
		"app.go:16: fixture/app.authorizer.AuthorizeRead takes (context.Context, string); take (context.Context, invocation.Invocation, ...)",
		"app.go:17: fixture/app.authorizer.AuthorizeJob takes (context.Context, invocation.System, string); take (context.Context, invocation.Invocation, ...)",
		"app.go:25: fixture/app.service.AuthorizeEdit takes (context.Context, string); take (context.Context, invocation.Invocation, ...)",
		"app.go:29: fixture/app.AuthorizeExport takes (context.Context, string); take (context.Context, invocation.Invocation, ...)",
	}, authorizationHelperViolations(loaded, "fixture"))
}

func authorizationHelperViolations(loaded []*packages.Package, module string) []string {
	var violations []string
	for _, pkg := range loaded {
		if pkg.PkgPath != module+"/app" && !strings.HasPrefix(pkg.PkgPath, module+"/app/") {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			if function, ok := scope.Lookup(name).(*types.Func); ok {
				if problem := authorizationHelperShape(function, module); problem != "" {
					violations = append(violations, fmt.Sprintf("%s: %s.%s %s", sourcePosition(pkg, function.Pos()), pkg.PkgPath, name, problem))
				}
				continue
			}
			typeName, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || typeName.IsAlias() {
				continue
			}
			named, ok := typeName.Type().(*types.Named)
			if !ok {
				continue
			}
			var methods []*types.Func
			if port, ok := named.Underlying().(*types.Interface); ok {
				for index := range port.NumExplicitMethods() {
					methods = append(methods, port.ExplicitMethod(index))
				}
			} else {
				for index := range named.NumMethods() {
					methods = append(methods, named.Method(index))
				}
			}
			for _, method := range methods {
				if problem := authorizationHelperShape(method, module); problem != "" {
					violations = append(violations, fmt.Sprintf("%s: %s.%s.%s %s", sourcePosition(pkg, method.Pos()), pkg.PkgPath, name, method.Name(), problem))
				}
			}
		}
	}
	sort.Strings(violations)
	return violations
}

// authorizationHelperShape reports how an exported Authorize or
// Authorize<Concept> function departs from the canonical decision's shape, or
// "" when it does not, or is not an authorization helper.
func authorizationHelperShape(function *types.Func, module string) string {
	name := function.Name()
	if name != "Authorize" && (!strings.HasPrefix(name, "Authorize") || !isUpperASCII(name[len("Authorize")])) {
		return ""
	}
	signature := function.Type().(*types.Signature)
	qualifier := func(p *types.Package) string { return p.Name() }
	parameters := signature.Params()
	if parameters.Len() < 2 || types.TypeString(parameters.At(0).Type(), nil) != "context.Context" || !isInvocationType(parameters.At(1).Type(), module) {
		return "takes " + tupleString(parameters, qualifier) + "; take (context.Context, invocation.Invocation, ...)"
	}
	results := signature.Results()
	switch {
	case results.Len() == 1 && isErrorType(results.At(0).Type()):
		return ""
	case results.Len() == 2 && isNamedStruct(results.At(0).Type()) && isErrorType(results.At(1).Type()):
		return ""
	}
	return "returns " + tupleString(results, qualifier) + "; return error, or (R, error) with R a named struct result"
}

func isUpperASCII(character byte) bool { return character >= 'A' && character <= 'Z' }

func isErrorType(candidate types.Type) bool {
	return types.Identical(candidate, types.Universe.Lookup("error").Type())
}

func isInvocationType(candidate types.Type, module string) bool {
	named, ok := types.Unalias(candidate).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == module+"/app/invocation" && named.Obj().Name() == "Invocation"
}

func isNamedStruct(candidate types.Type) bool {
	if pointer, ok := candidate.(*types.Pointer); ok {
		candidate = pointer.Elem()
	}
	named, ok := types.Unalias(candidate).(*types.Named)
	if !ok {
		return false
	}
	_, ok = named.Underlying().(*types.Struct)
	return ok
}

func tupleString(tuple *types.Tuple, qualifier types.Qualifier) string {
	parts := make([]string, 0, tuple.Len())
	for variable := range tuple.Variables() {
		parts = append(parts, types.TypeString(variable.Type(), qualifier))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
