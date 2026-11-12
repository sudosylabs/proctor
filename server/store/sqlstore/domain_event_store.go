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
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLDomainEventStore is the PostgreSQL adapter for the domain-event outbox.
type SQLDomainEventStore struct{ *SQLStore }

func newSQLDomainEventStore(sqlStore *SQLStore) store.DomainEventStore {
	return &SQLDomainEventStore{SQLStore: sqlStore}
}

type domainEventRow struct {
	ID         string    `db:"id"`
	Topic      string    `db:"topic"`
	Data       jsonValue `db:"data"`
	OccurredAt time.Time `db:"occurred_at"`
}

func (row domainEventRow) model() (*model.DomainEvent, error) {
	id, err := model.ParseDomainEventID(row.ID)
	if err != nil {
		return nil, invalidPersistedState("domain_event", "id", err)
	}
	event := &model.DomainEvent{ID: id, Topic: model.DomainEventTopic(row.Topic), OccurredAt: model.TimeUTC(row.OccurredAt),
		Data: append([]byte(nil), row.Data...)}
	if err = event.Validate(); err != nil {
		return nil, invalidPersistedState("domain_event", "value", err)
	}
	return event, nil
}

// Get returns one undelivered domain event.
func (s *SQLDomainEventStore) Get(ctx context.Context, id model.DomainEventID) (*model.DomainEvent, error) {
	if !id.IsValid() {
		return nil, store.NewErrInvalidInput("domain_event", "id", nil)
	}
	var row domainEventRow
	if err := s.GetMaster().Get(ctx, &row, `SELECT id,topic,data,occurred_at FROM domain_events WHERE id=?`, id.String()); err != nil {
		return nil, translateError("domain_event", id.String(), err)
	}
	return row.model()
}

// Delete retires a delivered domain event; an absent row is already retired.
func (s *SQLDomainEventStore) Delete(ctx context.Context, id model.DomainEventID) error {
	if !id.IsValid() {
		return store.NewErrInvalidInput("domain_event", "id", nil)
	}
	if _, err := s.GetMaster().Exec(ctx, `DELETE FROM domain_events WHERE id=?`, id.String()); err != nil {
		return fmt.Errorf("delete domain event: %w", err)
	}
	return nil
}

// domainEventIdentities is the application-proposed identity of one outbox
// row and its dispatch Job.
type domainEventIdentities struct {
	EventID model.DomainEventID
	JobID   model.JobID
}

func (ids domainEventIdentities) valid() bool { return ids.EventID.IsValid() && ids.JobID.IsValid() }

// insertExamAttemptSubmittedEvent writes the exam.attempt.submitted outbox
// row for a newly sealed Submission and its queued dispatch Job, inside the
// sealing transaction and at its PostgreSQL time.
func insertExamAttemptSubmittedEvent(ctx context.Context, tx *sqlxTxWrapper, ids domainEventIdentities,
	submission *model.ExamSubmission, sittingID model.ExamSittingID, examID model.ExamID, at time.Time,
) error {
	event, err := model.NewExamAttemptSubmittedEvent(model.ExamAttemptSubmittedEvent{ID: ids.EventID, OccurredAt: at,
		SubmissionID: submission.ID, AttemptID: submission.AttemptID, SittingID: sittingID, ExamID: examID})
	if err != nil {
		return store.NewErrInvalidInput("domain_event", "value", nil).Wrap(err)
	}
	job, err := model.NewDomainEventDispatchJob(ids.JobID, event)
	if err != nil {
		return store.NewErrInvalidInput("job", "domain_event_dispatch", nil).Wrap(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO domain_events (id,topic,data,occurred_at) VALUES (?,?,?,?)`,
		event.ID.String(), string(event.Topic), jsonValue(event.Data), event.OccurredAt); err != nil {
		return fmt.Errorf("insert domain event: %w", err)
	}
	if _, err = insertQueuedJob(ctx, tx, job, false); err != nil {
		return fmt.Errorf("insert domain event dispatch Job: %w", err)
	}
	return nil
}
