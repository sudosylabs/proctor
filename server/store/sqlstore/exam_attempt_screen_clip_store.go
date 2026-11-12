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

// screenClipMaxFuture bounds how far past the request a manager's range may
// end, so Desktop never waits long for the future part of a range.
const screenClipMaxFuture = 10 * time.Minute

const screenClipColumns = `id, exam_attempt_id, source, requested_by_user_id, private_reason, integrity_flag_id,
	range_from_session_start, range_start, range_end, state, failure, delivered_participation_id,
	requested_at, updated_at, revision`

type screenClipRow struct {
	ID                       string         `db:"id"`
	AttemptID                string         `db:"exam_attempt_id"`
	Source                   string         `db:"source"`
	RequestedByUserID        sql.NullString `db:"requested_by_user_id"`
	PrivateReason            sql.NullString `db:"private_reason"`
	FlagID                   sql.NullString `db:"integrity_flag_id"`
	RangeFromSessionStart    bool           `db:"range_from_session_start"`
	RangeStart               sql.NullTime   `db:"range_start"`
	RangeEnd                 time.Time      `db:"range_end"`
	State                    string         `db:"state"`
	Failure                  sql.NullString `db:"failure"`
	DeliveredParticipationID sql.NullString `db:"delivered_participation_id"`
	RequestedAt              time.Time      `db:"requested_at"`
	UpdatedAt                time.Time      `db:"updated_at"`
	Revision                 int64          `db:"revision"`
}

func (row screenClipRow) model() (*model.ScreenClip, error) {
	id, err := model.ParseScreenClipID(row.ID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "id", err)
	}
	attemptID, err := model.ParseExamAttemptID(row.AttemptID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "exam_attempt_id", err)
	}
	clip := &model.ScreenClip{
		ID:            id,
		AttemptID:     attemptID,
		Source:        model.ScreenClipSource(row.Source),
		PrivateReason: row.PrivateReason.String,
		Range: model.ScreenClipRange{
			FromSessionStart: row.RangeFromSessionStart,
			End:              model.TimeUTC(row.RangeEnd),
		},
		State:       model.ScreenClipState(row.State),
		Failure:     model.ScreenClipFailure(row.Failure.String),
		RequestedAt: model.TimeUTC(row.RequestedAt),
		UpdatedAt:   model.TimeUTC(row.UpdatedAt),
		Revision:    row.Revision,
	}
	if row.RangeStart.Valid {
		clip.Range.Start = model.TimeUTC(row.RangeStart.Time)
	}
	if row.RequestedByUserID.Valid {
		if clip.RequestedByUserID, err = model.ParseUserID(row.RequestedByUserID.String); err != nil {
			return nil, invalidPersistedState("screen_clip", "requested_by_user_id", err)
		}
	}
	if row.FlagID.Valid {
		if clip.FlagID, err = model.ParseIntegrityFlagID(row.FlagID.String); err != nil {
			return nil, invalidPersistedState("screen_clip", "integrity_flag_id", err)
		}
	}
	if row.DeliveredParticipationID.Valid {
		if clip.DeliveredParticipationID, err = model.ParseAttemptParticipationID(row.DeliveredParticipationID.String); err != nil {
			return nil, invalidPersistedState("screen_clip", "delivered_participation_id", err)
		}
	}
	if err = clip.Validate(); err != nil {
		return nil, invalidPersistedState("screen_clip", "value", err)
	}
	return clip, nil
}

// screenClipOutcome is a request's replayable outcome. Like the view, it
// carries no private reason.
type screenClipOutcome struct {
	ExamID          string                  `json:"exam_id"`
	SittingID       string                  `json:"sitting_id"`
	CandidateUserID string                  `json:"candidate_user_id"`
	Clip            screenClipOutcomeRecord `json:"clip"`
}

type screenClipOutcomeRecord struct {
	ID                    string     `json:"id"`
	AttemptID             string     `json:"exam_attempt_id"`
	RequestedByUserID     string     `json:"requested_by_user_id"`
	RangeFromSessionStart bool       `json:"range_from_session_start"`
	RangeStart            *time.Time `json:"range_start"`
	RangeEnd              time.Time  `json:"range_end"`
	RequestedAt           time.Time  `json:"requested_at"`
}

func (screenClipOutcome) outcomeDocument() {}

