// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Mattermost's first-user administrator behavior was used as a product
// reference. Proctor replaces its process-local first-user check with an
// explicit, audited, PostgreSQL-serialized installation aggregate.

package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type BootstrapInstallationCommand struct {
	InstitutionName          string
	InstitutionDisplayName   string
	InstitutionDescription   string
	AdministratorUsername    string
	AdministratorEmail       string
	AdministratorDisplayName string
	AdministratorFirstName   string
	AdministratorLastName    string
	AdministratorLocale      string
	AdministratorTimezone    string
	Password                 string
	BootstrapSecret          string
	Source                   string
}

// AdministratorRecoveryCommand is accepted only by the host-level offline
// capability. Password is private input and is replaced with an encoded hash
// before the named Store aggregate is invoked.
type AdministratorRecoveryCommand struct {
	InstitutionID    model.InstitutionID
	UserID           model.UserID
	EnableLocalLogin bool
	Password         string
}

type AdministratorRecoveryResult struct {
	LocalLoginEnabled bool
	PasswordRotated   bool
}

// BootstrapProtectionPolicy is the immutable deployment-owned proof used by
// the public one-time bootstrap command. The service retains only its digest.
type BootstrapProtectionPolicy struct {
	Secret string
}

// RecoverAdministratorAccess is intentionally absent from every network
// application interface. The module root borrows this method only for the
// explicit offline host command.
func (s *Service) RecoverAdministratorAccess(
	ctx context.Context,
	system invocation.System,
	command AdministratorRecoveryCommand,
) (*AdministratorRecoveryResult, error) {
	if !system.Valid() {
		return nil, apperror.New("administrator_recovery.invalid")
	}

	institutionID, institutionErr := model.ParseInstitutionID(strings.TrimSpace(command.InstitutionID.String()))
	userID, userErr := model.ParseUserID(strings.TrimSpace(command.UserID.String()))
	if institutionErr != nil || userErr != nil || (!command.EnableLocalLogin && command.Password == "") {
		return nil, apperror.New("administrator_recovery.invalid")
	}

	passwordHash := ""
	if command.Password != "" {
		var err error
		passwordHash, err = s.hasher.Hash(ctx, command.Password)
		if err != nil {
			return nil, PasswordHashError(err, "administrator_recovery.unavailable")
		}
	}

	result, err := s.installations.RecoverAdministratorAccess(ctx, &store.AdministratorRecovery{
		InstitutionID:      institutionID,
		UserID:             userID,
		EnableLocalLogin:   command.EnableLocalLogin,
		RotatePasswordHash: passwordHash,
	})
	if err != nil {
		return nil, apperror.New("administrator_recovery.unavailable").Wrap(err)
	}
	if result == nil {
		return nil, apperror.New("administrator_recovery.unavailable").Wrap(errors.New("persistence returned no recovery result"))
	}

	return &AdministratorRecoveryResult{
		LocalLoginEnabled: result.LocalLoginEnabled,
		PasswordRotated:   result.PasswordRotated,
	}, nil
}

// ReconcileAdministratorRecovery records every completed offline recovery in
// ordinary audit before this node starts workers or network transports.
func (s *Service) ReconcileAdministratorRecovery(ctx context.Context, system invocation.System) error {
	if !system.Valid() {
		return errors.New("reconcile offline administrator recovery: invalid system identity")
	}
	_, err := s.installations.ReconcileAdministratorRecovery(ctx, &store.AdministratorRecoveryReconciliation{NodeID: s.nodeID})
	if err != nil {
		return fmt.Errorf("reconcile offline administrator recovery: %w", err)
	}
	return nil
}

