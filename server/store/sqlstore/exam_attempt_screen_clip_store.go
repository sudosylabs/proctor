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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// screenClipMaxFuture bounds how far past the request a manager's range may
// end, so Desktop never waits long for the future part of a range.
const screenClipMaxFuture = 10 * time.Minute

var screenClipColumnNames = []string{"id", "exam_attempt_id", "source", "requested_by_user_id", "private_reason",
	"integrity_flag_id", "range_from_session_start", "range_start", "range_end", "state", "failure",
	"delivered_participation_id", "media_type", "size_bytes", "chunk_sha256", "content_digest", "content_signature",
	"requested_at", "updated_at", "revision"}

var (
	screenClipColumns = strings.Join(screenClipColumnNames, ", ")
	// prefixedScreenClipColumns are screenClipColumns read through the alias c.
	prefixedScreenClipColumns = "c." + strings.Join(screenClipColumnNames, ", c.")
)

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
	MediaType                sql.NullString `db:"media_type"`
	SizeBytes                sql.NullInt64  `db:"size_bytes"`
	ChunkSHA256              pq.StringArray `db:"chunk_sha256"`
	ContentDigest            sql.NullString `db:"content_digest"`
	ContentSignature         sql.NullString `db:"content_signature"`
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
		Signature:   row.ContentSignature.String,
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
	if row.MediaType.Valid {
		clip.Upload = &model.ScreenClipUpload{
			MediaType: row.MediaType.String, SizeBytes: row.SizeBytes.Int64, ChunkSHA256: []string(row.ChunkSHA256),
		}
		if clip.Upload.Validate() != nil || clip.Upload.Digest() != row.ContentDigest.String {
			return nil, invalidPersistedState("screen_clip", "upload", nil)
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
			`+screenRecordingEnabledSQL+` AS recording
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

	databaseNow, err := screenClipStatementTime(ctx, tx)
	if err != nil {
		return nil, err
	}
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

// requestIncidentScreenClip asks Desktop for the minute either side of the
// moment a Flag opened, when the Attempt's policy records the screen. Focus
// Loss, Connection Loss and native Flags qualify; at most
// model.ScreenClipIncidentRequestLimit are asked for per Attempt, and any
// further Flag goes without one.
func requestIncidentScreenClip(ctx context.Context, tx *sqlxTxWrapper, flag *model.IntegrityFlag) (bool, error) {
	if flag.Kind == model.IntegrityPolicyBrowserNavigation {
		return false, nil
	}
	var attempt struct {
		Recording bool `db:"recording"`
		Incidents int  `db:"incidents"`
	}
	if err := tx.Get(ctx, &attempt, `SELECT
			`+screenRecordingEnabledSQL+` AS recording,
			(SELECT count(*) FROM exam_attempt_screen_clips c WHERE c.exam_attempt_id = a.id AND c.source = ?) AS incidents
		FROM exam_attempts a
		JOIN exam_revisions r ON r.id = a.admission_revision_id
		WHERE a.id = ?`, model.ScreenClipSourceIncident, flag.AttemptID.String()); err != nil {
		return false, translateError("exam_attempt", flag.AttemptID.String(), err)
	}
	if !attempt.Recording || attempt.Incidents >= model.ScreenClipIncidentRequestLimit {
		return false, nil
	}
	clip, err := model.NewIncidentScreenClip(model.NewScreenClipID(), flag.AttemptID, flag.ID, flag.CreatedAt)
	if err != nil {
		return false, err
	}
	return true, insertScreenClip(ctx, tx, clip)
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?, ?)`,
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
		clip, participationID, err := lockLiveScreenClip(ctx, tx, input.Access, input.ClipID)
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

		now, err := screenClipStatementTime(ctx, tx)
		if err != nil {
			return nil, err
		}
		if err = clip.Deliver(participationID, now); err != nil {
			return nil, store.NewErrInvalidInput("screen_clip", "delivery", nil).Wrap(err)
		}
		return updateScreenClip(ctx, tx, clip)
	})
}

// lockLiveScreenClip locks a clip of the Attempt the live Connection serves,
// and returns the Participation that Connection belongs to.
func lockLiveScreenClip(
	ctx context.Context,
	tx *sqlxTxWrapper,
	access store.CandidateAttemptAccess,
	clipID model.ScreenClipID,
) (*model.ScreenClip, model.AttemptParticipationID, error) {
	if access.ConnectionID == "" && access.ContinuityCredentialHash == "" {
		return lockClosedScreenClip(ctx, tx, access, clipID)
	}
	if _, err := lockCandidateGuard(ctx, tx, access); err != nil {
		return nil, "", err
	}
	var participation string
	if err := tx.Get(ctx, &participation, `SELECT participation_id FROM exam_attempt_connections
		WHERE id = ? AND exam_attempt_id = ?`, access.ConnectionID.String(), access.AttemptID.String()); err != nil {
		return nil, "", translateError("attempt_connection", access.ConnectionID.String(), err)
	}
	participationID, err := model.ParseAttemptParticipationID(participation)
	if err != nil {
		return nil, "", invalidPersistedState("attempt_connection", "participation_id", err)
	}

	var row screenClipRow
	err = tx.Get(ctx, &row, `SELECT `+screenClipColumns+` FROM exam_attempt_screen_clips
		WHERE id = ? AND exam_attempt_id = ?
		FOR UPDATE`, clipID.String(), access.AttemptID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", store.NewErrNotFound("screen_clip", clipID.String())
	}
	if err != nil {
		return nil, "", fmt.Errorf("lock screen clip: %w", err)
	}
	clip, err := row.model()
	if err != nil {
		return nil, "", err
	}
	return clip, participationID, nil
}

// lockClosedScreenClip locks a clip whose upload continues after the
// Participation that took it closed. The candidate's registered Desktop
// Session must be the one that held that Participation, and its upload
// deadline must not have passed.
func lockClosedScreenClip(
	ctx context.Context,
	tx *sqlxTxWrapper,
	access store.CandidateAttemptAccess,
	clipID model.ScreenClipID,
) (*model.ScreenClip, model.AttemptParticipationID, error) {
	var owner struct {
		StreamID      string       `db:"delivery_stream_id"`
		Participation string       `db:"participation_id"`
		Registration  string       `db:"registration_id"`
		Thumbprint    string       `db:"key_thumbprint"`
		UploadExpires sql.NullTime `db:"upload_expires_at"`
	}
	err := tx.Get(ctx, &owner, `SELECT o.delivery_stream_id, o.participation_id, o.registration_id, o.key_thumbprint,
			o.upload_expires_at
		FROM exam_attempt_screen_clips c
		JOIN exam_attempt_security_owners o ON o.participation_id = c.delivered_participation_id
		WHERE c.id = ? AND c.exam_attempt_id = ?`, clipID.String(), access.AttemptID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", store.NewErrNotFound("screen_clip", clipID.String())
	}
	if err != nil {
		return nil, "", fmt.Errorf("read screen clip owner: %w", err)
	}
	if err = lockNativeDeliveryAccess(ctx, tx, store.NativeDeliveryAccess{Access: access, StreamID: owner.StreamID}); err != nil {
		return nil, "", err
	}
	if owner.Registration != access.DesktopRegistrationID.String() || owner.Thumbprint != access.DPoPKeyThumbprint {
		return nil, "", store.NewErrNotFound("screen_clip", clipID.String())
	}
	if !owner.UploadExpires.Valid {
		// The Participation is still live, so the upload needs its credential.
		return nil, "", store.NewErrInvalidInput("exam_attempt", "candidate_access", nil)
	}

	var row screenClipRow
	if err = tx.Get(ctx, &row, `SELECT `+screenClipColumns+` FROM exam_attempt_screen_clips
		WHERE id = ? AND exam_attempt_id = ?
		FOR UPDATE`, clipID.String(), access.AttemptID.String()); err != nil {
		return nil, "", fmt.Errorf("lock screen clip: %w", err)
	}
	clip, err := row.model()
	if err != nil {
		return nil, "", err
	}
	now, err := screenClipStatementTime(ctx, tx)
	if err != nil {
		return nil, "", err
	}
	if !now.Before(owner.UploadExpires.Time) {
		// The delivery expiry pass fails the clip itself.
		return nil, "", model.ErrDeliveryExpired
	}
	participationID, err := model.ParseAttemptParticipationID(owner.Participation)
	if err != nil {
		return nil, "", invalidPersistedState("attempt_participation", "id", err)
	}
	return clip, participationID, nil
}

// failExpiredScreenClipUploads fails the clips a Participation took and did
// not finish uploading before its upload deadline. The delivery expiry pass
// calls it when that deadline passes.
func failExpiredScreenClipUploads(ctx context.Context, tx *sqlxTxWrapper, participation model.AttemptParticipationID, now time.Time) error {
	var rows []screenClipRow
	if err := tx.Select(ctx, &rows, `SELECT `+screenClipColumns+` FROM exam_attempt_screen_clips
		WHERE delivered_participation_id = ? AND state IN ('recording', 'uploading')
		ORDER BY id
		FOR UPDATE`, participation.String()); err != nil {
		return fmt.Errorf("lock unfinished screen clips: %w", err)
	}
	for _, row := range rows {
		clip, err := row.model()
		if err != nil {
			return err
		}
		if err = clip.Fail(model.ScreenClipFailureUploadDeadline, model.TimeUTC(now)); err != nil {
			return err
		}
		if _, err = updateScreenClip(ctx, tx, clip); err != nil {
			return err
		}
	}
	return nil
}

// screenRecordingEnabledSQL reads whether an Attempt's admitted Revision
// records the screen, from exam_revisions r.
const screenRecordingEnabledSQL = `COALESCE((r.policy_document #>> '{native,screen_recording,enabled}')::boolean, false)`

// screenClipStatementTime reads the database's time for the current
// statement, the instant a clip transition is decided and stamped at.
func screenClipStatementTime(ctx context.Context, tx *sqlxTxWrapper) (time.Time, error) {
	var now time.Time
	if err := tx.Get(ctx, &now, `SELECT statement_timestamp()`); err != nil {
		return time.Time{}, fmt.Errorf("read database time: %w", err)
	}
	return model.TimeUTC(now), nil
}

// updateScreenClip writes a clip's next revision behind its previous one.
func updateScreenClip(ctx context.Context, tx *sqlxTxWrapper, clip *model.ScreenClip) (*store.ExamAttemptScreenClipView, error) {
	var mediaType, size, chunks, digest any
	if clip.Upload != nil {
		mediaType, size = clip.Upload.MediaType, clip.Upload.SizeBytes
		chunks, digest = pq.StringArray(clip.Upload.ChunkSHA256), clip.Upload.Digest()
	}
	result, err := tx.Exec(ctx, `UPDATE exam_attempt_screen_clips
		SET state = ?, failure = ?, delivered_participation_id = ?, media_type = ?, size_bytes = ?,
			chunk_sha256 = ?, content_digest = ?, content_signature = ?, updated_at = ?, revision = ?
		WHERE id = ? AND revision = ?`,
		clip.State, nullableString(string(clip.Failure)), nullableString(clip.DeliveredParticipationID.String()),
		mediaType, size, chunks, digest, nullableString(clip.Signature), clip.UpdatedAt, clip.Revision, clip.ID.String(), clip.Revision-1)
	if err != nil {
		return nil, translateError("screen_clip", clip.ID.String(), err)
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
		return nil, store.NewErrConflict("screen_clip", "screen_clip_state", rowsErr)
	}
	view := store.NewExamAttemptScreenClipView(clip)
	return &view, nil
}

// BeginScreenClipUpload records the clip Desktop is about to upload.
func (s *SQLExamAttemptStore) BeginScreenClipUpload(
	ctx context.Context,
	input *store.ExamAttemptScreenClipUploadBegin,
) (*store.ExamAttemptScreenClipView, error) {
	if input == nil || !input.ClipID.IsValid() || input.Upload.Validate() != nil {
		return nil, store.NewErrInvalidInput("screen_clip", "upload", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "begin screen clip upload", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptScreenClipView, error) {
		clip, _, err := lockLiveScreenClip(ctx, tx, input.Access, input.ClipID)
		if err != nil {
			return nil, err
		}
		if clip.State == model.ScreenClipUploading && clip.Upload.Digest() == input.Upload.Digest() {
			view := store.NewExamAttemptScreenClipView(clip)
			return &view, nil
		}
		if clip.State != model.ScreenClipRecording {
			return nil, store.NewErrConflict("screen_clip", "screen_clip_state", nil)
		}

		var stored int64
		if err = tx.Get(ctx, &stored, `SELECT COALESCE(sum(size_bytes), 0) FROM exam_attempt_screen_clips
			WHERE exam_attempt_id = ? AND state IN ('uploading', 'ready')`, clip.AttemptID.String()); err != nil {
			return nil, fmt.Errorf("sum screen clip bytes: %w", err)
		}
		if stored+input.Upload.SizeBytes > model.ScreenClipAttemptMaxBytes {
			return nil, store.NewErrConflict("screen_clip", "screen_clip_bytes", nil)
		}

		now, err := screenClipStatementTime(ctx, tx)
		if err != nil {
			return nil, err
		}
		if err = clip.BeginUpload(input.Upload, now); err != nil {
			return nil, store.NewErrInvalidInput("screen_clip", "upload", nil).Wrap(err)
		}
		return updateScreenClip(ctx, tx, clip)
	})
}

// GetScreenClipUpload returns a clip and the chunks of it that arrived.
func (s *SQLExamAttemptStore) GetScreenClipUpload(
	ctx context.Context,
	access store.CandidateAttemptAccess,
	clipID model.ScreenClipID,
) (*store.ExamAttemptScreenClipUploadStatus, error) {
	if !clipID.IsValid() {
		return nil, store.NewErrInvalidInput("screen_clip", "id", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "get screen clip upload", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptScreenClipUploadStatus, error) {
		clip, _, err := lockLiveScreenClip(ctx, tx, access, clipID)
		if err != nil {
			return nil, err
		}
		received := []int{}
		if err = tx.Select(ctx, &received, `SELECT chunk_index FROM exam_attempt_screen_clip_chunks
			WHERE screen_clip_id = ? ORDER BY chunk_index`, clipID.String()); err != nil {
			return nil, fmt.Errorf("list screen clip chunks: %w", err)
		}
		return &store.ExamAttemptScreenClipUploadStatus{Clip: store.NewExamAttemptScreenClipView(clip), Received: received}, nil
	})
}

// RecordScreenClipChunk records a stored chunk that matches its declared hash.
func (s *SQLExamAttemptStore) RecordScreenClipChunk(ctx context.Context, input *store.ExamAttemptScreenClipChunk) error {
	if input == nil || !input.ClipID.IsValid() {
		return store.NewErrInvalidInput("screen_clip", "chunk", nil)
	}
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "record screen clip chunk", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (struct{}, error) {
		clip, _, err := lockLiveScreenClip(ctx, tx, input.Access, input.ClipID)
		if err != nil {
			return struct{}{}, err
		}
		if clip.State != model.ScreenClipUploading {
			return struct{}{}, store.NewErrConflict("screen_clip", "screen_clip_state", nil)
		}
		if input.Index < 0 || input.Index >= clip.Upload.ChunkCount() ||
			input.SHA256 != clip.Upload.ChunkSHA256[input.Index] || input.SizeBytes != clip.Upload.ChunkSize(input.Index) {
			return struct{}{}, store.NewErrInvalidInput("screen_clip", "chunk", nil)
		}
		now, err := screenClipStatementTime(ctx, tx)
		if err != nil {
			return struct{}{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO exam_attempt_screen_clip_chunks
			(screen_clip_id, chunk_index, sha256, size_bytes, received_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (screen_clip_id, chunk_index) DO NOTHING`,
			input.ClipID.String(), input.Index, input.SHA256, input.SizeBytes, now); err != nil {
			return struct{}{}, translateError("screen_clip_chunk", input.ClipID.String(), err)
		}
		return struct{}{}, nil
	})
	return err
}

// CompleteScreenClipUpload makes a clip whose every chunk arrived ready.
func (s *SQLExamAttemptStore) CompleteScreenClipUpload(
	ctx context.Context,
	input *store.ExamAttemptScreenClipCompletion,
) (*store.ExamAttemptScreenClipView, error) {
	if input == nil || !input.ClipID.IsValid() || len(input.Signature) != model.DesktopSignatureLength {
		return nil, store.NewErrInvalidInput("screen_clip", "completion", nil)
	}
	access, clipID := input.Access, input.ClipID
	return runSQLTransaction(ctx, s.GetMaster().Begin, "complete screen clip upload", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptScreenClipView, error) {
		clip, _, err := lockLiveScreenClip(ctx, tx, access, clipID)
		if err != nil {
			return nil, err
		}
		if clip.State == model.ScreenClipReady {
			view := store.NewExamAttemptScreenClipView(clip)
			return &view, nil
		}
		if clip.State != model.ScreenClipUploading {
			return nil, store.NewErrConflict("screen_clip", "screen_clip_state", nil)
		}
		var received int
		if err = tx.Get(ctx, &received, `SELECT count(*) FROM exam_attempt_screen_clip_chunks
			WHERE screen_clip_id = ?`, clipID.String()); err != nil {
			return nil, fmt.Errorf("count screen clip chunks: %w", err)
		}
		if received != clip.Upload.ChunkCount() {
			return nil, store.NewErrConflict("screen_clip", "screen_clip_incomplete", nil)
		}
		digest := clip.Upload.Digest()
		if input.ContentDigest != digest {
			return nil, store.NewErrInvalidInput("screen_clip", "content_digest", nil)
		}
		var key []byte
		if err = tx.Get(ctx, &key, `SELECT public_jwk FROM desktop_registrations
			WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
			access.DesktopRegistrationID.String(), access.CandidateUserID.String()); err != nil {
			return nil, translateError("desktop_registration", access.DesktopRegistrationID.String(), err)
		}
		var publicKey model.DesktopPublicJWK
		if err = json.Unmarshal(key, &publicKey); err != nil {
			return nil, invalidPersistedState("desktop_registration", "public_jwk", err)
		}
		if !publicKey.VerifySignature([]byte(digest), input.Signature) {
			return nil, store.NewErrInvalidInput("screen_clip", "signature", nil)
		}
		now, err := screenClipStatementTime(ctx, tx)
		if err != nil {
			return nil, err
		}
		if err = clip.Complete(input.Signature, now); err != nil {
			return nil, store.NewErrInvalidInput("screen_clip", "complete", nil).Wrap(err)
		}
		return updateScreenClip(ctx, tx, clip)
	})
}

// ListAttemptScreenClips returns every clip requested for an Attempt.
func (s *SQLExamAttemptStore) ListAttemptScreenClips(
	ctx context.Context,
	attempt store.ExamAttemptClips,
) ([]store.ExamAttemptScreenClipView, error) {
	if !attempt.IsValid() {
		return nil, store.NewErrInvalidInput("screen_clip", "attempt", nil)
	}
	var rows []screenClipRow
	if err := s.GetMaster().Select(ctx, &rows, `SELECT `+prefixedScreenClipColumns+`
		FROM exam_attempt_screen_clips c
		JOIN exam_attempts a ON a.id = c.exam_attempt_id
		WHERE a.id = ? AND a.exam_id = ? AND a.exam_sitting_id = ?
		ORDER BY c.requested_at DESC, c.id DESC
		LIMIT ?`,
		attempt.AttemptID.String(), attempt.ExamID.String(), attempt.SittingID.String(),
		model.ScreenClipManagerRequestLimit+model.ScreenClipIncidentRequestLimit); err != nil {
		return nil, fmt.Errorf("list Attempt screen clips: %w", err)
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
}

// GetAttemptScreenClip returns one clip of an Attempt.
func (s *SQLExamAttemptStore) GetAttemptScreenClip(
	ctx context.Context,
	attempt store.ExamAttemptClips,
	clipID model.ScreenClipID,
) (*store.ExamAttemptScreenClipView, error) {
	if !attempt.IsValid() || !clipID.IsValid() {
		return nil, store.NewErrInvalidInput("screen_clip", "id", nil)
	}
	var row screenClipRow
	err := s.GetMaster().Get(ctx, &row, `SELECT `+prefixedScreenClipColumns+`
		FROM exam_attempt_screen_clips c
		JOIN exam_attempts a ON a.id = c.exam_attempt_id
		WHERE c.id = ? AND a.id = ? AND a.exam_id = ? AND a.exam_sitting_id = ?`,
		clipID.String(), attempt.AttemptID.String(), attempt.ExamID.String(), attempt.SittingID.String())
	if err != nil {
		return nil, translateError("screen_clip", clipID.String(), err)
	}
	clip, err := row.model()
	if err != nil {
		return nil, err
	}
	view := store.NewExamAttemptScreenClipView(clip)
	return &view, nil
}
