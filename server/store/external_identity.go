// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type ExternalIdentityResolution struct {
	Identity    *model.ExternalIdentity
	User        *model.User
	Provisioned bool
}

type ExternalIdentityResolutionRequest struct {
	Identity                 *model.ExternalIdentity
	User                     *model.User
	Settings                 *model.UserSettingsDocument
	Capabilities             AccessDeploymentCapabilities
	AuditEvent               *model.AuditEvent
	DefaultProfilePictureJob *model.Job
}

// ExternalIdentityLink attaches a proved provider subject to the current
// user.
type ExternalIdentityLink struct {
	ExternalLoginStateID model.ExternalLoginStateID
	Identity             *model.ExternalIdentity
	Capabilities         AccessDeploymentCapabilities
	AuditEventID         model.AuditEventID
	AuditAt              time.Time
}

// ExternalIdentityUnlink removes one provider identity and revokes only the
// sessions that authenticated through it.
type ExternalIdentityUnlink struct {
	ID               model.ExternalIdentityID
	UserID           model.UserID
	Capabilities     AccessDeploymentCapabilities
	ChangedAt        time.Time
	RevocationReason model.SessionRevocationReason
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

type AuthenticationMethodMutationResult struct {
	Identity           *model.ExternalIdentity
	PasswordCredential *model.PasswordCredential
	RevokedSessions    []*model.Session
	RevokedTokenHashes []string
}

// ExternalIdentityStore persists provider-subject links. ResolveOrProvision
// either finds the link or creates a user and link; it never merges accounts
// by email.
type ExternalIdentityStore interface {
	Save(context.Context, *model.ExternalIdentity) (*model.ExternalIdentity, error)
	Get(context.Context, model.ExternalIdentityID) (*model.ExternalIdentity, error)
	GetByProviderSubject(context.Context, string, string) (*model.ExternalIdentity, error)
	ListByUser(context.Context, model.UserID) ([]*model.ExternalIdentity, error)
	ResolveOrProvision(context.Context, *ExternalIdentityResolutionRequest) (*ExternalIdentityResolution, error)
	Link(context.Context, *ExternalIdentityLink) (*AuthenticationMethodMutationResult, error)
	Unlink(context.Context, *ExternalIdentityUnlink) (*AuthenticationMethodMutationResult, error)
}

// ExternalLoginStateStore persists hashed, browser-bound, single-use login
// transactions so any node can take the provider callback.
type ExternalLoginStateStore interface {
	// Save starts the lifetime from the database clock; callers give no
	// absolute times.
	Save(context.Context, *ExternalLoginStateCreation) (*model.ExternalLoginState, error)
	// SaveInvitationAdmission looks up the pending invitation by claim digest
	// and binds it to the new transaction atomically. The digest is not
	// stored in the login state.
	SaveInvitationAdmission(context.Context, *ExternalLoginStateInvitationAdmission) (*model.ExternalLoginState, error)
	GetByStateHash(context.Context, string) (*model.ExternalLoginState, error)
	Consume(context.Context, *ExternalLoginStateConsumption) (*model.ExternalLoginState, error)
	Maintain(context.Context, int) (*ExternalLoginStateMaintenanceResult, error)
}

// ExternalLoginStateMaintenanceResult reports one bounded pass on the
// database clock. Terminalized counts abandoned provider-connection audit
// attempts completed as failures; Purged counts rows removed after retention.
type ExternalLoginStateMaintenanceResult struct {
	Terminalized int
	Purged       int
	More         bool
}

// ExternalLoginStateConsumption is the input to ExternalLoginStateStore.Consume.
type ExternalLoginStateConsumption struct {
	Provider    string
	StateHash   string
	BindingHash string
}

// ExternalLoginStateCreation is the input to ExternalLoginStateStore.Save.
type ExternalLoginStateCreation struct {
	State    *model.ExternalLoginState
	Lifetime time.Duration
}

// ExternalLoginStateInvitationAdmission is the input to ExternalLoginStateStore.SaveInvitationAdmission.
type ExternalLoginStateInvitationAdmission struct {
	State     *model.ExternalLoginState
	Lifetime  time.Duration
	ClaimHash string
}
