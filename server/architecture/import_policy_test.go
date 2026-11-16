// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import "strings"

const serverModule = "github.com/sudosylabs/proctor/server"

// importRule forbids packages matching from, other than those matching
// exempt, to import a path matching deny unless it also matches allow. A
// pattern ending in "/..." matches that path and everything below it; any
// other pattern matches exactly.
type importRule struct {
	reason string
	from   []string
	exempt []string
	deny   []string
	allow  []string
}

func (r importRule) forbids(packagePath, importPath string) bool {
	return matchesAny(r.from, packagePath) && !matchesAny(r.exempt, packagePath) &&
		matchesAny(r.deny, importPath) && !matchesAny(r.allow, importPath)
}

// srv names a server package by its path relative to the module root.
func srv(path string) string {
	if path == "" {
		return serverModule
	}
	return serverModule + "/" + path
}

var importRules = []importRule{
	{
		reason: "the domain model is a leaf; it uses only its own helpers and canonical JSON",
		from:   []string{srv("model/...")},
		deny:   []string{srv("...")},
		allow:  []string{srv("model/..."), srv("internal/canonicaljson")},
	},
	{
		reason: "the application layer never reaches transports, concrete infrastructure or composition",
		from:   []string{srv("app/...")},
		deny: []string{
			srv(""), srv("cmd/..."), srv("operator/..."),
			srv("httpapi/..."), srv("websocket/..."), srv("webui/..."),
			srv("store/sqlstore/..."), srv("store/localcachelayer/..."),
			srv("store/retrylayer/..."), srv("store/timerlayer/..."),
			srv("platform/..."), srv("cluster/local/..."), srv("cluster/memberlist/..."),
			srv("metrics/..."), srv("tracing/..."),
		},
	},
	{
		reason: "the application layer never speaks HTTP, SQL, Redis or gossip itself",
		from:   []string{srv("app/...")},
		deny: []string{
			"net/http/...", "database/sql/...",
			"github.com/gorilla/mux/...", "github.com/gorilla/websocket/...",
			"github.com/lib/pq/...", "github.com/jackc/pgx/...",
			"github.com/go-sql-driver/mysql/...", "modernc.org/sqlite/...",
			"github.com/redis/...", "github.com/hashicorp/memberlist/...",
		},
	},
	{
		reason: "the Store contract knows no implementation and no application code",
		from:   []string{srv("store")},
		deny:   []string{srv("store/..."), srv("app/...")},
		allow:  []string{srv("store")},
	},
	{
		reason: "transports call the application, never the store implementation or its layers",
		from:   []string{srv("httpapi/..."), srv("websocket/...")},
		deny: []string{
			srv("store/sqlstore/..."), srv("store/localcachelayer/..."),
			srv("store/retrylayer/..."), srv("store/timerlayer/..."),
		},
	},
	{
		reason: "only commands and the test harness compose the server",
		from:   []string{srv("...")},
		exempt: []string{srv("cmd/..."), srv("testlib/...")},
		deny:   []string{srv("")},
	},
	{
		reason: "the operator works on the store directly, outside the application and transports",
		from:   []string{srv("operator/...")},
		deny:   []string{srv(""), srv("app/..."), srv("httpapi/..."), srv("websocket/..."), srv("webui/...")},
	},
}

// parentImportExempt lists test-support children that wrap their parent.
var parentImportExempt = []string{srv("app/access/accesstest"), srv("app/realtime/realtimetest")}

// importsAncestor reports an application child importing an application
// package above it. Siblings may import each other; Go rejects cycles.
func importsAncestor(packagePath, importPath string) bool {
	return matches(srv("app/..."), packagePath) && !matchesAny(parentImportExempt, packagePath) &&
		matches(srv("app/..."), importPath) && strings.HasPrefix(packagePath, importPath+"/")
}

func matches(pattern, path string) bool {
	if root, ok := strings.CutSuffix(pattern, "/..."); ok {
		return path == root || strings.HasPrefix(path, root+"/")
	}
	return path == pattern
}

func matchesAny(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if matches(pattern, path) {
			return true
		}
	}
	return false
}
