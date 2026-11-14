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

// The planted cases exercise each matcher shape and every security edge. An
// ordinary allowed edge needs no case: the production import graph exercises
// it, and TestDependencyAllowancesAreUsed removes it once nothing imports it.
func TestDependencyPolicyRejectsForbiddenImports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     string
		imported string
	}{
		// Matcher shapes.
		{name: "denied standard library", from: serverModule + "/model", imported: "net/http"},
		{name: "denied standard library subtree", from: serverModule + "/httpapi", imported: "database/sql"},
		{name: "denied standard library beside its exception", from: serverModule + "/internal/openapidoc", imported: "os"},
		{name: "empty rule forbids project imports", from: serverModule + "/model/internal/idgen", imported: serverModule + "/app"},
		{name: "empty rule forbids third-party imports", from: serverModule + "/model", imported: "github.com/jackc/pgx/v5"},
		{name: "exact third-party allowance admits nothing else", from: serverModule + "/internal/autocert", imported: "github.com/gorilla/mux"},
		{name: "exact allowance excludes its children", from: serverModule + "/filecontent", imported: repositoryModule + "/packages/vfs/local"},
		{name: "subtree source governs its descendants", from: serverModule + "/app/job/internal", imported: serverModule + "/app"},
		{name: "allow-within denies the rest of its range", from: serverModule + "/store", imported: serverModule + "/store/sqlstore"},
		{name: "all-except denies descendants", from: serverModule + "/platform", imported: repositoryModule + "/packages/cache/redis"},
		{name: "all-except denies an exact package", from: serverModule + "/platform", imported: serverModule + "/metrics"},
		{name: "reusable module cannot import server", from: repositoryModule + "/packages/cache", imported: serverModule + "/model"},
		{name: "deny-every-import rule", from: serverModule + "/cmd/ptool/helpers", imported: "context"},
		{name: "unknown package fails closed", from: serverModule + "/services", imported: "context"},
		{name: "unknown application child fails closed", from: serverModule + "/app/helpers", imported: "context"},
		{name: "unknown Exam child fails closed", from: serverModule + "/app/exam/attempt/internal", imported: "context"},
		{name: "unknown operator child fails closed", from: serverModule + "/cmd/proctor/helpers", imported: "context"},
		{name: "unknown offline operator child fails closed", from: serverModule + "/operator/internal", imported: "context"},

		// Leaves.
		{name: "domain cannot import configuration", from: serverModule + "/model", imported: serverModule + "/config"},
		{name: "configuration cannot import store contracts", from: serverModule + "/config", imported: serverModule + "/store"},
		{name: "configuration cannot import the application", from: serverModule + "/config", imported: serverModule + "/app"},
		{name: "localization cannot import application", from: serverModule + "/localization", imported: serverModule + "/app"},
		{name: "invocation leaf cannot import store contracts", from: serverModule + "/app/invocation", imported: serverModule + "/store"},
		{name: "safe Markdown imports no server package", from: serverModule + "/app/exam/safemarkdown", imported: serverModule + "/model"},
		{name: "batch item context cannot import an application area", from: serverModule + "/app/internal/batchitem", imported: serverModule + "/app/identity"},

		// Application areas. TestApplicationPackagesPointInward covers the
		// transports, persistence adapters, platform and parent for every
		// application package.
		{name: "Realtime child cannot import persistence", from: serverModule + "/app/realtime", imported: serverModule + "/store"},
		{name: "Realtime child cannot import an application area", from: serverModule + "/app/realtime", imported: serverModule + "/app/identity"},
		{name: "Realtime child cannot import an Exam area", from: serverModule + "/app/realtime", imported: serverModule + "/app/exam/attempt"},
		{name: "Exam authoring cannot import Mail child", from: serverModule + "/app/exam", imported: serverModule + "/app/mail"},
		{name: "Exam correction cannot import Mail child", from: serverModule + "/app/exam/correction", imported: serverModule + "/app/mail"},
		{name: "Exam Attempt cannot import Exam Sitting", from: serverModule + "/app/exam/attempt", imported: serverModule + "/app/exam/sitting"},
		{name: "Exam review cannot import Exam Sitting", from: serverModule + "/app/exam/review", imported: serverModule + "/app/exam/sitting"},
		{name: "Exam Attempt cannot decide Exam Manager eligibility", from: serverModule + "/app/exam/attempt", imported: serverModule + "/app/exam/manageraccess"},
		{name: "Exam Manager eligibility cannot import a use case", from: serverModule + "/app/exam/manageraccess", imported: serverModule + "/app/exam"},
		{name: "Access Control cannot import an Exam area", from: serverModule + "/app/access", imported: serverModule + "/app/exam/sitting"},
		{name: "Mail child cannot import the identity area", from: serverModule + "/app/mail", imported: serverModule + "/app/identity"},
		{name: "Mail child cannot import the Job engine", from: serverModule + "/app/mail", imported: serverModule + "/app/job"},
		{name: "Mail child cannot import concrete Jobs", from: serverModule + "/app/mail", imported: serverModule + "/app/jobs"},
		{name: "mail rendering cannot import transport", from: serverModule + "/app/mail", imported: repositoryModule + "/packages/mail"},
		{name: "Job engine cannot import the identity area", from: serverModule + "/app/job", imported: serverModule + "/app/identity"},
		{name: "Job engine cannot import Mail child", from: serverModule + "/app/job", imported: serverModule + "/app/mail"},
		{name: "Job engine cannot import concrete Jobs", from: serverModule + "/app/job", imported: serverModule + "/app/jobs"},
		{name: "Search cannot import the identity area", from: serverModule + "/app/search", imported: serverModule + "/app/identity"},

		// File content.
		{name: "File Content cannot import persistence", from: serverModule + "/filecontent", imported: serverModule + "/store"},
		{name: "File Content cannot import HTTP", from: serverModule + "/filecontent", imported: "net/http"},
		{name: "File Content cannot locate platform services", from: serverModule + "/filecontent", imported: serverModule + "/platform"},
		{name: "File Content cannot select configuration", from: serverModule + "/filecontent", imported: serverModule + "/config"},
		{name: "File Content cannot run Jobs", from: serverModule + "/filecontent", imported: serverModule + "/app/job"},

		// Transports.
		{name: "HTTP cannot import SQL persistence", from: serverModule + "/httpapi", imported: serverModule + "/store/sqlstore"},
		{name: "HTTP cannot import platform services", from: serverModule + "/httpapi", imported: serverModule + "/platform"},
		{name: "HTTP cannot build a batch item context", from: serverModule + "/httpapi", imported: serverModule + "/app/internal/batchitem"},
		{name: "HTTP cannot import OpenTelemetry", from: serverModule + "/httpapi", imported: "go.opentelemetry.io/otel/trace"},
		{name: "routing kernel cannot import the application parent", from: serverModule + "/httpapi/route", imported: serverModule + "/app"},
		{name: "routing kernel cannot import an area", from: serverModule + "/httpapi/route", imported: serverModule + "/app/academic"},
		{name: "routing kernel cannot import an Exam child", from: serverModule + "/httpapi/route", imported: serverModule + "/app/exam/sitting"},
		{name: "routing kernel cannot import Identity", from: serverModule + "/httpapi/route", imported: serverModule + "/app/identity"},
		{name: "routing kernel cannot import the Attempt module", from: serverModule + "/httpapi/route", imported: serverModule + "/app/exam/attempt"},
		{name: "routing kernel cannot import store contracts", from: serverModule + "/httpapi/route", imported: serverModule + "/store"},
		{name: "routing kernel cannot import its resources", from: serverModule + "/httpapi/route", imported: serverModule + "/httpapi"},
		{name: "routing kernel cannot import the WebSocket transport", from: serverModule + "/httpapi/route", imported: serverModule + "/websocket"},
		{name: "WebSocket cannot import the routing kernel", from: serverModule + "/websocket", imported: serverModule + "/httpapi/route"},
		{name: "hosted webapp cannot import the routing kernel", from: serverModule + "/webui", imported: serverModule + "/httpapi/route"},
		{name: "ptool cannot import the routing kernel", from: serverModule + "/cmd/ptool/commands", imported: serverModule + "/httpapi/route"},

		// Infrastructure.
		{name: "SQL adapter cannot import application policy", from: serverModule + "/store/sqlstore", imported: serverModule + "/app"},
		{name: "store layer cannot import application policy", from: serverModule + "/store/retrylayer", imported: serverModule + "/app"},
		{name: "metrics cannot import application policy", from: serverModule + "/metrics", imported: serverModule + "/app"},
		{name: "metrics cannot import an application area", from: serverModule + "/metrics", imported: serverModule + "/app/mail"},
		{name: "metrics cannot import concrete infrastructure", from: serverModule + "/metrics", imported: repositoryModule + "/packages/cache/redis"},
		{name: "metrics cannot select a VFS backend", from: serverModule + "/metrics", imported: repositoryModule + "/packages/vfs/s3"},
		{name: "metrics cannot select a cluster adapter", from: serverModule + "/metrics", imported: serverModule + "/cluster/memberlist"},
		{name: "metrics cannot import external-auth adapters", from: serverModule + "/metrics", imported: serverModule + "/platform/externalauth"},
		{name: "metrics cannot import the composition root", from: serverModule + "/metrics", imported: serverModule},
		{name: "tracing cannot import application policy", from: serverModule + "/tracing", imported: serverModule + "/app"},
		{name: "tracing cannot import the store timing layer", from: serverModule + "/tracing", imported: serverModule + "/store/timerlayer"},
		{name: "cluster adapter cannot import platform", from: serverModule + "/cluster/memberlist", imported: serverModule + "/platform"},
		{name: "cluster adapter cannot import persistence", from: serverModule + "/cluster/memberlist", imported: serverModule + "/store"},
		{name: "external adapter cannot import HTTP", from: serverModule + "/platform/externalauth", imported: serverModule + "/httpapi"},
		{name: "plugin cannot import HTTP", from: serverModule + "/plugin/oidc", imported: serverModule + "/httpapi"},
		{name: "plugin cannot import the application", from: serverModule + "/plugin/zipexport", imported: serverModule + "/app"},
		{name: "plugin cannot import a Store", from: serverModule + "/plugin/cas", imported: serverModule + "/store"},
		{name: "application cannot import a plugin", from: serverModule + "/app", imported: serverModule + "/plugin/cas"},
		{name: "file content cannot import a plugin", from: serverModule + "/filecontent", imported: serverModule + "/plugin/zipexport"},

		// Executables and commands.
		{name: "operator executable cannot bypass its command tree", from: serverModule + "/cmd/proctor", imported: serverModule},
		{name: "command tree cannot bypass root composition", from: serverModule + "/cmd/proctor/commands", imported: serverModule + "/app"},
		{name: "command tree cannot open SQL directly", from: serverModule + "/cmd/proctor/commands", imported: "database/sql"},
		{name: "command tree cannot open the SQL store itself", from: serverModule + "/cmd/proctor/commands", imported: serverModule + "/store/sqlstore"},
		{name: "offline operator commands cannot import the composition root", from: serverModule + "/operator", imported: serverModule},
		{name: "offline operator commands cannot import application", from: serverModule + "/operator", imported: serverModule + "/app"},
		{name: "offline operator commands cannot import an application area", from: serverModule + "/operator", imported: serverModule + "/app/identity"},
		{name: "offline operator commands cannot import HTTP transport", from: serverModule + "/operator", imported: serverModule + "/httpapi"},
		{name: "offline operator commands cannot import platform", from: serverModule + "/operator", imported: serverModule + "/platform"},
		{name: "offline operator commands cannot open SQL directly", from: serverModule + "/operator", imported: "database/sql"},
		{name: "offline operator commands cannot import a SQL driver", from: serverModule + "/operator", imported: "github.com/jackc/pgx/v5"},
		{name: "mail preview cannot import application", from: serverModule + "/cmd/mailpreview", imported: serverModule + "/app"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, forbiddenImport(tt.from, tt.imported), "forbiddenImport(%q, %q) = false, want true", tt.from, tt.imported)
		})
	}
}

