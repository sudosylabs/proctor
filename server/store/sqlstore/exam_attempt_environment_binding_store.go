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
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type attemptEnvironmentBindingRow struct {
	ID              string       `db:"id"`
	AttemptID       string       `db:"exam_attempt_id"`
	ParticipationID string       `db:"participation_id"`
	Generation      int64        `db:"generation"`
	GrantID         string       `db:"grant_id"`
	Epoch           string       `db:"epoch"`
	Incarnation     int64        `db:"incarnation"`
	CreatedAt       time.Time    `db:"created_at"`
	EndedAt         sql.NullTime `db:"ended_at"`
}

const attemptEnvironmentBindingSelect = `SELECT id, exam_attempt_id, participation_id, generation, grant_id, epoch,
		incarnation, created_at, ended_at
	FROM exam_attempt_environment_bindings`

func (row attemptEnvironmentBindingRow) model() (*model.AttemptEnvironmentBinding, error) {
	id, err := model.ParseAttemptEnvironmentBindingID(row.ID)
	if err != nil {
		return nil, invalidPersistedState("attempt_environment_binding", "id", err)
	}
	attemptID, err := model.ParseExamAttemptID(row.AttemptID)
	if err != nil {
		return nil, invalidPersistedState("attempt_environment_binding", "exam_attempt_id", err)
	}
	participationID, err := model.ParseAttemptParticipationID(row.ParticipationID)
	if err != nil {
		return nil, invalidPersistedState("attempt_environment_binding", "participation_id", err)
	}

	binding := &model.AttemptEnvironmentBinding{
		ID:              id,
		AttemptID:       attemptID,
		ParticipationID: participationID,
		Generation:      row.Generation,
		Identity: model.AttemptEnvironmentIdentity{
			GrantID:     row.GrantID,
			Epoch:       row.Epoch,
			Incarnation: row.Incarnation,
		},
		CreatedAt: model.TimeUTC(row.CreatedAt),
		EndedAt:   OptionalTimeFromNullTime(row.EndedAt),
	}
	if err = binding.Validate(); err != nil {
		return nil, invalidPersistedState("attempt_environment_binding", "value", err)
	}
	return binding, nil
}

// BindEnvironment binds an execution environment identity to the Attempt's
// current Participation generation, replacing an older incarnation's binding
// for the same generation atomically.
func (s *SQLExamAttemptWorkspaceStore) BindEnvironment(
	ctx context.Context,
	input *store.ExamAttemptEnvironmentBind,
) (*store.ExamAttemptEnvironmentBindResult, error) {
	if input == nil ||
		!input.BindingID.IsValid() ||
		!input.AttemptID.IsValid() ||
		input.Generation < 1 ||
		input.Identity.Validate() != nil {
		return nil, store.NewErrInvalidInput("attempt_environment_binding", "bind", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "bind Attempt execution environment", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptEnvironmentBindResult, error) {
		participationID, databaseNow, err := lockAttemptEnvironmentGeneration(ctx, tx, input.AttemptID, input.Generation)
		if err != nil {
			return nil, err
		}

		live, err := lockLiveAttemptEnvironmentBinding(ctx, tx, input.AttemptID)
		if err != nil {
			return nil, err
		}

		result := &store.ExamAttemptEnvironmentBindResult{}
		if live != nil {
			if live.Generation == input.Generation && live.Identity == input.Identity {
				result.Binding, result.Repeated = *live, true
				return result, nil
			}
			// A stale owner may not displace a newer incarnation of the
			// same generation.
			if live.Generation == input.Generation && input.Identity.Incarnation <= live.Identity.Incarnation {
				return nil, store.NewErrConflict("attempt_environment_binding", "attempt_environment_binding_identity", nil)
			}
			if live, err = endAttemptEnvironmentBinding(ctx, tx, live.ID, databaseNow); err != nil {
				return nil, err
			}
			result.Replaced = live
		}

		// A grant belongs to one Attempt for good, and a grant and epoch keep
		// the incarnation they were first bound with. The same grant and
		// epoch, and so the same shell, may serve each later generation.
		var mismatched bool
		if err = tx.Get(ctx, &mismatched, `SELECT EXISTS (
				SELECT 1
				FROM exam_attempt_environment_bindings
				WHERE grant_id = ?
					AND (exam_attempt_id <> ? OR (epoch = ? AND incarnation <> ?))
			)`, input.Identity.GrantID, input.AttemptID.String(), input.Identity.Epoch, input.Identity.Incarnation); err != nil {
			return nil, fmt.Errorf("check execution environment grant ownership: %w", err)
		}
		if mismatched {
			return nil, store.NewErrConflict("attempt_environment_binding", "attempt_environment_binding_identity", nil)
		}

		var row attemptEnvironmentBindingRow
		if err = tx.Get(
			ctx,
			&row,
			`INSERT INTO exam_attempt_environment_bindings (
				id, exam_attempt_id, participation_id, generation, grant_id, epoch, incarnation, created_at, ended_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)
			RETURNING id, exam_attempt_id, participation_id, generation, grant_id, epoch, incarnation,
				created_at, ended_at`,
			input.BindingID.String(),
			input.AttemptID.String(),
			participationID,
			input.Generation,
			input.Identity.GrantID,
			input.Identity.Epoch,
			input.Identity.Incarnation,
			databaseNow,
		); err != nil {
			translated := translateError("attempt_environment_binding", input.BindingID.String(), err)
			if store.IsConflict(translated) {
				return nil, store.NewErrConflict("attempt_environment_binding", "attempt_environment_binding_identity", err)
			}
			return nil, fmt.Errorf("bind Attempt execution environment: %w", translated)
		}

		binding, err := row.model()
		if err != nil {
			return nil, err
		}
		result.Binding = *binding
		return result, nil
	})
}

