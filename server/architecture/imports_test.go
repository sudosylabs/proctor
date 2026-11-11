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

type importViolation struct {
	file       string
	importPath string
}

func (v importViolation) key() string {
	return v.file + "\t" + v.importPath
}

func TestDependencyPolicyRejectsForbiddenImports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     string
		imported string
	}{
		{name: "domain cannot import HTTP", from: serverModule + "/model", imported: "net/http"},
		{name: "domain cannot import PostgreSQL client", from: serverModule + "/model", imported: "github.com/jackc/pgx/v5"},
		{name: "identity-provider constraints cannot import domain", from: serverModule + "/identityprovider", imported: serverModule + "/model"},
		{name: "identity-provider constraints cannot import configuration", from: serverModule + "/identityprovider", imported: serverModule + "/config"},
		{name: "model build tools cannot import server code", from: serverModule + "/model/internal/idgen", imported: serverModule + "/app"},
		{name: "model build tools cannot import third-party code", from: serverModule + "/model/internal/idgen", imported: "golang.org/x/tools/go/packages"},
		{name: "ACME adapter cannot import application", from: serverModule + "/internal/autocert", imported: serverModule + "/app"},
		{name: "ACME adapter cannot import unrelated third-party code", from: serverModule + "/internal/autocert", imported: "github.com/gorilla/mux"},
		{name: "OpenAPI compiler cannot use the process filesystem", from: serverModule + "/internal/openapidoc", imported: "os"},
		{name: "OpenAPI compiler cannot import HTTP", from: serverModule + "/internal/openapidoc", imported: "net/http"},
		{name: "store contracts cannot import SQL adapters", from: serverModule + "/store", imported: serverModule + "/store/sqlstore"},
		{name: "application cannot import platform", from: serverModule + "/app", imported: serverModule + "/platform"},
		{name: "Exam child cannot import parent application", from: serverModule + "/app/exam", imported: serverModule + "/app"},
		{name: "Exam child cannot import HTTP transport", from: serverModule + "/app/exam", imported: serverModule + "/httpapi"},
		{name: "Exam child cannot import platform", from: serverModule + "/app/exam", imported: serverModule + "/platform"},
		{name: "Exam child cannot import SQL adapter", from: serverModule + "/app/exam", imported: serverModule + "/store/sqlstore"},
		{name: "Realtime child cannot import parent application", from: serverModule + "/app/realtime", imported: serverModule + "/app"},
		{name: "Realtime child cannot import persistence", from: serverModule + "/app/realtime", imported: serverModule + "/store"},
		{name: "Realtime child cannot import WebSocket", from: serverModule + "/app/realtime", imported: serverModule + "/websocket"},
		{name: "Realtime child cannot import platform", from: serverModule + "/app/realtime", imported: serverModule + "/platform"},
		{name: "Exam authoring cannot import Mail child", from: serverModule + "/app/exam", imported: serverModule + "/app/mail"},
		{name: "Exam correction cannot import Mail child", from: serverModule + "/app/exam/correction", imported: serverModule + "/app/mail"},
		{name: "Exam Attempt cannot import Exam Sitting", from: serverModule + "/app/exam/attempt", imported: serverModule + "/app/exam/sitting"},
		{name: "Exam review cannot import Exam Sitting", from: serverModule + "/app/exam/review", imported: serverModule + "/app/exam/sitting"},
		{name: "Access Control cannot import an Exam area", from: serverModule + "/app/access", imported: serverModule + "/app/exam/sitting"},
		{name: "Mail child cannot import parent application", from: serverModule + "/app/mail", imported: serverModule + "/app"},
		{name: "Mail child cannot import HTTP transport", from: serverModule + "/app/mail", imported: serverModule + "/httpapi"},
		{name: "Mail child cannot import platform", from: serverModule + "/app/mail", imported: serverModule + "/platform"},
		{name: "Mail child cannot import SQL adapter", from: serverModule + "/app/mail", imported: serverModule + "/store/sqlstore"},
		{name: "Mail child cannot import the identity area", from: serverModule + "/app/mail", imported: serverModule + "/app/identity"},
		{name: "Mail child cannot import concrete Jobs", from: serverModule + "/app/mail", imported: serverModule + "/app/jobs"},
		{name: "Job engine cannot import parent application", from: serverModule + "/app/job", imported: serverModule + "/app"},
		{name: "Job engine cannot import the identity area", from: serverModule + "/app/job", imported: serverModule + "/app/identity"},
		{name: "Job engine cannot import Mail child", from: serverModule + "/app/job", imported: serverModule + "/app/mail"},
		{name: "Job engine cannot import concrete Jobs", from: serverModule + "/app/job", imported: serverModule + "/app/jobs"},
		{name: "Search cannot import parent application", from: serverModule + "/app/search", imported: serverModule + "/app"},
		{name: "Search cannot import HTTP transport", from: serverModule + "/app/search", imported: serverModule + "/httpapi"},
		{name: "Search cannot import the identity area", from: serverModule + "/app/search", imported: serverModule + "/app/identity"},
		{name: "application cannot import Redis client", from: serverModule + "/app", imported: "github.com/redis/go-redis/v9"},
		{name: "Job engine descendants cannot import application", from: serverModule + "/app/job/internal", imported: serverModule + "/app"},
		{name: "Job engine cannot import HTTP transport", from: serverModule + "/app/job", imported: serverModule + "/httpapi"},
		{name: "Job engine cannot import platform", from: serverModule + "/app/job", imported: serverModule + "/platform"},
		{name: "Job engine cannot import SQL adapter", from: serverModule + "/app/job", imported: serverModule + "/store/sqlstore"},
		{name: "Job engine descendants cannot import Argon2", from: serverModule + "/app/job/internal", imported: "golang.org/x/crypto/argon2"},
		{name: "concrete Jobs cannot import parent application", from: serverModule + "/app/jobs", imported: serverModule + "/app"},
		{name: "concrete Jobs cannot import HTTP transport", from: serverModule + "/app/jobs", imported: serverModule + "/httpapi"},
		{name: "concrete Jobs cannot import platform", from: serverModule + "/app/jobs", imported: serverModule + "/platform"},
		{name: "concrete Jobs cannot import SQL adapter", from: serverModule + "/app/jobs", imported: serverModule + "/store/sqlstore"},
		{name: "File Content cannot import persistence", from: serverModule + "/filecontent", imported: serverModule + "/store"},
		{name: "File Content cannot import HTTP", from: serverModule + "/filecontent", imported: "net/http"},
		{name: "File Content cannot import WebSocket", from: serverModule + "/filecontent", imported: serverModule + "/websocket"},
		{name: "File Content cannot locate platform services", from: serverModule + "/filecontent", imported: serverModule + "/platform"},
		{name: "File Content cannot select configuration", from: serverModule + "/filecontent", imported: serverModule + "/config"},
		{name: "File Content cannot run Jobs", from: serverModule + "/filecontent", imported: serverModule + "/app/job"},
		{name: "File Content cannot select a VFS backend", from: serverModule + "/filecontent", imported: repositoryModule + "/packages/vfs/local"},
		{name: "File Content cannot select S3", from: serverModule + "/filecontent", imported: repositoryModule + "/packages/vfs/s3"},
		{name: "HTTP cannot import SQL persistence", from: serverModule + "/httpapi", imported: serverModule + "/store/sqlstore"},
		{name: "HTTP cannot import platform services", from: serverModule + "/httpapi", imported: serverModule + "/platform"},
		{name: "HTTP cannot import SQL driver", from: serverModule + "/httpapi", imported: "database/sql"},
		{name: "SQL adapter cannot import application policy", from: serverModule + "/store/sqlstore", imported: serverModule + "/app"},
		{name: "metrics cannot import application policy", from: serverModule + "/metrics", imported: serverModule + "/app"},
		{name: "metrics cannot import concrete infrastructure", from: serverModule + "/metrics", imported: repositoryModule + "/packages/cache/redis"},
		{name: "metrics cannot import an application area", from: serverModule + "/metrics", imported: serverModule + "/app/mail"},
		{name: "metrics cannot import the Job engine", from: serverModule + "/metrics", imported: serverModule + "/app/job"},
		{name: "metrics cannot select a VFS backend", from: serverModule + "/metrics", imported: repositoryModule + "/packages/vfs/s3"},
		{name: "metrics cannot select a mail transport", from: serverModule + "/metrics", imported: repositoryModule + "/packages/mail/smtp"},
		{name: "metrics cannot select a cluster adapter", from: serverModule + "/metrics", imported: serverModule + "/cluster/memberlist"},
		{name: "metrics cannot import external-auth adapters", from: serverModule + "/metrics", imported: serverModule + "/platform/externalauth"},
		{name: "metrics cannot import the composition root", from: serverModule + "/metrics", imported: serverModule},
		{name: "tracing cannot import the store timing layer", from: serverModule + "/tracing", imported: serverModule + "/store/timerlayer"},
		{name: "tracing cannot import the Job engine", from: serverModule + "/tracing", imported: serverModule + "/app/job"},
		{name: "platform cannot import metrics", from: serverModule + "/platform", imported: serverModule + "/metrics"},
		{name: "tracing cannot import application policy", from: serverModule + "/tracing", imported: serverModule + "/app"},
		{name: "tracing cannot import HTTP", from: serverModule + "/tracing", imported: serverModule + "/httpapi"},
		{name: "HTTP cannot import OpenTelemetry", from: serverModule + "/httpapi", imported: "go.opentelemetry.io/otel/trace"},
		{name: "Job engine cannot import OpenTelemetry", from: serverModule + "/app/job", imported: "go.opentelemetry.io/otel/trace"},
		{name: "store layer cannot import application policy", from: serverModule + "/store/retrylayer", imported: serverModule + "/app"},
		{name: "cluster adapter cannot import platform", from: serverModule + "/cluster/memberlist", imported: serverModule + "/platform"},
		{name: "cluster adapter cannot import persistence", from: serverModule + "/cluster/memberlist", imported: serverModule + "/store"},
		{name: "external adapter cannot import HTTP", from: serverModule + "/platform/externalauth/oidc", imported: serverModule + "/httpapi"},
		{name: "platform cannot select concrete adapters", from: serverModule + "/platform", imported: "github.com/sudosylabs/proctor/packages/cache/redis"},
		{name: "operator executable cannot bypass its command tree", from: serverModule + "/cmd/proctor", imported: serverModule},
		{name: "command tree cannot bypass root composition", from: serverModule + "/cmd/proctor/commands", imported: serverModule + "/app"},
		{name: "command tree cannot open SQL directly", from: serverModule + "/cmd/proctor/commands", imported: "database/sql"},
		{name: "unknown operator child package fails closed", from: serverModule + "/cmd/proctor/helpers", imported: "context"},
		{name: "offline operator commands cannot import the composition root", from: serverModule + "/operator", imported: serverModule},
		{name: "offline operator commands cannot import application", from: serverModule + "/operator", imported: serverModule + "/app"},
		{name: "offline operator commands cannot import an application area", from: serverModule + "/operator", imported: serverModule + "/app/identity"},
		{name: "offline operator commands cannot import HTTP transport", from: serverModule + "/operator", imported: serverModule + "/httpapi"},
		{name: "offline operator commands cannot import WebSocket", from: serverModule + "/operator", imported: serverModule + "/websocket"},
		{name: "offline operator commands cannot import platform", from: serverModule + "/operator", imported: serverModule + "/platform"},
		{name: "offline operator commands cannot open SQL directly", from: serverModule + "/operator", imported: "database/sql"},
		{name: "offline operator commands cannot import a SQL driver", from: serverModule + "/operator", imported: "github.com/jackc/pgx/v5"},
		{name: "offline operator child package fails closed", from: serverModule + "/operator/internal", imported: "context"},
		{name: "command tree cannot open the SQL store itself", from: serverModule + "/cmd/proctor/commands", imported: serverModule + "/store/sqlstore"},
		{name: "localization cannot import application", from: serverModule + "/localization", imported: serverModule + "/app"},
		{name: "mail rendering cannot import transport", from: serverModule + "/app/mail", imported: repositoryModule + "/packages/mail"},
		{name: "mail preview cannot import application", from: serverModule + "/cmd/mailpreview", imported: serverModule + "/app"},
		{name: "reusable module cannot import server", from: repositoryModule + "/packages/cache", imported: serverModule + "/model"},
		{name: "unknown package is fail closed", from: serverModule + "/services", imported: "context"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, forbiddenImport(tt.from, tt.imported), "forbiddenImport(%q, %q) = false, want true", tt.from, tt.imported)
		})
	}
}

