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

// SecurityPreflightAccess is an authenticated selector, never a wire DTO.
// Build meaning comes from the verified server release catalog.
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

// ConsistentWith reports whether the prepared preflight carries a valid
// challenge and a policy that is valid at the server time and resolved for the
// given Sitting.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
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

// SecurityPolicyRecovery returns privileged admission provenance only to the
// active owner's registered Session. It does not grant or renew a lease.
type SecurityPolicyRecovery struct {
	Security                   model.AdmittedSecurity
	Control                    model.SecurityControlResult
	ServerTime                 time.Time
	FrozenAttemptConfiguration model.AttemptConfiguration
}

// ConsistentWith reports whether the recovery carries its server time, a
// valid renewal result, a valid admitted security binding bound to the given
// Attempt, and a valid frozen Attempt configuration.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
func (r *SecurityPolicyRecovery) ConsistentWith(attemptID model.ExamAttemptID) bool {
	return !(r == nil ||
		r.ServerTime.IsZero() ||
		r.Control.Validate() != nil ||
		r.Security.Validate() != nil ||
		r.Security.Policy.Scope.AttemptID != attemptID ||
		r.FrozenAttemptConfiguration.Validate() != nil)
}

// SecurityPreflightStore persists the security preflight a Candidate's
// Desktop completes before it connects to a Sitting, and the recovery of the
// security policy an admitted Attempt runs under.
type SecurityPreflightStore interface {
	// PrepareSecurityPreflight issues the preflight challenge for the
	// Candidate's Desktop build against the Sitting's current policy. A
	// replayed command returns the original challenge.
	PrepareSecurityPreflight(context.Context, *SecurityPreflightPrepare, *CommandIdempotency) (*SecurityPreflightPrepared, error)
	// ResolveSecurityPreflightSitting returns the Sitting of a preflight this
	// User and Session prepared.
	ResolveSecurityPreflightSitting(context.Context, string, model.UserID, model.SessionID) (model.ExamSittingID, error)
	// ReportSecurityPreflight evaluates the Desktop's report against the
	// prepared challenge and records the result.
	ReportSecurityPreflight(context.Context, *SecurityPreflightReport, *CommandIdempotency) (*model.SecurityPreflightResult, error)
	// RecoverSecurityPolicy returns the admitted security policy, frozen
	// Attempt Configuration and native control state of the Candidate's active
	// Participation, for a Desktop that lost its Connect response.
	RecoverSecurityPolicy(context.Context, SecurityPreflightAccess, model.ExamAttemptID) (*SecurityPolicyRecovery, error)
	// ReadSecurityPolicyOverview resolves the Sitting's current policy for the
	// readiness overview, under the same eligibility checks as preparation,
	// without creating a preflight.
	ReadSecurityPolicyOverview(context.Context, SecurityPreflightAccess) (*SecurityPolicyOverview, error)
}

// SecurityPolicyOverview is the policy the readiness overview checks the
// candidate's machine against before Start.
type SecurityPolicyOverview struct {
	ServerTime                time.Time
	Resolved                  model.ResolvedNativePolicy
	BrowserActivityDisclosure model.BrowserActivityDisclosure
	ScreenRecordingDisclosure *model.ScreenRecordingDisclosure
}
