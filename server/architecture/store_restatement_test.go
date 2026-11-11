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
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// storeRestatements lists the application interfaces that may repeat part of
// a store contract, each with the reason it cannot hold the contract itself.
var storeRestatements = map[string]string{
	"app/realtime.SittingCandidates": "app/realtime may not import store; the parent passes the Exam Sitting store through this one-method port",

	// These three predate the rule and are not exceptions: each package
	// already imports store and can hold the contract. Replace the interface
	// with the contract, then remove its entry.
	"app/access.academicPeriodScopeReader": "predates the rule; hold store.AcademicPeriodStore",
	"app/exam/attempt.Sittings":            "predates the rule; hold store.ExamSittingStore",
	"app.RealtimeService.attempts":         "predates the rule; hold store.ExamAttemptStore",
}

// TestApplicationInterfacesDoNotRestateStoreContracts keeps one declaration
// of every persistence contract. An application service holds the store
// contract it calls and its tests fake that contract by embedding it; an
// application interface whose every method is copied from one store.*Store
// contract is a second declaration that drifts from the first.
func TestApplicationInterfacesDoNotRestateStoreContracts(t *testing.T) {
	t.Parallel()
	restatements := storeRestatementViolations(typedServerPackages(t), serverModule)
	var violations []string
	for name, violation := range restatements {
		if _, allowed := storeRestatements[name]; !allowed {
			violations = append(violations, violation)
		}
	}
	for name := range storeRestatements {
		if _, found := restatements[name]; !found {
			violations = append(violations, name+" is listed in storeRestatements but no longer restates a store contract; remove the entry")
		}
	}
	sort.Strings(violations)
	require.Empty(t, violations)
}

func TestStoreRestatementRuleRejectsPlantedViolations(t *testing.T) {
	t.Parallel()
	loaded := loadFixturePackages(t, map[string]string{
		"store/store.go": `package store

import "context"

type User struct{}

type UserStore interface {
	Get(ctx context.Context, id string) (*User, error)
	Delete(ctx context.Context, id string) error
}

type SessionStore interface {
	Revoke(ctx context.Context, id string) error
}

type PasswordMatcher interface {
	Match(password string) bool
}
`,
		"app/user/user.go": `package user

import (
	"context"

	"fixture/store"
)

type userReader interface {
	Get(context.Context, string) (*store.User, error)
}

type Users interface {
	store.UserStore
}

type sessions interface {
	Revoke(ctx context.Context, sessionID string) error
}

type differentSignature interface {
	Get(ctx context.Context, id int) (*store.User, error)
}

type twoContracts interface {
	Delete(ctx context.Context, id string) error
	Revoke(ctx context.Context, id string) error
}

type widened interface {
	Get(ctx context.Context, id string) (*store.User, error)
	Notify(ctx context.Context) error
}

type matcher interface {
	Match(password string) bool
}

type marker interface{}

type service struct {
	users interface {
		Delete(ctx context.Context, id string) error
	}
}
`,
		"httpapi/handler.go": `package httpapi

import "context"

type sessions interface {
	Revoke(ctx context.Context, id string) error
}
`,
	})
	restatements := storeRestatementViolations(loaded, "fixture")
	violations := make([]string, 0, len(restatements))
	for _, violation := range restatements {
		violations = append(violations, violation)
	}
	sort.Strings(violations)
	require.Equal(t, []string{
		"user.go:13: app/user.Users restates store.UserStore (Delete, Get); hold the store contract and fake it in tests by embedding it",
		"user.go:17: app/user.sessions restates store.SessionStore (Revoke); hold the store contract and fake it in tests by embedding it",
		"user.go:42: app/user.service.users restates store.UserStore (Delete); hold the store contract and fake it in tests by embedding it",
		"user.go:9: app/user.userReader restates store.UserStore (Get); hold the store contract and fake it in tests by embedding it",
	}, violations)
}

// storeRestatementViolations reports, keyed by "<package>.<name>", every
// interface declared in a production file of app or a child package whose
// non-empty method set is a subset of one store.*Store contract's method set
// with identical signatures. An interface written as a struct field's type is
// named for its field. The interface of a type assertion is a capability
// probe, not a declaration, and is not examined.
func storeRestatementViolations(loaded []*packages.Package, module string) map[string]string {
	contracts := make(map[string]*types.Interface)
	for _, pkg := range loaded {
		if pkg.PkgPath != module+"/store" {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			typeName, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || !strings.HasSuffix(name, "Store") {
				continue
			}
			if contract, isInterface := typeName.Type().Underlying().(*types.Interface); isInterface {
				contracts[name] = contract
			}
		}
	}
	contractNames := make([]string, 0, len(contracts))
	for name := range contracts {
		contractNames = append(contractNames, name)
	}
	sort.Strings(contractNames)

	violations := make(map[string]string)
	for _, pkg := range loaded {
		if !packageInSubtree(pkg.PkgPath, module+"/app") {
			continue
		}
		relative := strings.TrimPrefix(pkg.PkgPath, module+"/")
		for _, file := range pkg.Syntax {
			for written, name := range declaredInterfaceNames(file) {
				candidate, ok := pkg.TypesInfo.TypeOf(written).(*types.Interface)
				if !ok || candidate.NumMethods() == 0 {
					continue
				}
				name = relative + "." + name
				for _, contractName := range contractNames {
					if methods, restated := restatedMethods(candidate, contracts[contractName]); restated {
						violations[name] = fmt.Sprintf("%s: %s restates store.%s (%s); hold the store contract and fake it in tests by embedding it",
							sourcePosition(pkg, written.Pos()), name, contractName, strings.Join(methods, ", "))
						break
					}
				}
			}
		}
	}
	return violations
}

// restatedMethods reports whether contract declares every method of candidate
// with an identical signature, and names those methods.
func restatedMethods(candidate, contract *types.Interface) ([]string, bool) {
	names := make([]string, 0, candidate.NumMethods())
	for method := range candidate.Methods() {
		declared, _, _ := types.LookupFieldOrMethod(contract, false, method.Pkg(), method.Name())
		if declared == nil || !types.Identical(declared.Type(), method.Type()) {
			return nil, false
		}
		names = append(names, method.Name())
	}
	return names, true
}

// declaredInterfaceNames names the interfaces a file declares as a type
// ("Users") or writes as a struct field's type ("service.users").
func declaredInterfaceNames(file *ast.File) map[*ast.InterfaceType]string {
	names := make(map[*ast.InterfaceType]string)
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok {
			return true
		}
		switch declared := spec.Type.(type) {
		case *ast.InterfaceType:
			names[declared] = spec.Name.Name
		case *ast.StructType:
			for _, field := range declared.Fields.List {
				written, isInterface := field.Type.(*ast.InterfaceType)
				if isInterface && len(field.Names) > 0 {
					names[written] = spec.Name.Name + "." + field.Names[0].Name
				}
			}
		}
		return true
	})
	return names
}
