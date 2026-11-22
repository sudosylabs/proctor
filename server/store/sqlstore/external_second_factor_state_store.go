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

const secondFactorEntity = "external_second_factor_state"

const secondFactorStateColumns = `id, created_at, updated_at, purpose, user_id, external_identity_id,
    provider, authentication_method, browser_authentication_transaction_id, binding_hash,
    authentication_generation, primary_authenticated_at, return_to, client_type, device_id,
    device_name, audit_event_id, external_login_state_id, password_credential_id,
    password_credential_revision, expires_at, consumed_at`

type externalSecondFactorStateRow struct {
	ID                                 string         `db:"id"`
	CreatedAt                          time.Time      `db:"created_at"`
	UpdatedAt                          time.Time      `db:"updated_at"`
	Purpose                            string         `db:"purpose"`
	UserID                             string         `db:"user_id"`
	ExternalIdentityID                 sql.NullString `db:"external_identity_id"`
	Provider                           string         `db:"provider"`
	AuthenticationMethod               string         `db:"authentication_method"`
	BrowserAuthenticationTransactionID sql.NullString `db:"browser_authentication_transaction_id"`
	BindingHash                        string         `db:"binding_hash"`
	AuthenticationGeneration           int64          `db:"authentication_generation"`
	PrimaryAuthenticatedAt             time.Time      `db:"primary_authenticated_at"`
	ReturnTo                           string         `db:"return_to"`
	ClientType                         string         `db:"client_type"`
	DeviceID                           string         `db:"device_id"`
	DeviceName                         string         `db:"device_name"`
	AuditEventID                       sql.NullString `db:"audit_event_id"`
	ExternalLoginStateID               sql.NullString `db:"external_login_state_id"`
	PasswordCredentialID               sql.NullString `db:"password_credential_id"`
	PasswordCredentialRevision         sql.NullInt64  `db:"password_credential_revision"`
	ExpiresAt                          time.Time      `db:"expires_at"`
	ConsumedAt                         sql.NullTime   `db:"consumed_at"`
}

func (r externalSecondFactorStateRow) model() (*model.ExternalSecondFactorState, error) {
	state := &model.ExternalSecondFactorState{
		ID:                                 model.ExternalSecondFactorStateID(r.ID),
		CreatedAt:                          model.TimeUTC(r.CreatedAt),
		UpdatedAt:                          model.TimeUTC(r.UpdatedAt),
		Purpose:                            model.ExternalAuthenticationPurpose(r.Purpose),
		UserID:                             model.UserID(r.UserID),
		ExternalIdentityID:                 model.ExternalIdentityID(r.ExternalIdentityID.String),
		Provider:                           r.Provider,
		AuthenticationMethod:               r.AuthenticationMethod,
		BrowserAuthenticationTransactionID: model.BrowserAuthenticationTransactionID(r.BrowserAuthenticationTransactionID.String),
		BindingHash:                        r.BindingHash,
		AuthenticationGeneration:           r.AuthenticationGeneration,
		PrimaryAuthenticatedAt:             model.TimeUTC(r.PrimaryAuthenticatedAt),
		ReturnTo:                           r.ReturnTo,
		ClientType:                         model.SessionClientType(r.ClientType),
		DeviceID:                           r.DeviceID,
		DeviceName:                         r.DeviceName,
		AuditEventID:                       model.AuditEventID(r.AuditEventID.String),
		ExternalLoginStateID:               model.ExternalLoginStateID(r.ExternalLoginStateID.String),
		PasswordCredentialID:               model.PasswordCredentialID(r.PasswordCredentialID.String),
		PasswordCredentialRevision:         r.PasswordCredentialRevision.Int64,
		ExpiresAt:                          model.TimeUTC(r.ExpiresAt),
		ConsumedAt:                         OptionalTimeFromNullTime(r.ConsumedAt),
	}
	return state, state.Validate()
}