func TestDependencyPolicyAllowsInwardImports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     string
		imported string
	}{
		{name: "store contracts may import domain", from: serverModule + "/store", imported: serverModule + "/model"},
		{name: "domain may import identity-provider constraints", from: serverModule + "/model", imported: serverModule + "/identityprovider"},
		{name: "domain may canonicalize internationalized host names", from: serverModule + "/model", imported: "golang.org/x/net/idna"},
		{name: "configuration may import identity-provider constraints", from: serverModule + "/config", imported: serverModule + "/identityprovider"},
		{name: "application may import store contracts", from: serverModule + "/app", imported: serverModule + "/store"},
		{name: "application may import Job engine", from: serverModule + "/app", imported: serverModule + "/app/job"},
		{name: "application may import concrete Jobs", from: serverModule + "/app", imported: serverModule + "/app/jobs"},
		{name: "application may import Realtime child", from: serverModule + "/app", imported: serverModule + "/app/realtime"},
		{name: "application may import Exam child", from: serverModule + "/app", imported: serverModule + "/app/exam"},
		{name: "application may import Exam resource child", from: serverModule + "/app", imported: serverModule + "/app/exam/resource"},
		{name: "application may import Exam workspace child", from: serverModule + "/app", imported: serverModule + "/app/exam/workspace"},
		{name: "application may import Mail child", from: serverModule + "/app", imported: serverModule + "/app/mail"},
		{name: "Mail child may import domain models", from: serverModule + "/app/mail", imported: serverModule + "/model"},
		{name: "Mail child may import store contracts", from: serverModule + "/app/mail", imported: serverModule + "/store"},
		{name: "Mail child may import secret sealing", from: serverModule + "/app/mail", imported: serverModule + "/secretseal"},
		{name: "Mail child may import Exam mail contracts", from: serverModule + "/app/mail", imported: serverModule + "/app/exam"},
		{name: "Mail child may import localization", from: serverModule + "/app/mail", imported: serverModule + "/localization"},
		{name: "Mail operator service may authorize and audit", from: serverModule + "/app/mail", imported: serverModule + "/app/access"},
		{name: "Mail operator service may decode its rekey Job documents", from: serverModule + "/app/mail", imported: serverModule + "/app/job"},
		{name: "Job operator service may authorize and audit", from: serverModule + "/app/job", imported: serverModule + "/app/access"},
		{name: "application may import Search", from: serverModule + "/app", imported: serverModule + "/app/search"},
		{name: "Search may import the Exam catalog", from: serverModule + "/app/search", imported: serverModule + "/app/exam"},
		{name: "Search may import the User directory", from: serverModule + "/app/search", imported: serverModule + "/app/user"},
		{name: "HTTP may name operator mail results", from: serverModule + "/httpapi", imported: serverModule + "/app/mail"},
		{name: "HTTP may name search results", from: serverModule + "/httpapi", imported: serverModule + "/app/search"},
		{name: "Mail child may accept presentation filesystems", from: serverModule + "/app/mail", imported: "io/fs"},
		{name: "Exam child may import domain models", from: serverModule + "/app/exam", imported: serverModule + "/model"},
		{name: "Exam child may import store contracts", from: serverModule + "/app/exam", imported: serverModule + "/store"},
		{name: "Exam use cases may import the mutation audit vocabulary", from: serverModule + "/app/exam/attempt", imported: serverModule + "/app/access"},
		{name: "Exam children may import shared safe Markdown", from: serverModule + "/app/exam/review", imported: serverModule + "/app/exam/safemarkdown"},
		{name: "Exam Sitting may compose its schedule mail", from: serverModule + "/app/exam/sitting", imported: serverModule + "/app/mail"},
		{name: "Exam Attempt may compose its Submission receipt", from: serverModule + "/app/exam/attempt", imported: serverModule + "/app/mail"},
		{name: "Exam review may compose its result-release notice", from: serverModule + "/app/exam/review", imported: serverModule + "/app/mail"},
		{name: "Exam Sitting keeps the Exam Manager access rule", from: serverModule + "/app/exam/sitting", imported: serverModule + "/app/exam/manageraccess"},
		{name: "Realtime child may import domain models", from: serverModule + "/app/realtime", imported: serverModule + "/model"},
		{name: "Job engine may import store contracts", from: serverModule + "/app/job", imported: serverModule + "/store"},
		{name: "concrete Jobs may import Job engine", from: serverModule + "/app/jobs", imported: serverModule + "/app/job"},
		{name: "concrete Jobs may import Mail capabilities", from: serverModule + "/app/jobs", imported: serverModule + "/app/mail"},
		{name: "concrete Jobs may import Exam capabilities", from: serverModule + "/app/jobs", imported: serverModule + "/app/exam/attempt"},
		{name: "File Content may import domain models", from: serverModule + "/filecontent", imported: serverModule + "/model"},
		{name: "File Content may import application content contracts", from: serverModule + "/filecontent", imported: serverModule + "/app/user"},
		{name: "File Content may import VFS contracts", from: serverModule + "/filecontent", imported: repositoryModule + "/packages/vfs"},
		{name: "File Content may import the canonical encoder", from: serverModule + "/filecontent", imported: "github.com/HugoSmits86/nativewebp"},
		{name: "File Content may import bounded image transforms", from: serverModule + "/filecontent", imported: "github.com/disintegration/imaging"},
		{name: "File Content may register supported image decoders", from: serverModule + "/filecontent", imported: "golang.org/x/image/webp"},
		{name: "File Content may structurally validate PDFs", from: serverModule + "/filecontent", imported: "github.com/pdfcpu/pdfcpu/pkg/api"},
		{name: "File Content may use private temporary spools", from: serverModule + "/filecontent", imported: "os"},
		{name: "HTTP may import application", from: serverModule + "/httpapi", imported: serverModule + "/app"},
		{name: "WebSocket may import Realtime child", from: serverModule + "/websocket", imported: serverModule + "/app/realtime"},
		{name: "HTTP may import router library", from: serverModule + "/httpapi", imported: "github.com/gorilla/mux"},
		{name: "SQL adapter may import store contracts", from: serverModule + "/store/sqlstore", imported: serverModule + "/store"},
		{name: "metrics may import timer recorder", from: serverModule + "/metrics", imported: serverModule + "/store/timerlayer"},
		{name: "metrics may import Prometheus", from: serverModule + "/metrics", imported: "github.com/prometheus/client_golang/prometheus"},
		{name: "metrics may wrap the platform cache and mailer ports", from: serverModule + "/metrics", imported: serverModule + "/platform"},
		{name: "metrics may wrap the cluster transport contract", from: serverModule + "/metrics", imported: serverModule + "/cluster"},
		{name: "metrics may wrap the VFS contract", from: serverModule + "/metrics", imported: repositoryModule + "/packages/vfs"},
		{name: "metrics may name mail transport messages", from: serverModule + "/metrics", imported: repositoryModule + "/packages/mail"},
		{name: "tracing may import OpenTelemetry", from: serverModule + "/tracing", imported: "go.opentelemetry.io/otel/sdk/trace"},
		{name: "cache store layer may import cache contract", from: serverModule + "/store/localcachelayer", imported: repositoryModule + "/packages/cache"},
		{name: "cluster adapter may import cluster contracts", from: serverModule + "/cluster/memberlist", imported: serverModule + "/cluster"},
		{name: "application may import password hashing library", from: serverModule + "/app", imported: "golang.org/x/crypto/argon2"},
		{name: "root composition may import platform", from: serverModule, imported: serverModule + "/platform"},
		{name: "operator executable may import its command tree", from: serverModule + "/cmd/proctor", imported: serverModule + "/cmd/proctor/commands"},
		{name: "operator command tree may import the root server facade", from: serverModule + "/cmd/proctor/commands", imported: serverModule},
		{name: "operator command tree may import localization", from: serverModule + "/cmd/proctor/commands", imported: serverModule + "/localization"},
		{name: "operator command tree may import the offline operator commands", from: serverModule + "/cmd/proctor/commands", imported: serverModule + "/operator"},
		{name: "offline operator commands may import configuration", from: serverModule + "/operator", imported: serverModule + "/config"},
		{name: "offline operator commands may import the SQL store and migrator", from: serverModule + "/operator", imported: serverModule + "/store/sqlstore"},
		{name: "offline operator commands may import store contracts", from: serverModule + "/operator", imported: serverModule + "/store"},
		{name: "offline operator commands may import domain models", from: serverModule + "/operator", imported: serverModule + "/model"},
		{name: "operator command tree may import Cobra", from: serverModule + "/cmd/proctor/commands", imported: "github.com/spf13/cobra"},
		{name: "operator command tree may read the monotonic clock", from: serverModule + "/cmd/proctor/commands", imported: "golang.org/x/sys/unix"},
		{name: "mail preview may import mail rendering", from: serverModule + "/cmd/mailpreview", imported: serverModule + "/app/mail"},
		{name: "mail preview may import localization", from: serverModule + "/cmd/mailpreview", imported: serverModule + "/localization"},
		{name: "standard library remains available", from: serverModule + "/app", imported: "context"},
		{name: "model build tools may import standard library", from: serverModule + "/model/internal/idgen", imported: "go/format"},
		{name: "ACME adapter may import ACME protocol", from: serverModule + "/internal/autocert", imported: "golang.org/x/crypto/acme"},
		{name: "ACME adapter may canonicalize IDNs", from: serverModule + "/internal/autocert", imported: "golang.org/x/net/idna"},
		{name: "OpenAPI compiler may accept a filesystem", from: serverModule + "/internal/openapidoc", imported: "io/fs"},
		{name: "OpenAPI compiler may validate OpenAPI", from: serverModule + "/internal/openapidoc", imported: "github.com/getkin/kin-openapi/openapi3"},
		{name: "ptool may import the OpenAPI compiler", from: serverModule + "/cmd/ptool/commands", imported: serverModule + "/internal/openapidoc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.False(t, forbiddenImport(tt.from, tt.imported), "forbiddenImport(%q, %q) = true, want false", tt.from, tt.imported)
		})
	}
}