// EndEnvironmentBinding ends one binding of the Attempt. Ending a binding
// that has already ended returns it with Changed false.
func (s *SQLExamAttemptWorkspaceStore) EndEnvironmentBinding(
	ctx context.Context,
	input *store.ExamAttemptEnvironmentBindingEnd,
) (*store.ExamAttemptEnvironmentBindingEndResult, error) {
	if input == nil || !input.AttemptID.IsValid() || !input.BindingID.IsValid() {
		return nil, store.NewErrInvalidInput("attempt_environment_binding", "end", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "end Attempt execution environment binding", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptEnvironmentBindingEndResult, error) {
		if err := lockAttemptForEnvironmentBinding(ctx, tx, input.AttemptID); err != nil {
			return nil, err
		}

		var row attemptEnvironmentBindingRow
		if err := tx.Get(
			ctx,
			&row,
			attemptEnvironmentBindingSelect+` WHERE id = ? AND exam_attempt_id = ? FOR UPDATE`,
			input.BindingID.String(),
			input.AttemptID.String(),
		); err != nil {
			return nil, translateError("attempt_environment_binding", input.BindingID.String(), err)
		}

		binding, err := row.model()
		if err != nil {
			return nil, err
		}
		if !binding.Live() {
			return &store.ExamAttemptEnvironmentBindingEndResult{Binding: *binding}, nil
		}

		var databaseNow time.Time
		if err = tx.Get(ctx, &databaseNow, `SELECT statement_timestamp()`); err != nil {
			return nil, fmt.Errorf("read execution environment binding end time: %w", err)
		}
		if binding, err = endAttemptEnvironmentBinding(ctx, tx, binding.ID, model.TimeUTC(databaseNow)); err != nil {
			return nil, err
		}
		return &store.ExamAttemptEnvironmentBindingEndResult{Binding: *binding, Changed: true}, nil
	})
}

// lockAttemptForEnvironmentBinding takes the Attempt row lock every binding
// change starts from, the same lock every Participation-ending path holds.
func lockAttemptForEnvironmentBinding(ctx context.Context, tx *sqlxTxWrapper, attemptID model.ExamAttemptID) error {
	var id string
	if err := tx.Get(ctx, &id, `SELECT id FROM exam_attempts WHERE id = ? FOR UPDATE`, attemptID.String()); err != nil {
		return translateError("exam_attempt", attemptID.String(), err)
	}
	return nil
}

// lockAttemptEnvironmentGeneration requires the Attempt to be Active and the
// generation to be its active Participation with an unexpired lease, and
// returns that Participation and the decision instant.
func lockAttemptEnvironmentGeneration(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attemptID model.ExamAttemptID,
	generation int64,
) (string, time.Time, error) {
	var attemptState string
	if err := tx.Get(ctx, &attemptState, `SELECT state FROM exam_attempts WHERE id = ? FOR UPDATE`, attemptID.String()); err != nil {
		return "", time.Time{}, translateError("exam_attempt", attemptID.String(), err)
	}

	var participation struct {
		ID             string    `db:"id"`
		State          string    `db:"state"`
		LeaseExpiresAt time.Time `db:"lease_expires_at"`
		DatabaseNow    time.Time `db:"database_now"`
	}
	err := tx.Get(ctx, &participation, `SELECT id, state, lease_expires_at, statement_timestamp() AS database_now
		FROM exam_attempt_participations
		WHERE exam_attempt_id = ?
			AND generation = ?
		FOR SHARE`, attemptID.String(), generation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, store.NewErrConflict("attempt_participation", "attempt_participation_generation", nil)
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("lock execution environment Participation: %w", err)
	}

	databaseNow := model.TimeUTC(participation.DatabaseNow)
	if attemptState != string(model.ExamAttemptActive) {
		return "", time.Time{}, store.NewErrConflict("exam_attempt", "exam_attempt_state", nil)
	}
	if participation.State != string(model.AttemptParticipationActive) {
		return "", time.Time{}, store.NewErrConflict("attempt_participation", "attempt_participation_generation", nil)
	}
	if !databaseNow.Before(participation.LeaseExpiresAt) {
		return "", time.Time{}, store.NewErrConflict("attempt_participation", "attempt_participation_expired", nil)
	}

	return participation.ID, databaseNow, nil
}

