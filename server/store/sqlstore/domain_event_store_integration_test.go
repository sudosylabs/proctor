//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
)

func TestDomainEventStore(t *testing.T) {
	persistence := openTestStore(t)
	for _, test := range []struct {
		name     string
		decorate func(*testing.T, *SQLStore) store.Store
	}{
		{"SQL", func(_ *testing.T, s *SQLStore) store.Store { return s }},
		{"LocalCache", newLocalCacheConformanceStore}, {"Retry", newRetryConformanceStore}, {"Timer", newTimerConformanceStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetTestStore(t, persistence)
			storetest.TestDomainEventStore(t, test.decorate(t, persistence))
		})
	}
}

func TestExamSubmissionSealWritesOneDomainEvent(t *testing.T) {
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	storetest.TestExamSubmissionDomainEvents(t, persistence, func(t *testing.T, ctx context.Context) (int, int) {
		t.Helper()
		var events, jobs int
		require.NoError(t, persistence.GetMaster().Get(ctx, &events, `SELECT count(*) FROM domain_events`))
		require.NoError(t, persistence.GetMaster().Get(ctx, &jobs, `SELECT count(*) FROM jobs WHERE type=?`,
			string(model.JobTypeDomainEventDispatch)))
		return events, jobs
	})
}

func TestDomainEventSchemaRejectsInvalidRows(t *testing.T) {
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	ctx := context.Background()
	data := `{"submission_id":"` + model.NewSubmissionID().String() + `"}`
	tests := []struct {
		name       string
		id         string
		topic      string
		data       string
		constraint string
	}{
		{name: "noncanonical id", id: "bad", topic: "exam.attempt.submitted", data: data, constraint: "domain_events_id_canonical_check"},
		{name: "unknown topic", id: model.NewDomainEventID().String(), topic: "exam.attempt.started", data: data, constraint: "domain_events_topic_check"},
		{name: "non-object data", id: model.NewDomainEventID().String(), topic: "exam.attempt.submitted", data: `[]`, constraint: "domain_events_data_check"},
		{name: "oversized data", id: model.NewDomainEventID().String(), topic: "exam.attempt.submitted",
			data: `{"padding":"` + strings.Repeat("a", 4096) + `"}`, constraint: "domain_events_data_check"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := persistence.GetMaster().Exec(ctx, `INSERT INTO domain_events (id,topic,data,occurred_at) VALUES (?,?,?::jsonb,clock_timestamp())`,
				test.id, test.topic, test.data)
			var postgresErr *pq.Error
			require.ErrorAs(t, err, &postgresErr, "constraint error")
			require.Equal(t, "23514", string(postgresErr.Code), "constraint error")
			require.Equal(t, test.constraint, postgresErr.Constraint, "constraint error")
		})
	}
}