func (s SQLExternalLoginStateStore) CreateExternalSecondFactorState(
	ctx context.Context, input *store.ExternalSecondFactorStateCreation,
) (*model.ExternalSecondFactorState, error) {
	if input == nil || input.State == nil || input.State.ID != "" || input.State.ConsumedAt.Valid ||
		input.Lifetime < externalLoginStateLifetimeMinimum || input.Lifetime > externalLoginStateLifetimeMaximum {
		return nil, store.NewErrInvalidInput(secondFactorEntity, "creation", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "create second factor state", func(
		ctx context.Context, tx *sqlxTxWrapper,
	) (*model.ExternalSecondFactorState, error) {
		state := *input.State
		at, err := jobDatabaseNow(ctx, tx)
		if err != nil {
			return nil, err
		}
		state.ExpiresAt = at.Add(input.Lifetime)
		state.PrepareCreate(model.NewExternalSecondFactorStateID(), at)
		if err := state.Validate(); err != nil {
			return nil, err
		}
		if err := requireSecondFactorPrimaryState(ctx, tx, &state); err != nil {
			return nil, err
		}

		_, err = tx.Exec(ctx, `INSERT INTO external_second_factor_states (`+secondFactorStateColumns+`)
            VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?,
                NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, 0), ?, NULL)`,
			state.ID.String(), state.CreatedAt, state.UpdatedAt, string(state.Purpose), state.UserID.String(),
			state.ExternalIdentityID.String(), state.Provider, state.AuthenticationMethod,
			state.BrowserAuthenticationTransactionID.String(), state.BindingHash, state.AuthenticationGeneration,
			state.PrimaryAuthenticatedAt, state.ReturnTo, string(state.ClientType), state.DeviceID, state.DeviceName,
			state.AuditEventID.String(), state.ExternalLoginStateID.String(), state.PasswordCredentialID.String(),
			state.PasswordCredentialRevision, state.ExpiresAt)
		if err != nil {
			return nil, translateError(secondFactorEntity, state.ID.String(), err)
		}
		return &state, nil
	})
}

func requireSecondFactorPrimaryState(ctx context.Context, tx *sqlxTxWrapper, state *model.ExternalSecondFactorState) error {
	if state.Purpose == model.ExternalAuthenticationPurposeLogin {
		var id string
		err := tx.Get(ctx, &id, `SELECT id FROM external_login_states
            WHERE id = ? AND provider = ? AND purpose = 'login' AND consumed_at IS NOT NULL FOR KEY SHARE`,
			state.ExternalLoginStateID.String(), state.Provider)
		return translateError("external_login_state", state.ExternalLoginStateID.String(), err)
	}

	var expiry time.Time
	err := tx.Get(ctx, &expiry, `SELECT expires_at FROM browser_authentication_transactions
        WHERE id = ? AND purpose = ? AND state = 'bound' AND expires_at > clock_timestamp()
            AND (purpose <> 'desktop_reauthentication' OR (browser_proof_hash = ? AND user_id = ?))
        FOR SHARE`, state.BrowserAuthenticationTransactionID.String(), string(state.Purpose), state.BindingHash, state.UserID.String())
	if err != nil {
		return translateError("browser_authentication_transaction", state.BrowserAuthenticationTransactionID.String(), err)
	}
	state.ExpiresAt = model.TimeUTC(state.ExpiresAt).Truncate(time.Microsecond)
	if expiry.Before(state.ExpiresAt) {
		state.ExpiresAt = model.TimeUTC(expiry)
	}
	return nil
}

func (s SQLExternalLoginStateStore) GetExternalSecondFactorStateByBinding(
	ctx context.Context, bindingHash string, now time.Time,
) (*model.ExternalSecondFactorState, error) {
	if !model.IsValidTokenHash(bindingHash) {
		return nil, store.NewErrInvalidInput(secondFactorEntity, "binding", nil)
	}
	var row externalSecondFactorStateRow
	err := s.GetMaster().Get(ctx, &row, `SELECT `+secondFactorStateColumns+` FROM external_second_factor_states
        WHERE binding_hash = ? AND consumed_at IS NULL AND expires_at > ? AND expires_at > clock_timestamp()`,
		bindingHash, model.TimeUTC(now))
	if err != nil {
		return nil, translateError(secondFactorEntity, "", err)
	}
	return row.model()
}

func (s SQLExternalLoginStateStore) HasLiveExternalSecondFactorState(
	ctx context.Context, id model.BrowserAuthenticationTransactionID,
) (bool, error) {
	var found bool
	err := s.GetMaster().Get(ctx, &found, `SELECT EXISTS(SELECT 1 FROM external_second_factor_states
        WHERE browser_authentication_transaction_id = ? AND consumed_at IS NULL AND expires_at > clock_timestamp())`, id.String())
	return found, err
}

// ExpireExternalSecondFactorState closes the open audit and retains the row
// until maintenance, allowing a browser that still presents the binding to
// receive the expiry outcome.
func (s SQLExternalLoginStateStore) ExpireExternalSecondFactorState(ctx context.Context, bindingHash string) (bool, error) {
	var expired bool
	err := s.GetMaster().Get(ctx, &expired, `WITH expired AS (
        SELECT audit_event_id FROM external_second_factor_states
        WHERE binding_hash = ? AND consumed_at IS NULL AND expires_at <= clock_timestamp()
        FOR UPDATE
    ), audited AS (
        UPDATE audit_events AS audit
        SET updated_at = GREATEST(audit.updated_at, clock_timestamp()), status = 'fail',
            error_code = 'authentication.external.second_factor_expired'
        FROM expired WHERE audit.id = expired.audit_event_id AND audit.status = 'attempt'
    )
    SELECT EXISTS(SELECT 1 FROM expired)`, bindingHash)
	return expired, err
}

func (s SQLExternalLoginStateStore) ConsumeExternalSecondFactorState(
	ctx context.Context, input *store.ExternalSecondFactorStateConsumption,
) (*model.ExternalSecondFactorState, error) {
	if input == nil || !input.ID.IsValid() || !model.IsValidTokenHash(input.BindingHash) ||
		!input.CredentialID.IsValid() || !input.AuditEventID.IsValid() || input.VerifiedAt.IsZero() ||
		((input.TimeStep <= 0) == (input.RecoveryCodeHash == "")) ||
		(input.RecoveryCodeHash != "" && !model.IsValidTokenHash(input.RecoveryCodeHash)) {
		return nil, store.NewErrInvalidInput(secondFactorEntity, "proof", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "consume second factor state", func(
		ctx context.Context, tx *sqlxTxWrapper,
	) (*model.ExternalSecondFactorState, error) {
		// Take the same per-user fences as MFA reset before locking the pause.
		var userID model.UserID
		if err := tx.Get(ctx, &userID, `SELECT user_id FROM external_second_factor_states
            WHERE id = ? AND binding_hash = ? AND purpose = ? AND consumed_at IS NULL`,
			input.ID.String(), input.BindingHash, string(input.Purpose)); err != nil {
			return nil, translateError(secondFactorEntity, "", err)
		}
		if err := lockMFASessionUser(ctx, tx, userID.String()); err != nil {
			return nil, err
		}
		var row externalSecondFactorStateRow
		if err := tx.Get(ctx, &row, `SELECT `+secondFactorStateColumns+` FROM external_second_factor_states
            WHERE id = ? AND binding_hash = ? AND purpose = ? AND consumed_at IS NULL FOR UPDATE`,
			input.ID.String(), input.BindingHash, string(input.Purpose)); err != nil {
			return nil, translateError(secondFactorEntity, "", err)
		}
		state, err := row.model()
		if err != nil {
			return nil, err
		}
		at, err := jobDatabaseNow(ctx, tx)
		if err != nil {
			return nil, err
		}
		if !state.ExpiresAt.After(at) {
			return nil, store.NewErrConflict(secondFactorEntity, "expired", nil)
		}
		if err := consumeExternalSecondFactorProof(ctx, tx, state, input, at); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE external_second_factor_states SET consumed_at = ?, updated_at = GREATEST(updated_at, ?)
            WHERE id = ?`, at, at, state.ID.String()); err != nil {
			return nil, fmt.Errorf("mark second factor state consumed: %w", err)
		}
		if _, err := completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", nil, at); err != nil {
			return nil, err
		}
		state.ConsumedAt = model.OptionalTimeFrom(at)
		state.UpdatedAt = latestTime(state.UpdatedAt, at)
		return state, nil
	})
}

func consumeExternalSecondFactorProof(
	ctx context.Context, tx *sqlxTxWrapper, state *model.ExternalSecondFactorState,
	input *store.ExternalSecondFactorStateConsumption, at time.Time,
) error {
	if err := requireActiveUser(ctx, tx, state.UserID.String(), false); err != nil {
		return err
	}
	recovery, err := getMFARecoveryState(ctx, tx, state.UserID)
	if err != nil {
		return err
	}
	if state.AuthenticationGeneration != recovery.Generation || recovery.ReenrollmentRequired {
		return store.NewErrConflict(secondFactorEntity, "generation_changed", nil)
	}
	if state.AuthenticationMethod == "password" {
		proof := store.PasswordCredentialProof{ID: state.PasswordCredentialID, Revision: state.PasswordCredentialRevision}
		if err := requireCurrentPasswordProof(ctx, tx, state.UserID, proof); err != nil {
			if errors.Is(err, store.ErrPasswordCredentialChanged) {
				return store.NewErrConflict(secondFactorEntity, "password_changed", nil)
			}
			return err
		}
	}
	if input.VerifiedAt.After(at.Add(time.Minute)) || input.VerifiedAt.Before(at.Add(-time.Minute)) {
		return store.NewErrConflict("mfa_credential", "verification_expired", nil)
	}
	return consumeExactMFAFactor(ctx, tx, state.UserID, input.CredentialID, input.TimeStep, input.RecoveryCodeHash, at)
}

func sweepExternalSecondFactorStates(ctx context.Context, tx *sqlxTxWrapper, limit int) (int, int, error) {
	var result struct {
		Purged       int `db:"purged"`
		Terminalized int `db:"terminalized"`
	}
	err := tx.Get(ctx, &result, `WITH candidates AS (
        SELECT id FROM external_second_factor_states
        WHERE expires_at <= clock_timestamp() OR consumed_at IS NOT NULL
        ORDER BY expires_at, id FOR UPDATE SKIP LOCKED LIMIT ?
    ), removed AS (
        DELETE FROM external_second_factor_states AS state USING candidates
        WHERE state.id = candidates.id RETURNING state.audit_event_id, state.consumed_at
    ), audited AS (
        UPDATE audit_events AS audit
        SET updated_at = GREATEST(audit.updated_at, clock_timestamp()), status = 'fail',
            error_code = 'authentication.external.second_factor_expired'
        FROM removed WHERE audit.id = removed.audit_event_id AND removed.consumed_at IS NULL
            AND audit.status = 'attempt' RETURNING audit.id
    )
    SELECT (SELECT count(*) FROM removed) AS purged, (SELECT count(*) FROM audited) AS terminalized`, limit)
	return result.Purged, result.Terminalized, err
}
