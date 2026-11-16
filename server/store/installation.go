// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// InstallationBootstrap is the input of the one-time bootstrap. PasswordHash
// is already encoded and must never be logged or audited.
type InstallationBootstrap struct {
	BootstrapSecretDigest    [sha256.Size]byte
	CommandFingerprint       [sha256.Size]byte
	Institution              *model.Institution
	Administrator            *model.User
	AdministratorSettings    *model.UserSettingsDocument
	PasswordHash             string
	Role                     *model.Role
	RoleBinding              *model.RoleBinding
	AccessPolicy             *model.AccessPolicy
	AuditEvent               *model.AuditEvent
	DefaultProfilePictureJob *model.Job
}

// SystemAdministratorRoleReconciliation adds newly registered actions to the
// built-in role. It never removes one, so a rolling downgrade keeps actions
// the older binary does not know.
type SystemAdministratorRoleReconciliation struct {
	RequiredPermissions []string
	ReconciledAt        time.Time
	AuditEvent          *model.AuditEvent
}

// SystemAdministratorRoleReconciliationResult is the role after
// reconciliation. A nil Role means the installation is not bootstrapped yet.
type SystemAdministratorRoleReconciliationResult struct {
	Role             *model.Role
	Changed          bool
	AddedPermissions []string
}

// AdministratorRecovery is the input of offline administrator recovery.
// RotatePasswordHash is already encoded; plaintext never crosses the store
// boundary.
type AdministratorRecovery struct {
	InstitutionID      model.InstitutionID
	UserID             model.UserID
	EnableLocalLogin   bool
	RotatePasswordHash string
}

type AdministratorRecoveryResult struct {
	RecordID          model.AdministratorRecoveryRecordID
	LocalLoginEnabled bool
	PasswordRotated   bool
}

// AdministratorRecoveryReconciliation runs at startup and turns pending
// offline recovery records into audit events.
type AdministratorRecoveryReconciliation struct {
	NodeID string
}

type AdministratorRecoveryReconciliationResult struct {
	Reconciled int
}

// InstallationStore bootstraps an empty database into an installation and
// runs offline administrator recovery.
type InstallationStore interface {
	Get(context.Context) (*model.InstallationState, error)
	Bootstrap(context.Context, *InstallationBootstrap) (*model.InstallationBootstrapResult, error)
	ReconcileSystemAdministratorRole(
		context.Context,
		*SystemAdministratorRoleReconciliation,
	) (*SystemAdministratorRoleReconciliationResult, error)
	RecoverAdministratorAccess(context.Context, *AdministratorRecovery) (*AdministratorRecoveryResult, error)
	ResetAdministratorMFA(context.Context, *AdministratorMFAReset) (*AdministratorMFAResetResult, error)
	ReconcileAdministratorRecovery(context.Context, *AdministratorRecoveryReconciliation) (*AdministratorRecoveryReconciliationResult, error)
}