// ReconcileSystemAdministratorRole adds newly registered grantable actions to
// the protected built-in Role before this node serves application traffic.
func (s *Service) ReconcileSystemAdministratorRole(ctx context.Context, system invocation.System) error {
	if !system.Valid() {
		return errors.New("reconcile protected system-administrator Role: invalid system identity")
	}

	_, err := s.installations.ReconcileSystemAdministratorRole(ctx, &store.SystemAdministratorRoleReconciliation{
		RequiredPermissions: model.AllActions(),
		ReconciledAt:        s.now(),
		AuditEvent: &model.AuditEvent{
			Action:     string(model.ActionRoleSystemAdminReconcile),
			Status:     model.AuditStatusSuccess,
			NodeID:     s.nodeID,
			ClientType: "system",
		},
	})
	if err != nil {
		return fmt.Errorf("reconcile protected system-administrator Role: %w", err)
	}

	return nil
}

// GetInstallationStatus reports whether the installation has been
// bootstrapped.
func (s *Service) GetInstallationStatus(
	ctx context.Context,
	_ invocation.Invocation,
) (*model.InstallationStatus, error) {
	state, err := s.installations.Get(ctx)
	if err != nil {
		if store.IsNotFound(err) {
			return &model.InstallationStatus{Initialized: false}, nil
		}
		return nil, apperror.New("installation.unavailable").Wrap(err)
	}

	if err := state.Validate(); err != nil {
		return nil, apperror.New("installation.unavailable").Wrap(err)
	}

	return &model.InstallationStatus{Initialized: true}, nil
}

// BootstrapInstallation creates the Institution, its protected System
// Administrator and the initial Access Policy once, for a caller that proves
// the deployment bootstrap secret. Attempts are rate limited per source.
func (s *Service) BootstrapInstallation(
	ctx context.Context,
	invocation invocation.Invocation,
	command BootstrapInstallationCommand,
) (*model.InstallationBootstrapResult, error) {
	if strings.TrimSpace(command.InstitutionName) == "" ||
		strings.TrimSpace(command.AdministratorUsername) == "" ||
		strings.TrimSpace(command.AdministratorEmail) == "" ||
		command.Password == "" {
		return nil, apperror.New("request.invalid").WithField("field", "bootstrap")
	}

	if err := s.checkBootstrapRateLimit(ctx, command.Source); err != nil {
		return nil, err
	}

	providedDigest := digestBootstrapSecret(command.BootstrapSecret)
	if !s.bootstrapAvailable || subtle.ConstantTimeCompare(providedDigest[:], s.bootstrapSecretDigest[:]) != 1 {
		return nil, apperror.New("installation.bootstrap_denied")
	}

	fingerprint, err := fingerprintBootstrapCommand(command)
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "bootstrap").Wrap(err)
	}

	if err = CheckNewPassword(command.Password, PasswordIdentity{
		Username:    command.AdministratorUsername,
		Email:       command.AdministratorEmail,
		DisplayName: command.AdministratorDisplayName,
		FirstName:   command.AdministratorFirstName,
		LastName:    command.AdministratorLastName,
	}); err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, PasswordHashError(err, "installation.unavailable")
	}

	metadata := invocation.RequestMetadata()
	administrator, defaultPictureJob, err := jobhandler.PrepareUserDefaultProfilePictureJob(&model.User{
		Username:    command.AdministratorUsername,
		Email:       command.AdministratorEmail,
		DisplayName: command.AdministratorDisplayName,
		FirstName:   command.AdministratorFirstName,
		LastName:    command.AdministratorLastName,
		Locale:      command.AdministratorLocale,
		Timezone:    command.AdministratorTimezone,
	}, s.now())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "administrator").Wrap(err)
	}

	administratorSettings, err := user.PrepareInitialSettingsDocument(administrator)
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "administrator").Wrap(err)
	}

	input := &store.InstallationBootstrap{
		BootstrapSecretDigest: providedDigest,
		CommandFingerprint:    fingerprint,
		Institution: &model.Institution{
			Name:        command.InstitutionName,
			DisplayName: command.InstitutionDisplayName,
			Description: command.InstitutionDescription,
		},
		Administrator:         administrator,
		AdministratorSettings: administratorSettings,
		PasswordHash:          hash,
		Role: &model.Role{
			Name:        model.SystemAdministratorRoleName,
			DisplayName: "System Administrator",
			Description: "Protected administrator role for this Proctor installation.",
			Permissions: model.AllActions(),
			BuiltIn:     true,
		},
		RoleBinding:  &model.RoleBinding{},
		AccessPolicy: model.NewInitialAccessPolicy(model.NewAccessPolicyID(), administrator.CreatedAt),
		AuditEvent: &model.AuditEvent{
			Action:     string(model.ActionInstallationBootstrap),
			RequestID:  metadata.RequestID,
			NodeID:     s.nodeID,
			ClientType: "bootstrap",
			AuthMethod: "bootstrap",
			IPAddress:  metadata.IPAddress,
			UserAgent:  metadata.UserAgent,
		},
		DefaultProfilePictureJob: defaultPictureJob,
	}
	result, err := s.installations.Bootstrap(ctx, input)
	if err != nil {
		if store.IsConflict(err) {
			return nil, apperror.New("installation.already_initialized").Wrap(err)
		}

		// The commit boundary may be ambiguous. Repeating the proof- and
		// fingerprint-fenced named aggregate either returns the retained exact
		// outcome or proves that this attempt did not win.
		retried, retryErr := s.installations.Bootstrap(ctx, input)
		if retryErr == nil {
			return retried, nil
		}
		if store.IsConflict(retryErr) {
			return nil, apperror.New("installation.already_initialized").Wrap(retryErr)
		}

		return nil, apperror.New("installation.unavailable").Wrap(errors.Join(err, retryErr))
	}

	return result, nil
}

