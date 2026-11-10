// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type RevokeDesktopRegistrationCommand struct {
	RegistrationID model.DesktopRegistrationID
}

// requireDesktopRegistration refuses a Service whose Desktop Registration
// collaborators are incomplete. New calls it once, with the account services.
func (s *Service) requireDesktopRegistration() error {
	if s.desktopRegistrations == nil || s.auditor == nil || s.effects == nil || s.recentAuthenticationTTL <= 0 || s.now == nil {
		return errors.New("Desktop Registration dependencies are invalid")
	}
	return nil
}

// ListDesktopRegistrations lists the calling User's Desktop Registrations.
func (s *Service) ListDesktopRegistrations(
	ctx context.Context,
	invocation invocation.Invocation,
) ([]*model.DesktopRegistration, error) {
	principal := invocation.Principal()
	if err := requireInteractiveSession(principal, false, s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}
	registrations, err := s.desktopRegistrations.ListByUser(ctx, principal.UserID)
	if err != nil {
		return nil, apperror.New("desktop_registration.unavailable").Wrap(err)
	}
	return registrations, nil
}

// RevokeDesktopRegistration revokes one of the calling User's Desktop
// Registrations and the Sessions it holds, from a recent interactive Session.
func (s *Service) RevokeDesktopRegistration(
	ctx context.Context,
	invocation invocation.Invocation,
	command RevokeDesktopRegistrationCommand,
) error {
	principal := invocation.Principal()
	at := model.TimeUTC(s.now())
	if err := requireInteractiveSession(principal, true, at, s.recentAuthenticationTTL); err != nil {
		return err
	}

	if !command.RegistrationID.IsValid() {
		return apperror.New("request.invalid").WithField("field", "desktop_registration_id")
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     model.ActionDesktopRegistrationRevoke,
		Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
		Operation:  "revoke",
		Value:      map[string]any{"registration_id": command.RegistrationID.String()},
	})
	if err != nil {
		return err
	}

	result, err := s.desktopRegistrations.Revoke(ctx, &store.DesktopRegistrationRevocation{
		RegistrationID: command.RegistrationID,
		UserID:         principal.UserID,
		RevokedAt:      at,
		AuditEventID:   auditID,
		AuditAt:        at,
	})
	if err != nil {
		failure := desktopRegistrationStoreErrors.Translate(err)
		if auditErr := s.auditor.Fail(ctx, auditID, apperror.CodeOf(failure, "desktop_registration.unavailable")); auditErr != nil {
			return apperror.New("audit.unavailable").Wrap(auditErr)
		}

		return failure
	}

	if !result.AlreadyRevoked && len(result.Sessions) > 0 {
		s.effects.SessionsRevoked(
			ctx, principal.UserID, sessionIDs(result.Sessions), result.TokenHashes,
		)
	}

	return nil
}

var desktopRegistrationStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Unavailable: "desktop_registration.unavailable",
}
