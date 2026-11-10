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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListUserSessionsQuery pages one User's Sessions newest first. Limit is
// between 1 and 200; a continuation names the last Session of the previous
// page by its creation instant and ID.
type ListUserSessionsQuery struct {
	UserID          model.UserID
	IncludeRevoked  bool
	Limit           int
	BeforeCreatedAt time.Time
	BeforeID        model.SessionID
}

type RevokeUserSessionCommand struct {
	UserID    model.UserID
	SessionID model.SessionID
}

type RevokeUserSessionsCommand struct {
	UserID         model.UserID
	IdempotencyKey string
	// Batch is the administration batch context; a transport leaves it zero.
	Batch batchitem.Item
}

// ListUserSessions pages one User's Sessions for an administrator.
func (s *Service) ListUserSessions(
	ctx context.Context,
	invocation invocation.Invocation,
	query ListUserSessionsQuery,
) (paging.Page[*model.Session], error) {
	userID := query.UserID
	if !userID.IsValid() {
		return paging.Page[*model.Session]{}, apperror.New("request.invalid").WithField("field", "user_id")
	}
	if query.Limit < 1 || query.Limit > paging.MaximumLimit {
		return paging.Page[*model.Session]{}, apperror.Invalid("request.invalid", "limit")
	}
	if query.BeforeCreatedAt.IsZero() != query.BeforeID.IsZero() ||
		(!query.BeforeID.IsZero() && !query.BeforeID.IsValid()) {
		return paging.Page[*model.Session]{}, apperror.Invalid("request.invalid", "cursor")
	}
	if err := s.authorizer.Authorize(ctx, invocation, model.ActionSessionView, model.Resource{Type: model.ResourceUser, ID: userID.String()}); err != nil {
		return paging.Page[*model.Session]{}, err
	}
	page := &store.SessionPageQuery{
		UserID: userID, Limit: query.Limit + 1,
		BeforeCreatedAt: query.BeforeCreatedAt, BeforeID: query.BeforeID,
	}
	if !query.IncludeRevoked {
		page.ActiveAt = model.TimeUTC(s.now())
	}
	sessions, err := s.sessions.ListByUser(ctx, page)
	if err != nil {
		return paging.Page[*model.Session]{}, sessionAdministrationStoreErrors.Translate(err)
	}
	return paging.New(sessions, query.Limit), nil
}

// RevokeUserSession revokes one of a User's Sessions for an administrator and
// notifies the User.
func (s *Service) RevokeUserSession(
	ctx context.Context,
	invocation invocation.Invocation,
	command RevokeUserSessionCommand,
) error {
	userID := command.UserID
	sessionID := command.SessionID
	if !userID.IsValid() {
		return apperror.New("request.invalid").WithField("field", "user_id")
	}
	if !sessionID.IsValid() {
		return apperror.New("request.invalid").WithField("field", "session_id")
	}
	if err := s.authorizer.Authorize(ctx, invocation, model.ActionSessionManage, model.Resource{Type: model.ResourceUser, ID: userID.String()}); err != nil {
		return err
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil || session.UserID != userID {
		if err == nil {
			err = store.NewErrNotFound("session", sessionID.String())
		}
		return sessionAdministrationStoreErrors.Translate(err)
	}
	user, err := s.users.Get(ctx, userID)
	if err != nil {
		return sessionAdministrationStoreErrors.Translate(err)
	}
	now := model.TimeUTC(s.now())
	prepared, err := s.prepareRevocationNotice(user, now)
	if err != nil {
		return sessionAdministrationStoreErrors.Translate(err)
	}
	return s.revokeSession(ctx,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  "revoke_session",
			Value:      map[string]any{"user_id": userID, "session_id": sessionID},
			Prior:      session.Auditable(),
		},
		&store.SessionRevocation{
			SessionID: sessionID, UserID: userID,
			Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, DeliveryJob: prepared.Job,
			Reason: model.SessionRevocationAdministratorSession,
		},
		sessionAdministrationStoreErrors.Translate, func() time.Time { return now },
	)
}

// RevokeUserSessions revokes every Session of a User for an administrator, or
// for an administration batch row, and notifies the User.
func (s *Service) RevokeUserSessions(
	ctx context.Context,
	invocation invocation.Invocation,
	command RevokeUserSessionsCommand,
) error {
	userID := command.UserID
	if !userID.IsValid() {
		return apperror.New("request.invalid").WithField("field", "user_id")
	}
	if err := s.authorizer.Authorize(ctx, invocation, model.ActionSessionManage, model.Resource{Type: model.ResourceUser, ID: userID.String()}); err != nil {
		return err
	}
	user, err := s.users.Get(ctx, userID)
	if err != nil {
		return sessionAdministrationStoreErrors.Translate(err)
	}
	now := model.TimeUTC(s.now())
	idempotency, err := idempotency.Prepare(invocation, "user_sessions.revoke", command.IdempotencyKey, struct {
		UserID string `json:"user_id"`
	}{userID.String()})
	if err != nil {
		return err
	}
	batch := command.Batch.Fields()
	batchitem.BindOnboardingImport(idempotency, batch.OnboardingImportID, batch.OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, batch.Authorization)
	batchitem.BindBatch(idempotency, batch.Metadata)
	prepared := &store.PreparedMail{}
	if !batch.RetainedOutcome {
		recipient := *user
		recipient.DisabledAt = model.OptionalTime{}
		prepared, err = s.prepareRevocationNotice(&recipient, now)
		if err != nil {
			return sessionAdministrationStoreErrors.Translate(err)
		}
	}
	return s.revokeAllSessions(ctx,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: userID.String()},
			Operation:  "revoke_sessions",
			Value:      map[string]any{"user_id": userID},
		},
		&store.UserSessionsRevocation{
			UserID:     userID,
			Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, DeliveryJob: prepared.Job,
			Reason: model.SessionRevocationAdministratorAllSessions, Command: idempotency,
		},
		sessionAdministrationStoreErrors.Translate, batch.Replayed, func() time.Time { return now },
	)
}

func (s *Service) prepareRevocationNotice(user *model.User, at time.Time) (*store.PreparedMail, error) {
	return s.mail.PrepareSessionsRevokedByAdministrator(mail.NoticePreparation{Recipient: user, At: at})
}

var sessionAdministrationStoreErrors = apperror.StoreTable{
	NotFound:    "session.not_found",
	Invalid:     "request.invalid",
	Unavailable: "administration.unavailable",
	Fields:      map[string]string{"resource": "session"},
}
