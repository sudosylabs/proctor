// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package localcachelayer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/model"
)

// clusterInvalidation tells other nodes to drop an academic period this node
// changed. Peers only invalidate locally and never rebroadcast.
type clusterInvalidation struct {
	cluster cluster.Transport
}

type academicPeriodInvalidationMessage struct {
	ID string `json:"id"`
}

const academicPeriodInvalidatedEvent cluster.Event = "store.academic_period.invalidated"

// NewClusterInvalidation returns an InvalidationFanout over the cluster
// transport.
func NewClusterInvalidation(transport cluster.Transport) (InvalidationFanout, error) {
	if transport == nil {
		return nil, errors.New("cluster is nil")
	}
	return &clusterInvalidation{cluster: transport}, nil
}

func (a *clusterInvalidation) RegisterAcademicPeriod(
	handler func(context.Context, string) error,
) error {
	if handler == nil {
		return errors.New("academic-period invalidation handler is nil")
	}
	return a.cluster.RegisterHandler(
		academicPeriodInvalidatedEvent,
		func(ctx context.Context, message *cluster.Message) error {
			if message == nil {
				return errors.New("cluster message is nil")
			}
			if err := message.Validate(); err != nil {
				return err
			}
			var invalidation academicPeriodInvalidationMessage
			if err := json.Unmarshal(message.Data, &invalidation); err != nil {
				return fmt.Errorf("decode academic-period cache invalidation: %w", err)
			}
			if !model.AcademicPeriodID(invalidation.ID).IsValid() {
				return errors.New("academic-period cache invalidation ID is invalid")
			}
			return handler(ctx, invalidation.ID)
		},
	)
}

func (a *clusterInvalidation) BroadcastAcademicPeriod(ctx context.Context, id string) error {
	if !model.AcademicPeriodID(id).IsValid() {
		return errors.New("academic-period cache invalidation ID is invalid")
	}
	data, err := json.Marshal(academicPeriodInvalidationMessage{ID: id})
	if err != nil {
		return fmt.Errorf("encode academic-period cache invalidation: %w", err)
	}
	return a.cluster.Broadcast(ctx, &cluster.Message{
		Event: academicPeriodInvalidatedEvent,
		Data:  data,
	})
}

var _ InvalidationFanout = (*clusterInvalidation)(nil)
