// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProductionImportsFollowPolicy reports every production import in the
// server module that an import rule or the parent rule forbids.
func TestProductionImportsFollowPolicy(t *testing.T) {
	t.Parallel()

	imports, err := productionImports("..")
	require.NoError(t, err, "scan production imports")
	require.NotEmpty(t, imports, "production imports")

	var violations []string
	for _, edge := range imports {
		for _, rule := range importRules {
			if rule.forbids(edge.packagePath, edge.importPath) {
				violations = append(violations, fmt.Sprintf("%s imports %s: %s", edge.file, edge.importPath, rule.reason))
			}
		}
		if importsAncestor(edge.packagePath, edge.importPath) {
			violations = append(violations, fmt.Sprintf("%s imports its parent %s: a child never imports its parent", edge.file, edge.importPath))
		}
	}
	slices.Sort(violations)
	for _, violation := range slices.Compact(violations) {
		assert.Fail(t, violation)
	}
}

// productionImport is one import of one non-test Go file, whatever its
// build constraints.
type productionImport struct {
	file        string
	packagePath string
	importPath  string
}

func productionImports(moduleRoot string) ([]productionImport, error) {
	var imports []productionImport
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != moduleRoot && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return fmt.Errorf("make %q relative to module root: %w", path, err)
		}
		packagePath := serverModule
		if directory := filepath.Dir(file); directory != "." {
			packagePath += "/" + filepath.ToSlash(directory)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse imports from %q: %w", file, err)
		}
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("unquote import in %q: %w", file, err)
			}
			imports = append(imports, productionImport{file: filepath.ToSlash(file), packagePath: packagePath, importPath: importPath})
		}
		return nil
	})
	return imports, err
}