func lockLiveAttemptEnvironmentBinding(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attemptID model.ExamAttemptID,
) (*model.AttemptEnvironmentBinding, error) {
	var row attemptEnvironmentBindingRow
	err := tx.Get(
		ctx,
		&row,
		attemptEnvironmentBindingSelect+` WHERE exam_attempt_id = ? AND ended_at IS NULL FOR UPDATE`,
		attemptID.String(),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock live execution environment binding: %w", err)
	}
	return row.model()
}

func endAttemptEnvironmentBinding(
	ctx context.Context,
	tx *sqlxTxWrapper,
	bindingID model.AttemptEnvironmentBindingID,
	at time.Time,
) (*model.AttemptEnvironmentBinding, error) {
	var row attemptEnvironmentBindingRow
	if err := tx.Get(
		ctx,
		&row,
		`UPDATE exam_attempt_environment_bindings
		SET ended_at = GREATEST(?, created_at)
		WHERE id = ?
			AND ended_at IS NULL
		RETURNING id, exam_attempt_id, participation_id, generation, grant_id, epoch, incarnation,
			created_at, ended_at`,
		at,
		bindingID.String(),
	); err != nil {
		return nil, fmt.Errorf("end execution environment binding: %w", translateError("attempt_environment_binding", bindingID.String(), err))
	}
	return row.model()
}

// lockAttemptEnvironmentReadGuard is the environment's read check: a live
// binding matching the access, its generation the Attempt's active
// Participation with an unexpired lease, an Active Attempt, a readable
// Sitting before its deadline and an active candidate account. It mirrors
// the candidate read guard without the Desktop Session and Connection.
func lockAttemptEnvironmentReadGuard(
	ctx context.Context,
	tx *sqlxTxWrapper,
	access store.ExamAttemptWorkspaceEnvironmentAccess,
) error {
	if !access.Valid() {
		return store.NewErrInvalidInput("attempt_workspace", "access", nil)
	}

	var row struct {
		AttemptState       string       `db:"attempt_state"`
		SittingState       string       `db:"sitting_state"`
		ScheduledEndAt     time.Time    `db:"scheduled_end_at"`
		ParticipationState string       `db:"participation_state"`
		Generation         int64        `db:"generation"`
		LeaseExpiresAt     time.Time    `db:"lease_expires_at"`
		GrantID            string       `db:"grant_id"`
		Epoch              string       `db:"epoch"`
		Incarnation        int64        `db:"incarnation"`
		EndedAt            sql.NullTime `db:"ended_at"`
		UserArchivedAt     sql.NullTime `db:"user_archived_at"`
		UserDisabledAt     sql.NullTime `db:"user_disabled_at"`
		DatabaseNow        time.Time    `db:"database_now"`
	}
	err := tx.Get(
		ctx,
		&row,
		`SELECT a.state AS attempt_state, s.state AS sitting_state, s.scheduled_end_at,
			p.state AS participation_state, p.generation, p.lease_expires_at,
			b.grant_id, b.epoch, b.incarnation, b.ended_at,
			u.archived_at AS user_archived_at, u.disabled_at AS user_disabled_at,
			statement_timestamp() AS database_now
		FROM exam_attempts a
		JOIN exam_sittings s ON s.id = a.exam_sitting_id
			AND s.exam_id = a.exam_id
		JOIN exam_attempt_environment_bindings b ON b.id = ?
			AND b.exam_attempt_id = a.id
		JOIN exam_attempt_participations p ON p.id = b.participation_id
			AND p.exam_attempt_id = a.id
			AND p.generation = b.generation
		JOIN users u ON u.id = a.candidate_user_id
		WHERE a.id = ?
		FOR SHARE OF a, s, b, p, u`,
		access.BindingID.String(),
		access.AttemptID.String(),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return store.NewErrNotFound("exam_attempt_access", access.AttemptID.String())
	}
	if err != nil {
		return fmt.Errorf("lock execution environment Attempt access: %w", err)
	}

	databaseNow := model.TimeUTC(row.DatabaseNow)
	if row.EndedAt.Valid ||
		row.Generation != access.Generation ||
		row.GrantID != access.Identity.GrantID ||
		row.Epoch != access.Identity.Epoch ||
		row.Incarnation != access.Identity.Incarnation ||
		row.AttemptState != string(model.ExamAttemptActive) ||
		row.ParticipationState != string(model.AttemptParticipationActive) ||
		!databaseNow.Before(row.LeaseExpiresAt) ||
		(row.SittingState != string(model.ExamSittingOpen) && row.SittingState != string(model.ExamSittingPaused)) ||
		!databaseNow.Before(row.ScheduledEndAt) ||
		row.UserArchivedAt.Valid ||
		row.UserDisabledAt.Valid {
		return store.NewErrNotFound("exam_attempt_access", access.AttemptID.String())
	}

	return nil
}
