// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// revokeSession, revokeAllSessions and revokeAllSessionsCounted commit an
// already-authorized revocation and publish only its new durable effects.
// Callers retain ownership checks, audit intent, public errors,
// administrative mail and command preparation, and pass the clock the
// mutation audit is stamped with. Account and password transitions keep their
// separate atomic Store operations.
func (s *Service) revokeSession(
	ctx context.Context,
	attempt access.Mutation,
	input *store.SessionRevocation,
	mapError func(error) error,
	now func() time.Time,
) error {
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		now,
		func(ctx context.Context, reference access.MutationReference) (*store.SessionRevocationResult, error) {
			input.RevokedAt = reference.MutationAt
			input.AuditEventID, input.AuditAt = reference.ID, reference.MutationAt
			return s.sessions.Revoke(ctx, input)
		},
		mapError,
	)
	if err != nil {
		return err
	}

	// The Store represents an already-absent logout with no affected Session;
	// other single revocations retain their owning use case's not-found error.
	if result != nil && result.Session != nil {
		s.publishSessionsRevoked(ctx, input.UserID.String(), []*model.Session{result.Session}, result.TokenHashes)
	}

	return nil
}

func (s *Service) revokeAllSessions(
	ctx context.Context,
	attempt access.Mutation,
	input *store.UserSessionsRevocation,
	mapError func(error) error,
	unchanged *bool,
	now func() time.Time,
) error {
	_, err := s.revokeAllSessionsCounted(ctx, attempt, input, mapError, unchanged, now)
	return err
}

// revokeAllSessionsCounted is revokeAllSessions that also reports how many
// Sessions this call newly revoked; a replayed or no-op revocation reports
// zero.
func (s *Service) revokeAllSessionsCounted(
	ctx context.Context,
	attempt access.Mutation,
	input *store.UserSessionsRevocation,
	mapError func(error) error,
	unchanged *bool,
	now func() time.Time,
) (int, error) {
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		now,
		func(ctx context.Context, reference access.MutationReference) (*store.UserSessionsRevocationResult, error) {
			input.RevokedAt = reference.MutationAt
			input.AuditEventID, input.AuditAt = reference.ID, reference.MutationAt
			value, err := s.sessions.RevokeAllForUser(ctx, input)
			// Batch callers retain the Store's replay/no-op observation even when
			// the mutation fails; an unavailable audit attempt leaves it untouched.
			if unchanged != nil {
				*unchanged = input.Replayed || input.NoOp
			}

			return value, err
		},
		mapError,
	)
	if err != nil {
		return 0, err
	}
	if result == nil || input.Replayed || input.NoOp {
		return 0, nil
	}

	s.publishSessionsRevoked(ctx, input.UserID.String(), result.Sessions, result.TokenHashes)
	return len(result.Sessions), nil
}

func (s *Service) publishSessionsRevoked(
	ctx context.Context,
	userID string,
	sessions []*model.Session,
	hashes []string,
) {
	if len(sessions) != 0 {
		s.effects.SessionsRevoked(ctx, model.UserID(userID), sessionIDs(sessions), hashes)
	}
}
