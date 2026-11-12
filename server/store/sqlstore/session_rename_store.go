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
	"strings"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Rename sets the device name of the User's live Web or CLI Session and
// completes the audit. PostgreSQL time decides whether the Session is live.
func (s SQLSessionStore) Rename(ctx context.Context, input *store.SessionRename) (*model.Session, error) {
	if input == nil ||
		!input.SessionID.IsValid() ||
		!input.UserID.IsValid() ||
		!input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() {
		return nil, store.NewErrInvalidInput("session", "rename", nil)
	}
	if name, err := model.NormalizeSessionDeviceName(input.DeviceName); err != nil || name != input.DeviceName {
		return nil, store.NewErrInvalidInput("session", "device_name", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "rename session", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*model.Session, error) {
		var row sessionRow
		if err := tx.Get(ctx, &row, `SELECT `+strings.Join(sessionSliceColumns(), ",")+`
		  FROM sessions
		 WHERE sessions.id = ? AND sessions.user_id = ?
		   AND sessions.archived_at IS NULL AND sessions.revoked_at IS NULL
		   AND sessions.idle_expires_at > statement_timestamp() AND sessions.expires_at > statement_timestamp()
		 FOR UPDATE`, input.SessionID.String(), input.UserID.String()); err != nil {
			return nil, translateError("session", input.SessionID.String(), err)
		}

		session, err := row.model()
		if err != nil {
			return nil, err
		}
		if session.ClientType == model.SessionClientDesktop {
			return nil, store.NewErrConflict("session", "desktop_named_by_registration", nil)
		}

		at := model.TimeUTC(input.AuditAt)
		if _, err = tx.Exec(
			ctx,
			`UPDATE sessions SET updated_at = GREATEST(updated_at, ?), device_name = ? WHERE id = ?`,
			at,
			input.DeviceName,
			input.SessionID.String(),
		); err != nil {
			return nil, fmt.Errorf("rename session: %w", err)
		}
		session.UpdatedAt = latestTime(session.UpdatedAt, at)
		session.DeviceName = input.DeviceName

		encoded, err := model.EncodeAuditData(session.Auditable())
		if err != nil {
			return nil, err
		}
		if _, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", encoded, input.AuditAt); err != nil {
			return nil, fmt.Errorf("complete session rename audit: %w", err)
		}

		return session, nil
	})
}
