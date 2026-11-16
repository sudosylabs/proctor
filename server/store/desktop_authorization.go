// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// DesktopAuthorizationCodeIssue turns one browser-authentication proof into a
// short-lived code. It carries hashes only. The store atomically rechecks the
// access policy, provider capability, active user, expiry, purpose and every
// pinned proof, and starts CodeLifetime from the database clock.
type DesktopAuthorizationCodeIssue struct {
	BindingHash    string
	StateHash      string
	CodeHash       string
	ExpectedUserID model.UserID
	CodeLifetime   time.Duration
	Capabilities   AccessDeploymentCapabilities
	AuditEventID   model.AuditEventID
	AuditAt        time.Time
}

type DesktopAuthorizationCancellation struct {
	BindingHash string
	StateHash   string
}

// DesktopAuthorizationExchangeProof resolves the live authorization behind an
// exchange request without consuming it; Exchange repeats every check.
type DesktopAuthorizationExchangeProof struct {
	CodeHash                string
	StateHash               string
	CodeChallenge           string
	Issuer                  string
	ExpectedKeyThumbprint   string
	DesktopRelease          string
	DesktopBuildID          string
	DesktopPlatform         model.DesktopPlatform
	DesktopArchitecture     model.DesktopArchitecture
	DesktopRealtimeProtocol int
}

// DesktopAuthorizationExchangeTarget is the authorization an exchange proof
// resolves to and the user who approved it. IssueCode fixes the user, so it
// cannot change before Exchange consumes the code.
type DesktopAuthorizationExchangeTarget struct {
	ID     model.BrowserAuthenticationTransactionID
	UserID model.UserID
}

type DesktopAuthorizationCodeIssued struct {
	CallbackURL   string
	CodeExpiresAt time.Time
}

// DesktopAuthorizationExchange consumes one code and creates a Desktop session
// with its first access and refresh credentials, atomically. Every lifetime
// starts from one database timestamp.
type DesktopAuthorizationExchange struct {
	CodeHash                           string
	StateHash                          string
	CodeChallenge                      string
	Issuer                             string
	ExpectedPublicJWK                  model.DesktopPublicJWK
	ExpectedKeyThumbprint              string
	DesktopRelease                     string
	DesktopBuildID                     string
	DesktopPlatform                    model.DesktopPlatform
	DesktopArchitecture                model.DesktopArchitecture
	DesktopRealtimeProtocol            int
	DesktopCompatibilityPolicyRevision int64
	AccessTokenHash                    string
	RefreshTokenHash                   string
	AccessLifetime                     time.Duration
	RefreshLifetime                    time.Duration
	IdleLifetime                       time.Duration
	AbsoluteLifetime                   time.Duration
	MaximumActive                      int
	Capabilities                       AccessDeploymentCapabilities
	// MFARequiredByPolicy refuses a single-factor authorization with
	// ErrMFAEnrollmentRequired, because a Desktop session cannot carry the
	// enrollment restriction.
	MFARequiredByPolicy bool
	AuditEventID        model.AuditEventID
	AuditAt             time.Time
}

type DesktopAuthorizationExchangeResult struct {
	Session          *model.Session
	Registration     *model.DesktopRegistration
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	Denied           bool
}
