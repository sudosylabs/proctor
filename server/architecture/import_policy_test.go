// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pathMatch uint8

const (
	matchExact pathMatch = iota
	matchSubtree
	matchDescendants
)

type pathPattern struct {
	path  string
	match pathMatch
}

func exact(path string) pathPattern {
	return pathPattern{path: path, match: matchExact}
}

func subtree(path string) pathPattern {
	return pathPattern{path: path, match: matchSubtree}
}

func descendants(path string) pathPattern {
	return pathPattern{path: path, match: matchDescendants}
}

func (p pathPattern) matches(path string) bool {
	switch p.match {
	case matchExact:
		return path == p.path
	case matchSubtree:
		return path == p.path || strings.HasPrefix(path, p.path+"/")
	case matchDescendants:
		return strings.HasPrefix(path, p.path+"/")
	default:
		return false
	}
}

func (p pathPattern) String() string {
	switch p.match {
	case matchSubtree:
		return p.path + " and below"
	case matchDescendants:
		return "below " + p.path
	default:
		return p.path
	}
}

type importAccess struct {
	allowUnlisted bool
	allowed       []pathPattern
	denied        []pathPattern
}

func only(patterns ...pathPattern) importAccess {
	return importAccess{allowed: patterns}
}

func allImports() importAccess {
	return importAccess{allowUnlisted: true}
}

func allExcept(patterns ...pathPattern) importAccess {
	return importAccess{allowUnlisted: true, denied: patterns}
}

func allowWithin(denied []pathPattern, allowed ...pathPattern) importAccess {
	return importAccess{allowUnlisted: true, allowed: allowed, denied: denied}
}

func (a importAccess) forbids(path string) bool {
	if matchesAny(a.allowed, path) {
		return false
	}
	if matchesAny(a.denied, path) {
		return true
	}
	return !a.allowUnlisted
}

type dependencyRule struct {
	name            string
	sources         []pathPattern
	deniedStandard  []pathPattern
	project         importAccess
	thirdParty      importAccess
	denyEveryImport bool
}

func (r dependencyRule) forbids(importPath string) bool {
	if r.denyEveryImport {
		return true
	}
	if projectImport(importPath) {
		return r.project.forbids(importPath)
	}
	if thirdPartyImport(importPath) {
		return r.thirdParty.forbids(importPath)
	}
	return matchesAny(r.deniedStandard, importPath)
}

var (
	standardInfrastructure = []pathPattern{
		exact("net/http"),
		subtree("database/sql"),
		exact("net/smtp"),
		exact("os"),
		exact("io/fs"),
		exact("path/filepath"),
	}
	standardInfrastructureExceptHTTP = []pathPattern{
		subtree("database/sql"),
		exact("net/smtp"),
		exact("os"),
		exact("io/fs"),
		exact("path/filepath"),
	}
	standardInfrastructureExceptFS = []pathPattern{
		exact("net/http"),
		subtree("database/sql"),
		exact("net/smtp"),
		exact("os"),
		exact("path/filepath"),
	}
	standardInfrastructureExceptOS = []pathPattern{
		exact("net/http"),
		subtree("database/sql"),
		exact("net/smtp"),
		exact("io/fs"),
		exact("path/filepath"),
	}
	commandInfrastructure = []pathPattern{
		subtree("database/sql"),
		exact("net/smtp"),
	}
)

