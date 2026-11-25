// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package testdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
)

// The package database becomes a template the first time a test asks for an
// isolated database. PostgreSQL copies a template only while no session is
// connected to it, so after prepare runs nothing connects to it again.
var template struct {
	once sync.Once
	err  error
}

var clones atomic.Int64

// Isolated returns the URL of a new database for t alone, copied from the
// package database, and drops it when t ends. The first call runs prepare on
// the package database, typically to migrate it, and every later copy starts
// from that state. A copy of a migrated database is far cheaper than
// migrating, so each parallel test can own one.
//
// Isolated needs Main in the package's TestMain.
func Isolated(t testing.TB, prepare func(dataSource string) error) string {
	t.Helper()
	if packageURL == nil {
		t.Fatalf("testdb: Isolated needs testdb.Main in TestMain and %s set", Variable)
	}
	template.once.Do(func() { template.err = prepare(packageURL.String()) })
	if template.err != nil {
		t.Fatalf("testdb: prepare the template database: %v", template.err)
	}

	name := fmt.Sprintf("%s_%d", cloneNamePrefix(), clones.Add(1))
	quoted := pq.QuoteIdentifier(name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := copyTemplate(ctx, quoted); err != nil {
		t.Fatalf("testdb: create %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// FORCE ends connections a test leaked.
		if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`); err != nil {
			t.Errorf("testdb: drop %s: %v", name, err)
		}
	})
	clone := *packageURL
	clone.Path = "/" + name
	return clone.String()
}

// copyTemplate retries while a session prepare closed is still leaving the
// template, which PostgreSQL reports as object_in_use.
func copyTemplate(ctx context.Context, quoted string) error {
	statement := `CREATE DATABASE ` + quoted + ` TEMPLATE ` + pq.QuoteIdentifier(packageName) //nolint:gosec // G202: PostgreSQL can't bind database names; both are quoted.
	for {
		_, err := admin.ExecContext(ctx, statement)
		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || pqErr.Code != "55006" {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// cloneNamePrefix keeps a copy's name within PostgreSQL's 63-byte identifier
// limit and unique to this run, so Main can drop copies a killed test left.
func cloneNamePrefix() string {
	return "pc_" + strings.TrimPrefix(packageName, "proctor_test_")
}

var (
	admin       *sql.DB
	packageURL  *url.URL
	packageName string
)

func setIsolation(server *sql.DB, created *url.URL, name string) {
	admin, packageURL, packageName = server, created, name
}

func dropCopies(ctx context.Context, server *sql.DB) error {
	if packageName == "" {
		return nil
	}
	rows, err := server.QueryContext(ctx, `SELECT datname FROM pg_database WHERE starts_with(datname, $1)`, cloneNamePrefix()+"_")
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, name := range names {
		if _, err = server.ExecContext(ctx, `DROP DATABASE IF EXISTS `+pq.QuoteIdentifier(name)+` WITH (FORCE)`); err != nil {
			return err
		}
	}
	return nil
}
