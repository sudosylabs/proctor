// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authentication.go,
// server/channels/app/login.go, and server/channels/app/session.go. Proctor
// keeps the single application authentication flow, generic login failures,
// server-side revocable sessions, activity debouncing, and boundary-safe
// errors while using split hashed access/refresh credentials and rotation.

package identity

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// LoginCommand is the local-password login use-case input.
type LoginCommand struct {
	LoginID    string
	Password   string
	ClientType model.SessionClientType
	DeviceID   string
	DeviceName string
	MFACode    string
	Source     string
}

// LoginResult is the transport-neutral successful login outcome.
type LoginResult struct {
	User    *model.User
	Session *model.Session
	Tokens  *model.AuthenticationTokens
}

// Login proves a local password, and a second factor when one is required, and
// issues a Session. Every admitted attempt is audited; Desktop sign-in uses
// Desktop authorization instead.
func (s *Service) Login(
	ctx context.Context,
	inv invocation.Invocation,
	command LoginCommand,
) (_ *LoginResult, resultErr error) {
	defer func() { recordOperational(s.operational, "authentication", "login", resultErr) }()
	if command.ClientType == model.SessionClientDesktop || !command.ClientType.IsValid() {
		return nil, apperror.New("authentication.client_type.invalid").WithField("field", "client_type")
	}

	proof, subject, err := s.proveLocal(ctx, command)
	if err != nil {
		if !subject.admitted {
			return nil, err
		}

		if auditErr := s.recordLocalLoginFailure(
			ctx,
			inv,
			subject.userID,
			command.ClientType,
			appErrorCode(err),
		); auditErr != nil {
			return nil, auditErr
		}

		return nil, err
	}

	auditID, err := s.beginLocalLogin(ctx, inv, proof.User.ID, command.ClientType)
	if err != nil {
		return nil, err
	}

	savedSession, tokens, sessionErr := s.createSession(
		ctx,
		sessionIssuance{
			User:                     proof.User,
			ClientType:               command.ClientType,
			PasswordProof:            proof.PasswordProof,
			AuthenticationGeneration: proof.AuthenticationGeneration,
			MFARecoveryRequired:      proof.MFARecoveryRequired,
			DeviceID:                 command.DeviceID,
			DeviceName:               command.DeviceName,
			AuthenticationMethod:     "password",
			AuthenticationStrength:   proof.AuthenticationStrength,
			AuthenticatedAt:          proof.AuthenticatedAt,
			MFACompletedAt:           proof.MFACompletedAt,
			AuditEventID:             auditID,
		},
	)
	if sessionErr != nil {
		if auditErr := s.completeLocalLogin(
			ctx,
			auditID,
			model.AuditStatusFail,
			appErrorCode(sessionErr),
			nil,
		); auditErr != nil {
			return nil, auditErr
		}

		return nil, sessionErr
	}

	s.resetLocalAuthenticationAttempts(ctx, proof)
	return &LoginResult{User: proof.User, Session: savedSession, Tokens: tokens}, nil
}

// beginLocalLogin, completeLocalLogin and recordLocalLoginFailure durably
// record every admitted local-password login outcome. Attempts rejected
// before rate-limit admission are not audited so an unauthenticated flood
// cannot grow the audit table without bound.
func (s *Service) beginLocalLogin(
	ctx context.Context,
	inv invocation.Invocation,
	userID model.UserID,
	clientType model.SessionClientType,
) (model.AuditEventID, error) {
	if s.securityAudit == nil {
		return "", access.AuditUnavailable(errors.New("local login audit service is unavailable"))
	}

	event, err := s.securityAudit.BeginLocalLogin(ctx, userID, clientType, inv.RequestMetadata())
	if err != nil {
		return "", err
	}

	return event.ID, nil
}

func (s *Service) completeLocalLogin(
	ctx context.Context,
	auditID model.AuditEventID,
	status model.AuditStatus,
	errorCode string,
	result any,
) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("local login audit service is unavailable"))
	}

	_, err := s.securityAudit.CompleteCriticalAction(ctx, auditID, status, errorCode, result)
	return err
}

func (s *Service) recordLocalLoginFailure(
	ctx context.Context,
	inv invocation.Invocation,
	target model.UserID,
	clientType model.SessionClientType,
	errorCode string,
) error {
	if s.securityAudit == nil {
		return access.AuditUnavailable(errors.New("local login audit service is unavailable"))
	}
	return s.securityAudit.RecordLocalLoginFailure(ctx, target, clientType, inv.RequestMetadata(), errorCode)
}
