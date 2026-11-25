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
	t.Parallel()
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
	t.Parallel()
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	storetest.TestExamSubmissionDomainEvents(t, persistence, func(t *testing.T, ctx context.Context) (int, int) {
		t.Helper()
		// A seal also ends the Participation, whose event the schema writes;
		// only the seal's own exam.attempt.submitted row is counted here.
		var events, jobs int
		require.NoError(t, persistence.GetMaster().Get(ctx, &events, `SELECT count(*) FROM domain_events WHERE topic=?`,
			string(model.DomainEventTopicExamAttemptSubmitted)))
		require.NoError(t, persistence.GetMaster().Get(ctx, &jobs, `SELECT count(*) FROM jobs j
			JOIN domain_events e ON e.id = j.command->>'event_id'
			WHERE j.type=? AND e.topic=?`,
			string(model.JobTypeDomainEventDispatch), string(model.DomainEventTopicExamAttemptSubmitted)))
		return events, jobs
	})
}

func TestLifecycleTriggersWriteDomainEvents(t *testing.T) {
	t.Parallel()
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	storetest.TestLifecycleDomainEvents(t, persistence, func(t *testing.T, ctx context.Context, topic model.DomainEventTopic,
		member, value string,
	) []storetest.LifecycleDomainEvent {
		t.Helper()
		var rows []struct {
			domainEventRow
			JobID string `db:"job_id"`
		}
		require.NoError(t, persistence.GetMaster().Select(ctx, &rows, `SELECT e.id, e.topic, e.data, e.occurred_at, j.id AS job_id
			FROM domain_events e
			JOIN jobs j ON j.type = ? AND j.dedupe_key = e.id
			WHERE e.topic = ? AND e.data->>? = ?
			ORDER BY e.occurred_at, e.id`, string(model.JobTypeDomainEventDispatch), string(topic), member, value))
		found := make([]storetest.LifecycleDomainEvent, 0, len(rows))
		for _, row := range rows {
			event, err := row.model()
			require.NoError(t, err)
			require.True(t, model.JobID(row.JobID).IsValid(), "dispatch Job id %q", row.JobID)
			found = append(found, storetest.LifecycleDomainEvent{Event: event, JobID: model.JobID(row.JobID)})
		}
		return found
	})
}

// TestNewOpaqueIDMatchesTheModel holds the schema's identifier minting to
// model.NewID's form and randomness.
func TestNewOpaqueIDMatchesTheModel(t *testing.T) {
	t.Parallel()
	persistence := openTestStore(t)
	var ids []string
	require.NoError(t, persistence.GetMaster().Select(context.Background(), &ids,
		`SELECT new_opaque_id() FROM generate_series(1, 256)`))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		require.True(t, model.IsValidOpaqueID(id), "minted %q", id)
		_, err := model.ParseDomainEventID(id)
		require.NoError(t, err)
		seen[id] = struct{}{}
	}
	require.Len(t, seen, len(ids), "minted identifiers repeat")
}

func TestDomainEventSchemaRejectsInvalidRows(t *testing.T) {
	t.Parallel()
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
