// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package testdb gives each integration test package its own PostgreSQL
// database, so packages can run in parallel and no test sees rows another
// package left behind.
//
// A package calls Main from its TestMain. Main creates a fresh database on the
// server PROCTOR_TEST_DATABASE_URL names, points the variable at it for the
// package's tests, and drops it when they finish. Tests keep reading the
// variable as before and migrate the database on first use. Advisory locks
// and LISTEN/NOTIFY are per database in PostgreSQL, so packages don't
// contend through them either.
package testdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	// The test database is reached through lib/pq, as the store is.
	_ "github.com/lib/pq"
)

// Variable names the database integration tests connect to.
const Variable = "PROCTOR_TEST_DATABASE_URL"

var unsafeName = regexp.MustCompile(`[^a-z0-9_]+`)

// Main runs m against a database of the package's own and returns m.Run's
// exit code. Without PROCTOR_TEST_DATABASE_URL it only runs m, so a package's
// unit tests still run without a database.
func Main(m *testing.M) int {
	server := os.Getenv(Variable)
	if server == "" {
		return m.Run()
	}
	working, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testdb: %v\n", err)
		return 1
	}
	name := databaseName(working, os.Getpid())
	packageURL, drop, err := create(server, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testdb: create %s: %v\n", name, err)
		return 1
	}
	if err = os.Setenv(Variable, packageURL); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: %v\n", err)
		return 1
	}
	code := m.Run()
	_ = os.Setenv(Variable, server)
	if err = drop(); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: drop %s: %v\n", name, err)
	}
	return code
}

// databaseName is unique to one run of one package: the directory names
// the package, and the process ID tells two runs of it apart. PostgreSQL
// identifiers stop at 63 bytes, so a long path is shortened with a digest.
func databaseName(directory string, pid int) string {
	parts := strings.Split(strings.ToLower(directory), string(os.PathSeparator))
	base := "root"
	if last := parts[len(parts)-1]; last != "" {
		base = last
	}
	digest := sha256.Sum256([]byte(directory))
	base = unsafeName.ReplaceAllString(base, "_")
	if len(base) > 30 {
		base = base[:30]
	}
	return fmt.Sprintf("proctor_test_%s_%s_%d", base, hex.EncodeToString(digest[:4]), pid)
}

// create makes the database and returns its URL and how to drop it.
func create(server, name string) (string, func() error, error) {
	parsed, err := url.Parse(server)
	if err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", Variable, err)
	}
	admin, err := sql.Open("postgres", server)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// A previous run killed before its cleanup would leave the name taken
	// only if its PID came back; drop it rather than fail.
	quoted := `"` + name + `"`
	if _, err = admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`); err != nil {
		_ = admin.Close()
		return "", nil, err
	}
	if _, err = admin.ExecContext(ctx, `CREATE DATABASE `+quoted); err != nil {
		_ = admin.Close()
		return "", nil, err
	}
	packageURL := *parsed
	packageURL.Path = "/" + name
	drop := func() error {
		defer func() { _ = admin.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// FORCE ends connections a test leaked, which would otherwise keep
		// the database alive.
		_, dropErr := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`)
		return dropErr
	}
	return packageURL.String(), drop, nil
}