func (o screenClipOutcome) result() (*store.ExamAttemptScreenClipResult, error) {
	examID, err := model.ParseExamID(o.ExamID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "exam_id", err)
	}
	sittingID, err := model.ParseExamSittingID(o.SittingID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "sitting_id", err)
	}
	candidateID, err := model.ParseUserID(o.CandidateUserID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "candidate_user_id", err)
	}
	clipID, err := model.ParseScreenClipID(o.Clip.ID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "id", err)
	}
	attemptID, err := model.ParseExamAttemptID(o.Clip.AttemptID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "exam_attempt_id", err)
	}
	managerID, err := model.ParseUserID(o.Clip.RequestedByUserID)
	if err != nil {
		return nil, invalidPersistedState("screen_clip", "requested_by_user_id", err)
	}
	clipRange := model.ScreenClipRange{FromSessionStart: o.Clip.RangeFromSessionStart, End: model.TimeUTC(o.Clip.RangeEnd)}
	if o.Clip.RangeStart != nil {
		clipRange.Start = model.TimeUTC(*o.Clip.RangeStart)
	}
	if clipRange.Validate() != nil || o.Clip.RequestedAt.IsZero() {
		return nil, invalidPersistedState("screen_clip", "outcome", nil)
	}
	// The outcome records the request as it was made; its later life is read
	// from the clip itself.
	return &store.ExamAttemptScreenClipResult{
		ExamID: examID, SittingID: sittingID, CandidateUserID: candidateID,
		Clip: store.ExamAttemptScreenClipView{
			ID: clipID, AttemptID: attemptID, Source: model.ScreenClipSourceManager, RequestedByUserID: managerID,
			Range: clipRange, State: model.ScreenClipRequested, RequestedAt: model.TimeUTC(o.Clip.RequestedAt),
			UpdatedAt: model.TimeUTC(o.Clip.RequestedAt), Revision: 1,
		},
	}, nil
}

// RequestScreenClip saves an Exam Manager's request.
func (s *SQLExamAttemptStore) RequestScreenClip(
	ctx context.Context,
	input *store.ExamAttemptScreenClipRequest,
	command *store.CommandIdempotency,
) (*store.ExamAttemptScreenClipResult, error) {
	if input == nil || input.Clip == nil || input.Clip.Validate() != nil ||
		input.Clip.Source != model.ScreenClipSourceManager || input.Clip.State != model.ScreenClipRequested ||
		!input.ExamID.IsValid() || !input.SittingID.IsValid() ||
		!input.AuditEventID.IsValid() || input.AuditAt.IsZero() || command == nil {
		return nil, store.NewErrInvalidInput("screen_clip", "request", nil)
	}

	result, err := runIdempotentMutation(ctx, s.SQLStore, "Exam Attempt screen clip request", idempotentMutation[*store.ExamAttemptScreenClipResult]{
		command:      command,
		auditEventID: input.AuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (*store.ExamAttemptScreenClipResult, error) {
			return requestScreenClip(ctx, tx, input)
		},
		encode: func(value *store.ExamAttemptScreenClipResult) ([]byte, error) {
			record := screenClipOutcomeRecord{
				ID:                    value.Clip.ID.String(),
				AttemptID:             value.Clip.AttemptID.String(),
				RequestedByUserID:     value.Clip.RequestedByUserID.String(),
				RangeFromSessionStart: value.Clip.Range.FromSessionStart,
				RangeEnd:              value.Clip.Range.End,
				RequestedAt:           value.Clip.RequestedAt,
			}
			if !value.Clip.Range.Start.IsZero() {
				start := value.Clip.Range.Start
				record.RangeStart = &start
			}
			return encodeOutcome(screenClipOutcome{
				ExamID: value.ExamID.String(), SittingID: value.SittingID.String(),
				CandidateUserID: value.CandidateUserID.String(), Clip: record,
			})
		},
		decode: func(data []byte) (*store.ExamAttemptScreenClipResult, error) {
			var outcome screenClipOutcome
			if err := decodeOutcome(data, &outcome); err != nil {
				return nil, invalidPersistedState("screen_clip", "outcome", err)
			}
			return outcome.result()
		},
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, value *store.ExamAttemptScreenClipResult, originalAuditID string) error {
			encoded, err := model.EncodeAuditData(map[string]any{
				"screen_clip_id":          value.Clip.ID.String(),
				"idempotency_replayed":    true,
				"original_audit_event_id": originalAuditID,
			})
			if err != nil {
				return err
			}
			_, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", encoded, input.AuditAt)
			return err
		},
	})
	if err != nil {
		return nil, err
	}
	result.Value.Replayed = result.Replayed
	return result.Value, nil
}