// dependencyRules is ordered from the most specific package boundary to the
// broadest. The first matching rule owns a production package.
var dependencyRules = []dependencyRule{
	{
		name: "reusable modules",
		sources: []pathPattern{
			subtree(repositoryModule + "/packages/cache"),
			subtree(repositoryModule + "/packages/mail"),
			subtree(repositoryModule + "/packages/vfs"),
		},
		project:    allExcept(subtree(serverModule)),
		thirdParty: allImports(),
	},
	{
		name:       "composition root",
		sources:    []pathPattern{exact(serverModule)},
		project:    allImports(),
		thirdParty: allImports(),
	},
	{
		name:    "plugin contracts",
		sources: []pathPattern{exact(serverModule + "/plugin")},
	},
	{
		name:    "model build tools",
		sources: []pathPattern{subtree(serverModule + "/model/internal/idgen")},
	},
	{
		name:    "route error-code analysis for tests",
		sources: []pathPattern{exact(serverModule + "/internal/errorflow")},
		thirdParty: only(
			subtree("golang.org/x/tools/go"),
		),
	},
	{
		name:    "static TLS key pair files",
		sources: []pathPattern{exact(serverModule + "/internal/tlscert")},
	},
	{
		name:    "ACME adapter",
		sources: []pathPattern{exact(serverModule + "/internal/autocert")},
		thirdParty: only(
			exact("golang.org/x/crypto/acme"),
			exact("golang.org/x/net/idna"),
		),
	},
	{
		name:           "OpenAPI compiler",
		sources:        []pathPattern{exact(serverModule + "/internal/openapidoc")},
		deniedStandard: standardInfrastructureExceptFS,
		thirdParty: only(
			exact("github.com/getkin/kin-openapi/openapi3"),
			exact("gopkg.in/yaml.v3"),
		),
	},
	{
		// The realtime contract compiler validates JSON Schema with the
		// library kin-openapi already brings into the module graph.
		name:           "AsyncAPI compiler",
		sources:        []pathPattern{exact(serverModule + "/internal/asyncapidoc")},
		deniedStandard: standardInfrastructureExceptFS,
		thirdParty: only(
			exact("github.com/santhosh-tekuri/jsonschema/v6"),
			exact("gopkg.in/yaml.v3"),
		),
	},
	{
		name:           "error catalog compiler",
		sources:        []pathPattern{exact(serverModule + "/internal/errorcatalog")},
		deniedStandard: standardInfrastructureExceptHTTP,
		thirdParty:     only(exact("gopkg.in/yaml.v3")),
	},
	{
		name:           "browser origin serialization",
		sources:        []pathPattern{exact(serverModule + "/internal/weborigin")},
		deniedStandard: standardInfrastructure,
	},
	{
		name:           "canonical agreement JSON",
		sources:        []pathPattern{exact(serverModule + "/internal/canonicaljson")},
		deniedStandard: standardInfrastructure,
	},
	{
		name:           "domain model",
		sources:        []pathPattern{exact(serverModule + "/model")},
		deniedStandard: standardInfrastructure,
		project:        only(exact(serverModule + "/internal/canonicaljson")),
	},
	{
		name:           "store contracts",
		sources:        []pathPattern{exact(serverModule + "/store")},
		deniedStandard: standardInfrastructure,
		project: allowWithin(
			[]pathPattern{descendants(serverModule)},
			exact(serverModule+"/model"),
		),
	},
	{
		name:    "SQL store adapter",
		sources: []pathPattern{subtree(serverModule + "/store/sqlstore")},
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/internal/canonicaljson"),
			exact(serverModule+"/store"),
			exact(serverModule+"/config"),
			exact(serverModule+"/migrations"),
		),
		thirdParty: allImports(),
	},
	{
		name:           "local cache store layer",
		sources:        []pathPattern{subtree(serverModule + "/store/localcachelayer")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(repositoryModule+"/packages/cache"),
		),
	},
	{
		name: "generic store layers",
		sources: []pathPattern{
			subtree(serverModule + "/store/timerlayer"),
			subtree(serverModule + "/store/retrylayer"),
		},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "concrete jobs",
		sources:        []pathPattern{subtree(serverModule + "/app/jobs")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/secretseal"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/job"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/user"),
			exact(serverModule+"/app/exam"),
			exact(serverModule+"/app/exam/attempt"),
			exact(serverModule+"/app/exam/sitting"),
		),
	},
	{
		// The engine needs only logging, model and store. The operator Job
		// service beside it authorizes, audits and reports application errors,
		// so it adds the access, apperror and invocation leaves.
		name:           "job engine",
		sources:        []pathPattern{subtree(serverModule + "/app/job")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/logging"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/invocation"),
		),
	},
	{
		name:           "realtime application",
		sources:        []pathPattern{subtree(serverModule + "/app/realtime")},
		deniedStandard: standardInfrastructure,
		// Delivery stays storage-free: the one Attempt read a Session
		// revocation needs comes through a port the parent adapts.
		project: only(exact(serverModule+"/logging"), exact(serverModule+"/model"), exact(serverModule+"/app/apperror")),
	},
	{
		name:           "mail application",
		sources:        []pathPattern{subtree(serverModule + "/app/mail")},
		deniedStandard: standardInfrastructureExceptFS,
		thirdParty:     only(exact("golang.org/x/net/html")),
		// Composition needs model, store, secretseal, the Exam Manager
		// contract and localization. The operator mail service beside it
		// authorizes, audits, pages and reports application errors.
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/secretseal"),
			exact(serverModule+"/app/exam"),
			exact(serverModule+"/localization"),
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/paging"),
		),
	},
	{
		name:           "application error vocabulary",
		sources:        []pathPattern{exact(serverModule + "/app/apperror")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "invocation application",
		sources:        []pathPattern{exact(serverModule + "/app/invocation")},
		deniedStandard: standardInfrastructure,
		project:        only(exact(serverModule + "/model")),
	},
	{
		name:           "idempotency application",
		sources:        []pathPattern{exact(serverModule + "/app/idempotency")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "exam manager access",
		sources:        []pathPattern{exact(serverModule + "/app/exam/manageraccess")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		// The Sitting area prepares its own schedule mail, so it alone among
		// the Exam Manager access consumers composes through app/mail.
		name:           "exam Sitting application",
		sources:        []pathPattern{exact(serverModule + "/app/exam/sitting")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/exam/manageraccess"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name: "exam manager access consumers",
		sources: []pathPattern{
			exact(serverModule + "/app/exam"),
			exact(serverModule + "/app/exam/correction"),
			exact(serverModule + "/app/exam/resource"),
			exact(serverModule + "/app/exam/workspace"),
		},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/exam/manageraccess"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		// The Attempt and Integrity review areas prepare the Submission
		// receipt and the result-release notice, so they compose through
		// app/mail, and render candidate-visible Markdown through the shared
		// safe Markdown leaf. Neither decides Exam Manager eligibility.
		name: "exam mail and safe Markdown consumers",
		sources: []pathPattern{
			exact(serverModule + "/app/exam/attempt"),
			exact(serverModule + "/app/exam/review"),
		},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/app/exam/safemarkdown"),
		),
	},
	{
		name:           "safe Markdown",
		sources:        []pathPattern{exact(serverModule + "/app/exam/safemarkdown")},
		deniedStandard: standardInfrastructure,
	},
	{
		name:           "file-content adapter",
		sources:        []pathPattern{subtree(serverModule + "/filecontent")},
		deniedStandard: standardInfrastructureExceptOS,
		project: only(
			exact(serverModule+"/app/exam"),
			exact(serverModule+"/app/user"),
			exact(serverModule+"/model"),
			exact(serverModule+"/plugin"),
			exact(repositoryModule+"/packages/vfs"),
		),
		thirdParty: only(
			exact("github.com/HugoSmits86/nativewebp"),
			exact("github.com/disintegration/imaging"),
			descendants("github.com/pdfcpu/pdfcpu"),
			exact("golang.org/x/image/webp"),
		),
	},
	{
		name:           "Desktop release verification",
		sources:        []pathPattern{exact(serverModule + "/desktoprelease")},
		deniedStandard: standardInfrastructure,
		project:        only(exact(serverModule+"/model"), exact(serverModule+"/internal/canonicaljson")),
	},
	{
		name:           "secret sealing",
		sources:        []pathPattern{exact(serverModule + "/secretseal")},
		deniedStandard: standardInfrastructure,
	},
	{
		name:           "localization",
		sources:        []pathPattern{exact(serverModule + "/localization")},
		deniedStandard: standardInfrastructureExceptFS,
		thirdParty: only(
			subtree("github.com/nicksnyder/go-i18n/v2/i18n"),
			exact("golang.org/x/text/language"),
		),
	},
	{
		name:           "access application",
		sources:        []pathPattern{exact(serverModule + "/app/access")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "paging application",
		sources:        []pathPattern{exact(serverModule + "/app/paging")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "academic application",
		sources:        []pathPattern{exact(serverModule + "/app/academic")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/internal/batchitem"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "user application",
		sources:        []pathPattern{exact(serverModule + "/app/user")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "identity application",
		sources:        []pathPattern{exact(serverModule + "/app/identity")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/logging"),
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/exam/attempt"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/internal/batchitem"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/job"),
			exact(serverModule+"/app/jobs"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/app/user"),
			exact(serverModule+"/model"),
			exact(serverModule+"/secretseal"),
			exact(serverModule+"/store"),
		),
		thirdParty: only(exact("golang.org/x/crypto/argon2")),
	},
	{
		name:           "administration application",
		sources:        []pathPattern{exact(serverModule + "/app/administration")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/identity"),
			exact(serverModule+"/app/internal/batchitem"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/jobs"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "onboarding application",
		sources:        []pathPattern{exact(serverModule + "/app/onboarding")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/academic"),
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/administration"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/idempotency"),
			exact(serverModule+"/app/identity"),
			exact(serverModule+"/app/internal/batchitem"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/jobs"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/app/user"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		name:           "search application",
		sources:        []pathPattern{exact(serverModule + "/app/search")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/exam"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/app/user"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		// Only the areas that run batches import the batch item context, and
		// the Go internal rule keeps every transport from building one.
		name:           "batch item context",
		sources:        []pathPattern{exact(serverModule + "/app/internal/batchitem")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
		),
	},
	{
		// The parent alone; an application child without its own rule fails
		// closed.
		name:           "application",
		sources:        []pathPattern{exact(serverModule + "/app")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/logging"),
			exact(serverModule+"/app/academic"),
			exact(serverModule+"/app/access"),
			exact(serverModule+"/app/administration"),
			exact(serverModule+"/app/identity"),
			exact(serverModule+"/app/onboarding"),
			exact(serverModule+"/app/user"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/app/job"),
			exact(serverModule+"/app/jobs"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/search"),
			exact(serverModule+"/secretseal"),
			subtree(serverModule+"/app/exam"),
		),
	},
	{
		name:    "hosted webapp transport",
		sources: []pathPattern{exact(serverModule + "/webui")},
	},
	{
		// The routing kernel serves every resource and knows none. It reaches
		// the application only for the error, invocation and paging leaves;
		// it reads a DPoP nonce challenge and delivery recovery state through
		// small interfaces on the error, and which codes advise Retry-After or
		// carry an extension member from its generated catalog.
		name:           "HTTP routing kernel",
		sources:        []pathPattern{exact(serverModule + "/httpapi/route")},
		deniedStandard: standardInfrastructureExceptHTTP,
		project: only(
			exact(serverModule+"/logging"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/model"),
			exact(serverModule+"/localization"),
			exact(serverModule+"/internal/weborigin"),
		),
		thirdParty: only(descendants("github.com/gorilla")),
	},
	{
		name:           "HTTP resources",
		sources:        []pathPattern{subtree(serverModule + "/httpapi")},
		deniedStandard: standardInfrastructureExceptHTTP,
		// The resources name the application surface, the area packages whose
		// command, query and result types they map, store result types, and
		// the domain and presentation leaves; the routing kernel owns the
		// router library and browser origins.
		project: only(
			exact(serverModule+"/httpapi/route"),
			exact(serverModule+"/logging"),
			exact(serverModule+"/app"),
			exact(serverModule+"/app/academic"),
			exact(serverModule+"/app/administration"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/identity"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/job"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/app/onboarding"),
			exact(serverModule+"/app/paging"),
			exact(serverModule+"/app/search"),
			exact(serverModule+"/app/user"),
			subtree(serverModule+"/app/exam"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/localization"),
		),
	},
	{
		name:           "WebSocket transport",
		sources:        []pathPattern{subtree(serverModule + "/websocket")},
		deniedStandard: standardInfrastructureExceptHTTP,
		// The socket delivers Realtime events and connects Attempts; it reaches
		// no other area.
		project: only(
			exact(serverModule+"/logging"),
			exact(serverModule+"/app/apperror"),
			exact(serverModule+"/app/exam/attempt"),
			exact(serverModule+"/app/invocation"),
			exact(serverModule+"/app/realtime"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/localization"),
			exact(serverModule+"/internal/weborigin"),
		),
		thirdParty: only(descendants("github.com/gorilla")),
	},
	{
		name:    "cluster contracts",
		sources: []pathPattern{exact(serverModule + "/cluster")},
	},
	{
		name: "cluster adapters",
		sources: []pathPattern{
			subtree(serverModule + "/cluster/local"),
			subtree(serverModule + "/cluster/memberlist"),
		},
		project:    only(exact(serverModule + "/cluster")),
		thirdParty: allImports(),
	},
	{
		name:    "metrics",
		sources: []pathPattern{exact(serverModule + "/metrics")},
		// Beyond its recorder contracts, metrics names only the ports its
		// measuring wrappers decorate: the platform Cache and Mailer, the
		// cluster transport, and the reusable VFS and mail contracts.
		project: only(
			exact(serverModule+"/config"),
			exact(serverModule+"/internal/tlscert"),
			exact(serverModule+"/store/timerlayer"),
			exact(serverModule+"/store/localcachelayer"),
			exact(serverModule+"/platform"),
			exact(serverModule+"/cluster"),
			exact(repositoryModule+"/packages/vfs"),
			exact(repositoryModule+"/packages/mail"),
		),
		thirdParty: only(subtree("github.com/prometheus/client_golang")),
	},
	{
		name:       "tracing",
		sources:    []pathPattern{exact(serverModule + "/tracing")},
		project:    only(exact(serverModule + "/config")),
		thirdParty: only(subtree("go.opentelemetry.io/otel")),
	},
	{
		name:    "external-auth adapters",
		sources: []pathPattern{subtree(serverModule + "/platform/externalauth")},
		project: only(
			exact(serverModule+"/config"),
			exact(serverModule+"/model"),
			exact(serverModule+"/platform/externalauth"),
			exact(serverModule+"/plugin"),
		),
		thirdParty: allImports(),
	},
	{
		name:    "platform lifecycle",
		sources: []pathPattern{exact(serverModule + "/platform")},
		project: allExcept(
			descendants(repositoryModule+"/packages/cache"),
			descendants(repositoryModule+"/packages/mail"),
			descendants(repositoryModule+"/packages/vfs"),
			descendants(serverModule+"/cluster"),
			descendants(serverModule+"/platform/externalauth"),
			exact(serverModule+"/store/sqlstore"),
			// metrics wraps the platform ports, so the edge points one way.
			exact(serverModule+"/metrics"),
			subtree(serverModule+"/app"),
			subtree(serverModule+"/httpapi"),
			subtree(serverModule+"/websocket"),
		),
	},
	{
		name:    "healthcheck executable",
		sources: []pathPattern{exact(serverModule + "/cmd/proctor-healthcheck")},
	},
	{
		// Offline operator commands open the configuration and the database
		// themselves; they never build a node.
		name:           "offline operator commands",
		sources:        []pathPattern{exact(serverModule + "/operator")},
		deniedStandard: standardInfrastructure,
		project: only(
			exact(serverModule+"/config"),
			exact(serverModule+"/model"),
			exact(serverModule+"/store"),
			exact(serverModule+"/store/sqlstore"),
		),
	},
	{
		name:    "operator executable",
		sources: []pathPattern{exact(serverModule + "/cmd/proctor")},
		project: only(exact(serverModule + "/cmd/proctor/commands")),
	},
	{
		name:           "operator commands",
		sources:        []pathPattern{subtree(serverModule + "/cmd/proctor/commands")},
		deniedStandard: commandInfrastructure,
		project: only(
			exact(serverModule),
			exact(serverModule+"/localization"),
			exact(serverModule+"/operator"),
		),
		// x/sys/unix reads CLOCK_MONOTONIC for systemd reload notifications.
		thirdParty: only(exact("github.com/spf13/cobra"), exact("golang.org/x/sys/unix")),
	},
	{
		name:    "ptool executable",
		sources: []pathPattern{exact(serverModule + "/cmd/ptool")},
		project: only(exact(serverModule + "/cmd/ptool/commands")),
	},
	{
		name:    "ptool commands",
		sources: []pathPattern{subtree(serverModule + "/cmd/ptool/commands")},
		project: only(
			exact(serverModule+"/cmd/ptool/devseed"),
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/cmd/proctor/commands"),
			exact(serverModule+"/httpapi"),
			exact(serverModule+"/internal/asyncapidoc"),
			exact(serverModule+"/internal/errorcatalog"),
			exact(serverModule+"/internal/openapidoc"),
			exact(serverModule+"/localization"),
			exact(serverModule+"/websocket"),
		),
		thirdParty: only(exact("github.com/spf13/cobra")),
	},
	{
		name:    "development seed HTTP client",
		sources: []pathPattern{exact(serverModule + "/cmd/ptool/devseed")},
	},
	{
		name:            "unknown ptool children",
		sources:         []pathPattern{descendants(serverModule + "/cmd/ptool")},
		denyEveryImport: true,
	},
	{
		name:    "mail preview executable",
		sources: []pathPattern{exact(serverModule + "/cmd/mailpreview")},
		project: only(
			exact(serverModule+"/app/mail"),
			exact(serverModule+"/localization"),
			exact(serverModule+"/model"),
		),
	},
	{
		name:       "configuration",
		sources:    []pathPattern{exact(serverModule + "/config")},
		project:    only(exact(serverModule + "/model")),
		thirdParty: allImports(),
	},
	{
		name: "leaf infrastructure",
		sources: []pathPattern{
			exact(serverModule + "/logging"),
			exact(serverModule + "/migrations"),
		},
		thirdParty: allImports(),
	},
}

func TestDependencyRulesAreWellFormed(t *testing.T) {
	t.Parallel()

	names := make(map[string]struct{}, len(dependencyRules))
	sources := make(map[string]string)
	for _, rule := range dependencyRules {
		assert.NotEmpty(t, rule.name, "dependency rule has an empty name")
		_, exists := names[rule.name]
		assert.False(t, exists, "duplicate dependency rule name %q", rule.name)
		names[rule.name] = struct{}{}
		assert.NotEmpty(t, rule.sources, "dependency rule %q has no source packages", rule.name)
		for _, source := range rule.sources {
			if !assert.NotEmpty(t, source.path, "dependency rule %q has an empty source pattern", rule.name) {
				continue
			}
			assert.LessOrEqual(t, source.match, matchDescendants, "dependency rule %q has an invalid source match for %q", rule.name, source.path)
			key := fmt.Sprintf("%d:%s", source.match, source.path)
			owner, exists := sources[key]
			assert.False(t, exists, "dependency rules %q and %q declare the same source pattern %q", owner, rule.name, source.path)
			sources[key] = rule.name
		}
	}
}

func TestDependencyRulePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		packagePath string
		wantRule    string
	}{
		{packagePath: serverModule + "/app/jobs", wantRule: "concrete jobs"},
		{packagePath: serverModule + "/app/idempotency", wantRule: "idempotency application"},
		{packagePath: serverModule + "/app/invocation", wantRule: "invocation application"},
		{packagePath: serverModule + "/app/apperror", wantRule: "application error vocabulary"},
		{packagePath: serverModule + "/app/access", wantRule: "access application"},
		{packagePath: serverModule + "/app/paging", wantRule: "paging application"},
		{packagePath: serverModule + "/app/academic", wantRule: "academic application"},
		{packagePath: serverModule + "/app/user", wantRule: "user application"},
		{packagePath: serverModule + "/app/identity", wantRule: "identity application"},
		{packagePath: serverModule + "/app/administration", wantRule: "administration application"},
		{packagePath: serverModule + "/app/onboarding", wantRule: "onboarding application"},
		{packagePath: serverModule + "/app/search", wantRule: "search application"},
		{packagePath: serverModule + "/app/job", wantRule: "job engine"},
		{packagePath: serverModule + "/app/mail", wantRule: "mail application"},
		{packagePath: serverModule + "/app/exam/manageraccess", wantRule: "exam manager access"},
		{packagePath: serverModule + "/app/exam", wantRule: "exam manager access consumers"},
		{packagePath: serverModule + "/app/exam/review", wantRule: "exam mail and safe Markdown consumers"},
		{packagePath: serverModule + "/app/exam/safemarkdown", wantRule: "safe Markdown"},
		{packagePath: serverModule + "/app/internal/batchitem", wantRule: "batch item context"},
		{packagePath: serverModule + "/app", wantRule: "application"},
		{packagePath: serverModule + "/store/sqlstore", wantRule: "SQL store adapter"},
		{packagePath: serverModule + "/platform/externalauth/oidc", wantRule: "external-auth adapters"},
		{packagePath: serverModule + "/internal/openapidoc", wantRule: "OpenAPI compiler"},
		{packagePath: serverModule + "/internal/asyncapidoc", wantRule: "AsyncAPI compiler"},
		{packagePath: serverModule + "/internal/errorcatalog", wantRule: "error catalog compiler"},
		{packagePath: serverModule + "/cmd/proctor-healthcheck", wantRule: "healthcheck executable"},
		{packagePath: serverModule + "/operator", wantRule: "offline operator commands"},
		{packagePath: serverModule + "/cmd/ptool/commands", wantRule: "ptool commands"},
		{packagePath: serverModule + "/httpapi/route", wantRule: "HTTP routing kernel"},
		{packagePath: serverModule + "/httpapi", wantRule: "HTTP resources"},
		{packagePath: serverModule + "/websocket", wantRule: "WebSocket transport"},
	}

	for _, tt := range tests {
		t.Run(tt.packagePath, func(t *testing.T) {
			t.Parallel()
			rule, ok := dependencyRuleFor(tt.packagePath)
			require.True(t, ok, "dependencyRuleFor(%q) found no rule", tt.packagePath)
			require.Equal(t, tt.wantRule, rule.name, "dependencyRuleFor(%q)", tt.packagePath)
		})
	}
}

func TestUnclassifiedProductionPackageFailsClosed(t *testing.T) {
	t.Parallel()

	packagePath := serverModule + "/services"
	require.False(t, knownProductionPackage(packagePath), "knownProductionPackage")
	require.True(t, forbiddenImport(packagePath, "context"), "forbiddenImport(%q, %q) = false, want true", packagePath, "context")
}

func forbiddenImport(from, imported string) bool {
	if exemptProductionHelper(from) {
		return false
	}
	rule, ok := dependencyRuleFor(from)
	if !ok {
		return true
	}
	return rule.forbids(imported)
}

func dependencyRuleFor(packagePath string) (dependencyRule, bool) {
	for _, rule := range dependencyRules {
		if matchesAny(rule.sources, packagePath) {
			return rule, true
		}
	}
	return dependencyRule{}, false
}

func matchesAny(patterns []pathPattern, path string) bool {
	for _, pattern := range patterns {
		if pattern.matches(path) {
			return true
		}
	}
	return false
}

func projectImport(importPath string) bool {
	return strings.HasPrefix(importPath, repositoryModule+"/")
}

func thirdPartyImport(importPath string) bool {
	if projectImport(importPath) {
		return false
	}
	first, _, _ := strings.Cut(importPath, "/")
	return strings.Contains(first, ".")
}

func packageOrBelow(packagePath, root string) bool {
	return packagePath == root || strings.HasPrefix(packagePath, root+"/")
}

func exemptProductionHelper(packagePath string) bool {
	return packageOrBelow(packagePath, serverModule+"/testlib") ||
		packageOrBelow(packagePath, serverModule+"/store/storetest") ||
		packageOrBelow(packagePath, serverModule+"/app/realtime/realtimetest") ||
		packageOrBelow(packagePath, serverModule+"/httpapi/route/routetest") ||
		packageOrBelow(packagePath, serverModule+"/config/configtest")
}

func knownProductionPackage(packagePath string) bool {
	if exemptProductionHelper(packagePath) {
		return true
	}
	_, ok := dependencyRuleFor(packagePath)
	return ok
}