// The allowed cases exercise each matcher shape that admits an import.
func TestDependencyPolicyAllowsInwardImports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     string
		imported string
	}{
		{name: "exact project allowance", from: serverModule + "/config", imported: serverModule + "/model"},
		{name: "exact third-party allowance", from: serverModule + "/internal/autocert", imported: "golang.org/x/crypto/acme"},
		{name: "subtree allowance admits its root", from: serverModule + "/app", imported: serverModule + "/app/exam"},
		{name: "subtree allowance admits a descendant", from: serverModule + "/app", imported: serverModule + "/app/exam/resource"},
		{name: "subtree third-party allowance", from: serverModule + "/metrics", imported: "github.com/prometheus/client_golang/prometheus"},
		{name: "descendants third-party allowance", from: serverModule + "/filecontent", imported: "github.com/pdfcpu/pdfcpu/pkg/api"},
		{name: "allow-within admits its allowance", from: serverModule + "/store", imported: serverModule + "/model"},
		{name: "all-except admits an unlisted package", from: serverModule + "/platform", imported: serverModule + "/config"},
		{name: "all-imports rule", from: serverModule, imported: serverModule + "/platform"},
		{name: "unlisted standard library", from: serverModule + "/app", imported: "context"},
		{name: "standard library exception", from: serverModule + "/filecontent", imported: "os"},
		{name: "test support crosses boundaries", from: serverModule + "/testlib", imported: serverModule + "/store/sqlstore"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.False(t, forbiddenImport(tt.from, tt.imported), "forbiddenImport(%q, %q) = true, want false", tt.from, tt.imported)
		})
	}
}

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

func TestDependencyAllowancesAreUsedRejectsPlantedViolations(t *testing.T) {
	t.Parallel()

	rules := []dependencyRule{
		{
			name:    "specific",
			sources: []pathPattern{exact("example/area/leaf")},
			project: only(exact(serverModule + "/store")),
		},
		{
			name:    "broad",
			sources: []pathPattern{subtree("example/area")},
			project: only(
				exact(serverModule+"/model"),
				exact(serverModule+"/store"),
				subtree(serverModule+"/app/exam"),
				subtree(serverModule+"/app/mail"),
			),
			thirdParty: only(descendants("github.com/gorilla"), exact("golang.org/x/net/idna")),
		},
	}
	imports := map[string][]string{
		"example/area":      {serverModule + "/model", serverModule + "/app/exam/attempt", "golang.org/x/net/idna"},
		"example/area/leaf": {serverModule + "/store"},
	}

	assert.Equal(t, []string{
		`rule "broad" allows ` + serverModule + `/store, which no package it owns imports`,
		`rule "broad" allows ` + serverModule + `/app/mail and below, which no package it owns imports`,
		`rule "broad" allows below github.com/gorilla, which no package it owns imports`,
	}, staleAllowances(rules, imports))
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