func requestScreenClip(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamAttemptScreenClipRequest,
) (*store.ExamAttemptScreenClipResult, error) {
	clip := *input.Clip
	var attempt struct {
		ExamID          string `db:"exam_id"`
		SittingID       string `db:"exam_sitting_id"`
		CandidateUserID string `db:"candidate_user_id"`
		State           string `db:"state"`
		Recording       bool   `db:"recording"`
	}
	err := tx.Get(ctx, &attempt, `SELECT a.exam_id, a.exam_sitting_id, a.candidate_user_id, a.state,
			COALESCE((r.policy_document #>> '{native,screen_recording,enabled}')::boolean, false) AS recording
		FROM exam_attempts a
		JOIN exam_revisions r ON r.id = a.admission_revision_id
		WHERE a.id = ?
		FOR UPDATE OF a`, clip.AttemptID.String())
	if err != nil {
		return nil, translateError("exam_attempt", clip.AttemptID.String(), err)
	}
	switch {
	case attempt.ExamID != input.ExamID.String() || attempt.SittingID != input.SittingID.String() ||
		attempt.CandidateUserID == clip.RequestedByUserID.String():
		return nil, store.NewErrNotFound("exam_attempt", clip.AttemptID.String())
	case attempt.State != string(model.ExamAttemptActive) && attempt.State != string(model.ExamAttemptSuspended):
		return nil, store.NewErrConflict("exam_attempt", "exam_attempt_state", nil)
	case !attempt.Recording:
		return nil, store.NewErrConflict("screen_clip", "screen_recording_disabled", nil)
	}

	var databaseNow time.Time
	if err = tx.Get(ctx, &databaseNow, `SELECT statement_timestamp()`); err != nil {
		return nil, fmt.Errorf("read screen clip request time: %w", err)
	}
	databaseNow = model.TimeUTC(databaseNow)
	if clip.Range.End.After(databaseNow.Add(screenClipMaxFuture)) {
		return nil, store.NewErrInvalidInput("screen_clip", "range", nil)
	}
	clip.RequestedAt, clip.UpdatedAt = databaseNow, databaseNow

	var requests int
	if err = tx.Get(ctx, &requests, `SELECT count(*) FROM exam_attempt_screen_clips
		WHERE exam_attempt_id = ? AND source = ?`, clip.AttemptID.String(), clip.Source); err != nil {
		return nil, fmt.Errorf("count screen clip requests: %w", err)
	}
	if requests >= model.ScreenClipManagerRequestLimit {
		return nil, store.NewErrConflict("screen_clip", "screen_clip_limit", nil)
	}

	if err = insertScreenClip(ctx, tx, &clip); err != nil {
		return nil, err
	}
	audit, err := model.EncodeAuditData(map[string]any{
		"exam_attempt_id":          clip.AttemptID.String(),
		"screen_clip_id":           clip.ID.String(),
		"range_from_session_start": clip.Range.FromSessionStart,
	})
	if err != nil {
		return nil, err
	}
	if _, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", audit, input.AuditAt); err != nil {
		return nil, fmt.Errorf("complete screen clip request audit: %w", err)
	}

	candidateID, err := model.ParseUserID(attempt.CandidateUserID)
	if err != nil {
		return nil, invalidPersistedState("exam_attempt", "candidate_user_id", err)
	}
	return &store.ExamAttemptScreenClipResult{
		ExamID: input.ExamID, SittingID: input.SittingID, CandidateUserID: candidateID,
		Clip: store.NewExamAttemptScreenClipView(&clip),
	}, nil
}

