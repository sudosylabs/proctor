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

// requireDesktopRegistration refuses a Service whose Desktop Registration
// collaborators are incomplete. New calls it once, with the account services.
func (s *Service) requireDesktopRegistration() error {
	if s.desktopRegistrations == nil || s.auditor == nil || s.publisher == nil || s.recentAuthenticationTTL <= 0 || s.now == nil {
		return errors.New("Desktop Registration dependencies are invalid")
	}
	return nil
}

// ListDesktopRegistrations lists the calling User's Desktop Registrations.
func (s *Service) ListDesktopRegistrations(
	ctx context.Context,
	inv invocation.Invocation,
) ([]*model.DesktopRegistration, error) {
	principal := inv.Principal()
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
	inv invocation.Invocation,
	id model.DesktopRegistrationID,
) error {
	principal := inv.Principal()
	at := model.TimeUTC(s.now())
	if err := requireInteractiveSession(principal, true, at, s.recentAuthenticationTTL); err != nil {
		return err
	}

	if !id.IsValid() {
		return apperror.New("request.invalid").WithField("field", "desktop_registration_id")
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionDesktopRegistrationRevoke,
		Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
		Operation:  "revoke",
		Value:      map[string]any{"registration_id": id.String()},
	})
	if err != nil {
		return err
	}

	result, err := s.desktopRegistrations.Revoke(ctx, &store.DesktopRegistrationRevocation{
		RegistrationID: id,
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
		s.publisher.SessionsRevoked(
			ctx, principal.UserID, sessionIDs(result.Sessions), result.TokenHashes,
		)
	}

	return nil
}

type RenameDesktopRegistrationCommand struct {
	RegistrationID model.DesktopRegistrationID
	DisplayName    string
}

// RenameDesktopRegistration gives one of the calling User's active Desktop
// Registrations the name they chose. The name labels the device in the
// User's own lists and survives later Desktop sign-ins with the same key. A
// rename changes no key, Session, or credential, so any interactive Session
// may make it; it needs no recent authentication.
func (s *Service) RenameDesktopRegistration(
	ctx context.Context,
	inv invocation.Invocation,
	command RenameDesktopRegistrationCommand,
) (*model.DesktopRegistration, error) {
	principal := inv.Principal()
	at := model.TimeUTC(s.now())
	if err := requireInteractiveSession(principal, false, at, s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	if !command.RegistrationID.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "desktop_registration_id")
	}
	displayName, err := model.NormalizeDesktopRegistrationDisplayName(command.DisplayName)
	if err != nil {
		return nil, apperror.Invalid("desktop_registration.invalid", "display_name").Wrap(err)
	}

	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionDesktopRegistrationRename,
			Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation:  model.AuditOperationRenameDesktopRegistration,
			Value:      map[string]any{"registration_id": command.RegistrationID.String()},
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.DesktopRegistration, error) {
			return s.desktopRegistrations.Rename(ctx, &store.DesktopRegistrationRename{
				RegistrationID: command.RegistrationID,
				UserID:         principal.UserID,
				DisplayName:    displayName,
				AuditEventID:   reference.ID,
				AuditAt:        reference.MutationAt,
			})
		},
		desktopRegistrationRenameStoreErrors.Translate,
	)
}

var desktopRegistrationStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Unavailable: "desktop_registration.unavailable",
}

// A revoked registration stays in the User's list as security history, so
// renaming it is refused rather than concealed.
var desktopRegistrationRenameStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Conflict:    "desktop_registration.revoked",
	Unavailable: "desktop_registration.unavailable",
}
