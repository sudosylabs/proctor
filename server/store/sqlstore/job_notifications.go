// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	// jobNotificationChannel is the channel the jobs_notify_queued trigger
	// notifies, with the Job's type as the payload.
	jobNotificationChannel = "proctor_jobs"
	// PoolListener names the listening connection in pg_stat_activity.
	PoolListener = "proctor-listener"

	listenerMinimumReconnect = 500 * time.Millisecond
	listenerMaximumReconnect = 30 * time.Second
)

// ListenJobs holds one dedicated connection that hears every Job of any node
// becoming claimable, and calls wake with its type. It calls wake with no
// type after connecting and after every reconnect, because a notification
// sent while the connection was down is lost. It returns when ctx ends.
func (ss *SQLStore) ListenJobs(ctx context.Context, wake func(types ...model.JobType)) error {
	dataSource, err := listenerDataSource(ss.settings.DataSource)
	if err != nil {
		return err
	}
	events := make(chan pq.ListenerEventType, 4)
	listener := pq.NewListener(dataSource, listenerMinimumReconnect, listenerMaximumReconnect,
		func(event pq.ListenerEventType, _ error) {
			select {
			case events <- event:
			default:
			}
		})
	defer func() { _ = listener.Close() }()
	if err := listener.Listen(jobNotificationChannel); err != nil {
		return fmt.Errorf("listen for queued jobs: %w", err)
	}
	wake()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-events:
			if event == pq.ListenerEventReconnected {
				wake()
			}
		case notification := <-listener.Notify:
			if notification == nil {
				// pq sends nil after reconnecting; whatever was sent meanwhile
				// is lost, so every type looks again.
				wake()
				continue
			}
			wake(model.JobType(notification.Extra))
		}
	}
}

// listenerDataSource names the listening connection and fixes its session
// zone, like every pooled connection.
func listenerDataSource(dataSource string) (string, error) {
	parsed, err := url.Parse(dataSource)
	if err != nil {
		return "", fmt.Errorf("parse database data source: %w", err)
	}
	query := parsed.Query()
	query.Set("application_name", PoolListener)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