func insertScreenClip(ctx context.Context, tx *sqlxTxWrapper, clip *model.ScreenClip) error {
	if err := clip.Validate(); err != nil {
		return err
	}
	var rangeStart any
	if !clip.Range.Start.IsZero() {
		rangeStart = clip.Range.Start
	}
	if _, err := tx.Exec(ctx, `INSERT INTO exam_attempt_screen_clips (`+screenClipColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?)`,
		clip.ID.String(), clip.AttemptID.String(), clip.Source,
		nullableString(clip.RequestedByUserID.String()), nullableString(clip.PrivateReason), nullableString(clip.FlagID.String()),
		clip.Range.FromSessionStart, rangeStart, clip.Range.End, clip.State,
		clip.RequestedAt, clip.UpdatedAt, clip.Revision,
	); err != nil {
		return translateError("screen_clip", clip.ID.String(), err)
	}
	return nil
}

// ListPendingScreenClips returns the requests the live Desktop has not taken.
func (s *SQLExamAttemptStore) ListPendingScreenClips(
	ctx context.Context,
	access store.CandidateAttemptAccess,
) ([]store.ExamAttemptScreenClipView, error) {
	return runSQLTransaction(ctx, s.GetMaster().Begin, "list pending screen clips", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) ([]store.ExamAttemptScreenClipView, error) {
		if _, err := lockCandidateGuard(ctx, tx, access); err != nil {
			return nil, err
		}
		var rows []screenClipRow
		if err := tx.Select(ctx, &rows, `SELECT `+screenClipColumns+` FROM exam_attempt_screen_clips
			WHERE exam_attempt_id = ? AND state = 'requested'
			ORDER BY requested_at, id
			LIMIT ?`, access.AttemptID.String(), model.ScreenClipManagerRequestLimit+model.ScreenClipIncidentRequestLimit); err != nil {
			return nil, fmt.Errorf("list pending screen clips: %w", err)
		}
		clips := make([]store.ExamAttemptScreenClipView, 0, len(rows))
		for _, row := range rows {
			clip, err := row.model()
			if err != nil {
				return nil, err
			}
			clips = append(clips, store.NewExamAttemptScreenClipView(clip))
		}
		return clips, nil
	})
}

// DeliverScreenClip records that the live Desktop took a request.
func (s *SQLExamAttemptStore) DeliverScreenClip(
	ctx context.Context,
	input *store.ExamAttemptScreenClipDelivery,
) (*store.ExamAttemptScreenClipView, error) {
	if input == nil || !input.ClipID.IsValid() {
		return nil, store.NewErrInvalidInput("screen_clip", "delivery", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "deliver screen clip", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptScreenClipView, error) {
		if _, err := lockCandidateGuard(ctx, tx, input.Access); err != nil {
			return nil, err
		}
		var participation string
		if err := tx.Get(ctx, &participation, `SELECT participation_id FROM exam_attempt_connections
			WHERE id = ? AND exam_attempt_id = ?`, input.Access.ConnectionID.String(), input.Access.AttemptID.String()); err != nil {
			return nil, translateError("attempt_connection", input.Access.ConnectionID.String(), err)
		}
		participationID, err := model.ParseAttemptParticipationID(participation)
		if err != nil {
			return nil, invalidPersistedState("attempt_connection", "participation_id", err)
		}

		var row screenClipRow
		err = tx.Get(ctx, &row, `SELECT `+screenClipColumns+` FROM exam_attempt_screen_clips
			WHERE id = ? AND exam_attempt_id = ?
			FOR UPDATE`, input.ClipID.String(), input.Access.AttemptID.String())
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.NewErrNotFound("screen_clip", input.ClipID.String())
		}
		if err != nil {
			return nil, fmt.Errorf("lock screen clip: %w", err)
		}
		clip, err := row.model()
		if err != nil {
			return nil, err
		}
		if clip.State != model.ScreenClipRequested {
			if clip.DeliveredParticipationID == participationID {
				view := store.NewExamAttemptScreenClipView(clip)
				return &view, nil
			}
			return nil, store.NewErrConflict("screen_clip", "screen_clip_state", nil)
		}

		var databaseNow time.Time
		if err = tx.Get(ctx, &databaseNow, `SELECT statement_timestamp()`); err != nil {
			return nil, fmt.Errorf("read screen clip delivery time: %w", err)
		}
		if err = clip.Deliver(participationID, databaseNow); err != nil {
			return nil, store.NewErrInvalidInput("screen_clip", "delivery", nil).Wrap(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_screen_clips
			SET state = ?, delivered_participation_id = ?, updated_at = ?, revision = ?
			WHERE id = ? AND revision = ?`,
			clip.State, clip.DeliveredParticipationID.String(), clip.UpdatedAt, clip.Revision,
			clip.ID.String(), clip.Revision-1); err != nil {
			return nil, translateError("screen_clip", clip.ID.String(), err)
		}
		view := store.NewExamAttemptScreenClipView(clip)
		return &view, nil
	})
}
