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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	repositoryModule = "github.com/sudosylabs/proctor"
	serverModule     = repositoryModule + "/server"
)

// TestApplicationPackagesPointInward holds every application package, and an
// application child without its own rule, away from the transports, the
// persistence and infrastructure adapters, the platform and the composition
// root; a child also cannot import its parent.
func TestApplicationPackagesPointInward(t *testing.T) {
	t.Parallel()

	outward := []string{
		serverModule,
		serverModule + "/httpapi",
		serverModule + "/httpapi/route",
		serverModule + "/websocket",
		serverModule + "/webui",
		serverModule + "/store/sqlstore",
		serverModule + "/store/localcachelayer",
		serverModule + "/platform",
		serverModule + "/platform/externalauth",
		serverModule + "/config",
		serverModule + "/cluster",
		serverModule + "/metrics",
		serverModule + "/tracing",
		serverModule + "/filecontent",
		"net/http",
		"database/sql",
		"github.com/jackc/pgx/v5",
		"github.com/redis/go-redis/v9",
	}
	packages := []string{serverModule + "/app", serverModule + "/app/helpers"}
	for _, rule := range dependencyRules {
		for _, source := range rule.sources {
			if !strings.HasPrefix(source.path, serverModule+"/app/") {
				continue
			}
			packagePath := source.path
			if source.match == matchDescendants {
				packagePath += "/helpers"
			}
			packages = append(packages, packagePath)
		}
	}
	require.Contains(t, packages, serverModule+"/app/identity", "application packages")

	for _, packagePath := range packages {
		imports := outward
		if packagePath != serverModule+"/app" {
			imports = append(slices.Clone(outward), serverModule+"/app")
		}
		for _, imported := range imports {
			assert.True(t, forbiddenImport(packagePath, imported), "%s may import %s", packagePath, imported)
		}
	}
}

func TestProductionImportsMatchDependencyPolicy(t *testing.T) {
	var violations []string
	for _, edge := range productionImports(t) {
		if edge.importPath == "" {
			violations = append(violations, fmt.Sprintf("unclassified production package: %s", edge.file))
			continue
		}
		if forbiddenImport(edge.packagePath, edge.importPath) {
			violations = append(violations, fmt.Sprintf("forbidden production import: %s imports %s", edge.file, edge.importPath))
		}
	}
	sort.Strings(violations)
	for _, violation := range slices.Compact(violations) {
		assert.Fail(t, violation)
	}
}

// TestDependencyAllowancesAreUsed fails when a rule allows an import that no
// package the rule owns makes, so an allowance cannot outlive its last use.
func TestDependencyAllowancesAreUsed(t *testing.T) {
	imports := make(map[string][]string)
	for _, edge := range productionImports(t) {
		if edge.importPath != "" {
			imports[edge.packagePath] = append(imports[edge.packagePath], edge.importPath)
		}
	}
	for _, stale := range staleAllowances(dependencyRules, imports) {
		assert.Fail(t, stale)
	}
}

func staleAllowances(rules []dependencyRule, imports map[string][]string) []string {
	owned := make([][]string, len(rules))
	for packagePath, packageImports := range imports {
		for index, rule := range rules {
			if matchesAny(rule.sources, packagePath) {
				owned[index] = append(owned[index], packageImports...)
				break
			}
		}
	}
	var stale []string
	for index, rule := range rules {
		allowances := append(slices.Clone(rule.project.allowed), rule.thirdParty.allowed...)
		for _, allowance := range allowances {
			if !slices.ContainsFunc(owned[index], allowance.matches) {
				stale = append(stale, fmt.Sprintf("rule %q allows %s, which no package it owns imports", rule.name, allowance))
			}
		}
	}
	return stale
}

// productionImport is one import of a production file. An unclassified
// package's files appear once each with an empty importPath.
type productionImport struct {
	file        string
	packagePath string
	importPath  string
}

func productionImports(t *testing.T) []productionImport {
	t.Helper()

	var imports []productionImport
	repositoryRoot := filepath.Clean("../..")
	modules := []struct {
		root       string
		modulePath string
		filePrefix string
	}{
		{root: filepath.Join(repositoryRoot, "server"), modulePath: serverModule},
		{root: filepath.Join(repositoryRoot, "packages", "cache"), modulePath: repositoryModule + "/packages/cache", filePrefix: "packages/cache"},
		{root: filepath.Join(repositoryRoot, "packages", "mail"), modulePath: repositoryModule + "/packages/mail", filePrefix: "packages/mail"},
		{root: filepath.Join(repositoryRoot, "packages", "vfs"), modulePath: repositoryModule + "/packages/vfs", filePrefix: "packages/vfs"},
	}
	for _, module := range modules {
		if _, err := os.Stat(module.root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			require.NoError(t, err, "inspect module root %q", module.root)
		}
		moduleImports, err := scanProductionImports(module.root, module.modulePath, module.filePrefix)
		require.NoError(t, err, "scan production imports under %q", module.root)
		imports = append(imports, moduleImports...)
	}
	return imports
}

func scanProductionImports(moduleRoot, modulePath, filePrefix string) ([]productionImport, error) {
	var imports []productionImport
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != moduleRoot && (entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relativeFile, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return fmt.Errorf("make %q relative to module root: %w", path, err)
		}
		packagePath := modulePath
		if directory := filepath.Dir(relativeFile); directory != "." {
			packagePath += "/" + filepath.ToSlash(directory)
		}
		if exemptProductionHelper(packagePath) {
			return nil
		}
		file := filepath.ToSlash(filepath.Join(filePrefix, relativeFile))
		if !knownProductionPackage(packagePath) {
			imports = append(imports, productionImport{file: file, packagePath: packagePath})
			return nil
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse imports from %q: %w", relativeFile, err)
		}
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("unquote import in %q: %w", relativeFile, err)
			}
			imports = append(imports, productionImport{file: file, packagePath: packagePath, importPath: importPath})
		}
		return nil
	})
	return imports, err
}
