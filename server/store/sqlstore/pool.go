// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
)

// The pools a SQLStore opens. Their names reach pg_stat_activity as the
// application_name and Prometheus as the pool label.
const (
	PoolMain    = "proctor"
	PoolControl = "proctor-control"
)

// PoolStats is one pool's database/sql statistics with the number of
// connections it has opened since start, which database/sql does not count.
// A rising Opened while load is steady means the pool is churning.
type PoolStats struct {
	Pool   string
	Stats  sql.DBStats
	Opened uint64
}

// countingConnector counts every successful connect, so churn is visible
// without reading PostgreSQL's own statistics.
type countingConnector struct {
	driver.Connector
	opened atomic.Uint64
}

func (c *countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err == nil {
		c.opened.Add(1)
	}
	return conn, err
}