// fixBootstrapProtection keeps only the digest of the deployment bootstrap
// secret, whether one is configured, and the bootstrap rate limit. New calls
// it once.
func (s *Service) fixBootstrapProtection(rateLimit LoginRateLimitPolicy, protection BootstrapProtectionPolicy) {
	s.bootstrapRateLimitWindow, s.bootstrapMaximumSourceAttempts = rateLimit.Window, rateLimit.MaximumSourceAttempts
	s.bootstrapSecretDigest, s.bootstrapAvailable = digestBootstrapSecret(protection.Secret), protection.Secret != ""
}

func digestBootstrapSecret(secret string) [sha256.Size]byte {
	return sha256.Sum256(append([]byte("proctor.installation_bootstrap_secret\x00"), []byte(secret)...))
}

func fingerprintBootstrapCommand(command BootstrapInstallationCommand) ([sha256.Size]byte, error) {
	payload, err := json.Marshal(struct {
		InstitutionName, InstitutionDisplayName, InstitutionDescription   string
		AdministratorUsername, AdministratorEmail                         string
		AdministratorDisplayName, AdministratorFirstName                  string
		AdministratorLastName, AdministratorLocale, AdministratorTimezone string
		Password                                                          string
	}{
		command.InstitutionName,
		command.InstitutionDisplayName,
		command.InstitutionDescription,
		command.AdministratorUsername,
		command.AdministratorEmail,
		command.AdministratorDisplayName,
		command.AdministratorFirstName,
		command.AdministratorLastName,
		command.AdministratorLocale,
		command.AdministratorTimezone,
		command.Password,
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}

	// The raw deployment secret keys the fingerprint so the durable value does
	// not become a fast offline password verifier if the database is exposed.
	mac := hmac.New(sha256.New, []byte(command.BootstrapSecret))
	_, _ = mac.Write([]byte("proctor.installation_bootstrap_command\x00"))
	_, _ = mac.Write(payload)
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], mac.Sum(nil))
	return fingerprint, nil
}

func (s *Service) checkBootstrapRateLimit(ctx context.Context, source string) error {
	_, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeInstallationBootstrap,
		window:  s.bootstrapRateLimitWindow,
		limits: []AttemptLimit{{
			dimension: authenticationAttemptDimensionSource,
			maximum:   s.bootstrapMaximumSourceAttempts,
			source:    source,
		}},
	})
	if err != nil {
		return apperror.New("administration.unavailable").Wrap(err)
	}
	if limited {
		return apperror.New("authentication.rate_limited")
	}
	return nil
}
