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

const (
	SecurityPreflightPrepareOperation = "exam.security_preflight.prepare"
	SecurityPreflightReportOperation  = "exam.security_preflight.report"
)

// SecurityPreflightAccess is built from the authenticated request, never
// decoded from the wire. What DesktopBuild means comes from the server's
// verified release catalog.
type SecurityPreflightAccess struct {
	SittingID                          model.ExamSittingID
	CandidateUserID                    model.UserID
	SessionID                          model.SessionID
	DesktopRegistrationID              model.DesktopRegistrationID
	DPoPKeyThumbprint                  string
	DesktopBuild                       model.DesktopBuildTuple
	DesktopCompatibilityPolicyRevision int64
}
type SecurityPreflightPrepare struct {
	Access                           SecurityPreflightAccess
	AttemptID                        model.ExamAttemptID
	PreflightID                      string
	Challenge                        string
	NativeRegistryDigest             string
	ConfigurationManifestFingerprint string
	AuditEventID                     model.AuditEventID
	AuditAt                          time.Time
}
type SecurityPreflightPrepared struct {
	ServerTime                time.Time
	BrowserActivityDisclosure model.BrowserActivityDisclosure
	// ScreenRecordingDisclosure is set exactly when the policy records the
	// screen.
	ScreenRecordingDisclosure  *model.ScreenRecordingDisclosure
	Challenge                  model.SecurityPreflightChallenge
	Resolved                   model.ResolvedNativePolicy
	FrozenAttemptConfiguration *model.AttemptConfiguration
	Replayed                   bool
}

// ConsistentWith reports whether the challenge is valid and the policy is
// valid at ServerTime and resolved for the sitting.
func (p *SecurityPreflightPrepared) ConsistentWith(sittingID model.ExamSittingID) bool {
	return !(p == nil ||
		p.Challenge.Validate() != nil ||
		p.Resolved.Policy.Validate(p.ServerTime) != nil ||
		p.Resolved.Policy.SittingID != sittingID)
}

type SecurityPreflightReport struct {
	Access       SecurityPreflightAccess
	PreflightID  string
	Report       model.SecurityPreflightReport
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// SecurityPolicyRecovery is returned only to the active owner's registered
// session. Recovering it grants and renews no lease.
type SecurityPolicyRecovery struct {
	Security                   model.AdmittedSecurity
	Control                    model.SecurityControlResult
	ServerTime                 time.Time
	FrozenAttemptConfiguration model.AttemptConfiguration
}

// ConsistentWith reports whether every part is present and valid and the
// admitted security is bound to the attempt.
func (r *SecurityPolicyRecovery) ConsistentWith(attemptID model.ExamAttemptID) bool {
	return !(r == nil ||
		r.ServerTime.IsZero() ||
		r.Control.Validate() != nil ||
		r.Security.Validate() != nil ||
		r.Security.Policy.Scope.AttemptID != attemptID ||
		r.FrozenAttemptConfiguration.Validate() != nil)
}

// SecurityPreflightStore persists the preflight a candidate's Desktop
// completes before connecting to a sitting, and recovers the policy an
// admitted attempt runs under.
type SecurityPreflightStore interface {
	// PrepareSecurityPreflight issues the challenge for the Desktop build
	// against the sitting's current policy. A replay returns the original
	// challenge.
	PrepareSecurityPreflight(context.Context, *SecurityPreflightPrepare, *CommandIdempotency) (*SecurityPreflightPrepared, error)
	// ResolveSecurityPreflightSitting finds only a preflight this user and
	// session prepared.
	ResolveSecurityPreflightSitting(context.Context, string, model.UserID, model.SessionID) (model.ExamSittingID, error)
	ReportSecurityPreflight(context.Context, *SecurityPreflightReport, *CommandIdempotency) (*model.SecurityPreflightResult, error)
	// RecoverSecurityPolicy serves a Desktop that lost its Connect response.
	RecoverSecurityPolicy(context.Context, SecurityPreflightAccess, model.ExamAttemptID) (*SecurityPolicyRecovery, error)
	// ReadSecurityPolicyOverview applies the same eligibility checks as
	// preparation but creates no preflight.
	ReadSecurityPolicyOverview(context.Context, SecurityPreflightAccess) (*SecurityPolicyOverview, error)
}

// SecurityPolicyOverview is the policy the readiness check compares the
// candidate's machine with before start, and what the candidate reads while
// they wait: the instructions, FocusLoss and BrowserPolicy as frozen in the
// Sitting's revision. InstructionsMarkdown is stored Markdown, not yet
// sanitized.
type SecurityPolicyOverview struct {
	InstructionsMarkdown      string
	ServerTime                time.Time
	Resolved                  model.ResolvedNativePolicy
	BrowserActivityDisclosure model.BrowserActivityDisclosure
	ScreenRecordingDisclosure *model.ScreenRecordingDisclosure
	FocusLoss                 model.FocusLossPolicy
	BrowserPolicy             model.BrowserPolicy
}
