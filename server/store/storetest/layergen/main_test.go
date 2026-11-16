// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package main

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderTypeRejectsUnsupportedContractSyntax(t *testing.T) {
	t.Parallel()

	_, err := renderType(&ast.ChanType{Value: ast.NewIdent("string")}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported store contract type")
}

func TestMethodIdentifierPreservesIDInitialism(t *testing.T) {
	t.Parallel()

	require.Equal(t, "GetAcademicUnitID", methodIdentifier("GetAcademicUnitID"))
	require.Equal(t, "GetByIDs", methodIdentifier("GetByIDs"))
}

func TestParseInterfacesExpandsCatalogAndOrdersByAccessor(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	files := map[string]string{
		"store.go": `package store

type Catalog interface {
	Zebra() ZebraStore
	Apple() AppleStore
}

type Store interface {
	Catalog

	Close() error
}
`,
		"apple.go": "package store\n\ntype AppleStore interface{ Get() error }\n",
		"zebra.go": "package store\n\ntype ZebraStore interface{ Get() error }\n",
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600))
	}
	interfaces, _, err := parseInterfaces(directory)
	require.NoError(t, err)
	var names []string
	for _, spec := range interfaces {
		names = append(names, spec.name)
	}
	require.Equal(t, "Store,ZebraStore,AppleStore", strings.Join(names, ","))
	var methods []string
	for _, method := range interfaces[0].methods {
		methods = append(methods, method.name)
	}
	require.Equal(t, "Zebra,Apple,Close", strings.Join(methods, ","))
}

func TestRetryAllowlistRejectsStaleAndMismatchedEntries(t *testing.T) {
	t.Parallel()

	interfaces := []interfaceSpec{{name: "AppleStore", methods: []methodSpec{{
		name:    "Get",
		params:  []valueSpec{{name: "arg0", typeName: "context.Context"}},
		results: []valueSpec{{name: "result0", typeName: "error"}},
	}}}}
	known := map[string]string{"context": "context", "store": "store", "sync": "sync"}
	tests := map[string]struct {
		allowlist string
		want      string
	}{
		"stale method":        {allowlist: "AppleStore.Gone always\n", want: "AppleStore.Gone is not a store contract method"},
		"stale contract":      {allowlist: "PearStore.Get always\n", want: "PearStore.Get is not a store contract method"},
		"command without one": {allowlist: "AppleStore.Get command\n", want: "takes 0 *store.CommandIdempotency arguments"},
		"unknown mode":        {allowlist: "AppleStore.Get sometimes\n", want: `mode "sometimes"`},
		"duplicate":           {allowlist: "AppleStore.Get always\nAppleStore.Get always # again\n", want: "already listed on line 1"},
		"missing method":      {allowlist: "AppleStore always\n", want: "is not Contract.Method"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "allowlist.txt")
			require.NoError(t, os.WriteFile(path, []byte(test.allowlist), 0o600))
			entries, err := parseAllowlist(path)
			if err == nil {
				_, err = renderRetryMethods(interfaces, entries, known)
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}
