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

// TestTransportsNameOnlyStoreResultTypes lets HTTP and WebSocket name the
// query-shaped result types that use cases return from store, while keeping
// persistence out of reach: a transport never names a Store contract
// (interface), a store function, or a store variable.
func TestTransportsNameOnlyStoreResultTypes(t *testing.T) {
	t.Parallel()
	for _, violation := range transportStoreViolations(typedServerPackages(t), serverModule) {
		assert.Fail(t, violation)
	}
}

func TestTransportStoreRuleRejectsPlantedViolations(t *testing.T) {
	t.Parallel()
	loaded := loadFixturePackages(t, map[string]string{
		"store/store.go": `package store

type Summary struct{ Count int }

func (s Summary) Empty() bool { return s.Count == 0 }

type ExamStore interface{ Summary() Summary }

const Limit = 3

var Default = Summary{}

func NewSummary() Summary { return Summary{} }
`,
		"httpapi/handler.go": `package httpapi

import "fixture/store"

func present(summary store.Summary) (bool, int) { return summary.Empty(), store.Limit }

func read(exams store.ExamStore) store.Summary { return exams.Summary() }

func fallback() store.Summary { return store.Default }

func build() store.Summary { return store.NewSummary() }
`,
	})
	require.Equal(t, []string{
		"handler.go:11: fixture/httpapi names store function fixture/store.NewSummary; transports use application use cases, never persistence",
		"handler.go:7: fixture/httpapi names store contract fixture/store.ExamStore; transports use application use cases, never persistence",
		"handler.go:7: fixture/httpapi names store contract method Summary; transports use application use cases, never persistence",
		"handler.go:9: fixture/httpapi names store variable fixture/store.Default; transports use application use cases, never persistence",
	}, transportStoreViolations(loaded, "fixture"))
}

func transportStoreViolations(loaded []*packages.Package, module string) []string {
	storePath := module + "/store"
	var violations []string
	for _, pkg := range loaded {
		if !packageInSubtree(pkg.PkgPath, module+"/httpapi") && !packageInSubtree(pkg.PkgPath, module+"/websocket") {
			continue
		}
		for identifier, object := range pkg.TypesInfo.Uses {
			if object.Pkg() == nil || object.Pkg().Path() != storePath {
				continue
			}
			kind := ""
			switch typed := object.(type) {
			case *types.TypeName:
				if types.IsInterface(typed.Type()) {
					kind = "contract " + storePath + "." + typed.Name()
				}
			case *types.Func:
				signature, _ := typed.Type().(*types.Signature)
				if signature == nil || signature.Recv() == nil {
					kind = "function " + storePath + "." + typed.Name()
				} else if types.IsInterface(signature.Recv().Type()) {
					kind = "contract method " + typed.Name()
				}
			case *types.Var:
				if !typed.IsField() {
					kind = "variable " + storePath + "." + typed.Name()
				}
			}
			if kind == "" {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s: %s names store %s; transports use application use cases, never persistence",
				sourcePosition(pkg, identifier.Pos()), pkg.PkgPath, kind))
		}
	}
	sort.Strings(violations)
	return violations
}

func packageInSubtree(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