func TestProductionImportsMatchDependencyPolicy(t *testing.T) {
	violations := productionImportViolations(t)
	keys := make([]string, 0, len(violations))
	for key := range violations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		violation := violations[key]
		assert.Fail(t, fmt.Sprintf("forbidden production import: %s imports %s", violation.file, violation.importPath))
	}
}

func productionImportViolations(t *testing.T) map[string]importViolation {
	t.Helper()

	violations := make(map[string]importViolation)
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
		t.Run(fmt.Sprintf("root=%v", module.root), func(t *testing.T) {
			if _, err := os.Stat(module.root); err != nil {
				if os.IsNotExist(err) {
					return
				}
				require.NoError(t, err, "inspect module root %q", module.root)
			}
			require.NoError(t, scanProductionImports(module.root, module.modulePath, module.filePrefix, violations))
		})
	}
	return violations
}

func scanProductionImports(moduleRoot, modulePath, filePrefix string, violations map[string]importViolation) error {
	return filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
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
		if !knownProductionPackage(packagePath) {
			violation := importViolation{file: filepath.ToSlash(relativeFile), importPath: "<unclassified production package>"}
			if filePrefix != "" {
				violation.file = filepath.ToSlash(filepath.Join(filePrefix, relativeFile))
			}
			violations[violation.key()] = violation
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
			if forbiddenImport(packagePath, importPath) {
				file := filepath.ToSlash(relativeFile)
				if filePrefix != "" {
					file = filepath.ToSlash(filepath.Join(filePrefix, relativeFile))
				}
				violation := importViolation{file: file, importPath: importPath}
				violations[violation.key()] = violation
			}
		}
		return nil
	})
}
