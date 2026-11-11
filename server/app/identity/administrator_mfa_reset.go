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
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type AdministratorMFAResetResult struct {
	RecoveryGeneration   int64
	ReenrollmentRequired bool
}

// ResetAdministratorMFA is host authority, never a User impersonation or
// network credential; it deliberately takes no password or factor input. It
// is absent from every network application capability.
// The module root borrows it only while its Server is inert, and the Store
// serializes it against all serving nodes and unreconciled host recovery.
func (s *Service) ResetAdministratorMFA(
	ctx context.Context,
	system invocation.System,
	institutionID model.InstitutionID,
	userID model.UserID,
) (*AdministratorMFAResetResult, error) {
	if !system.Valid() {
		return nil, apperror.New("administrator_recovery.invalid")
	}

	institutionID, institutionErr := model.ParseInstitutionID(strings.TrimSpace(institutionID.String()))
	userID, userErr := model.ParseUserID(strings.TrimSpace(userID.String()))
	if institutionErr != nil || userErr != nil {
		return nil, apperror.New("administrator_recovery.invalid")
	}
	if err := s.requireMFAEnabled(); err != nil {
		return nil, err
	}
	if s.capabilities == nil {
		return nil, apperror.New("administrator_recovery.unavailable").Wrap(errors.New("administrator recovery capabilities are unavailable"))
	}

	result, err := s.installations.ResetAdministratorMFA(ctx, &store.AdministratorMFAReset{
		InstitutionID: institutionID,
		UserID:        userID,
		MFAEnabled:    s.mfa.deps.Settings.Enabled,
		Capabilities:  AccessDeploymentCapabilities(s.capabilities.Snapshot()),
	})
	if err != nil {
		return nil, apperror.New("administrator_recovery.unavailable").Wrap(err)
	}

	if result == nil ||
		!result.RecordID.IsValid() ||
		result.Recovery == nil ||
		result.Recovery.Validate() != nil ||
		result.Recovery.UserID != userID ||
		!result.Recovery.ReenrollmentRequired {
		return nil, apperror.New("administrator_recovery.unavailable").Wrap(errors.New("persistence returned no valid MFA recovery evidence"))
	}

	return &AdministratorMFAResetResult{RecoveryGeneration: result.Recovery.Generation, ReenrollmentRequired: true}, nil
}
