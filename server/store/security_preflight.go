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
	SourceManifestDigest             string
	ConfigurationManifestFingerprint string
	AuditEventID                     model.AuditEventID
	AuditAt                          time.Time
}
type SecurityPreflightPrepared struct {
	ServerTime                 time.Time
	BrowserActivityDisclosure  model.BrowserActivityDisclosure
	Challenge                  model.SecurityPreflightChallenge
	Resolved                   model.ResolvedNativePolicy
	FrozenAttemptConfiguration *model.AttemptConfiguration
	Replayed                   bool
}

// ConsistentWith reports whether the prepared preflight carries a valid
// challenge and a policy that is valid at the server time and resolved for the
// given Sitting.
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
	CurrentSources             []model.NativeSourceCoverage
	CurrentCoverage            []model.NativeCoverageClaim
	SourceResetReceipts        []model.SourceResetReceipt
	SecurityCoverage           model.SecurityCoverageResult
	ServerTime                 time.Time
	Security                   model.AdmittedSecurity
	FrozenAttemptConfiguration model.AttemptConfiguration
}

// ConsistentWith reports whether the recovery carries its server time, valid
// security coverage, bounded and present source, coverage and reset receipt
// lists, a valid admitted security posture bound to the given Attempt, and a
// valid frozen Attempt configuration.
func (r *SecurityPolicyRecovery) ConsistentWith(attemptID model.ExamAttemptID) bool {
	return !(r == nil ||
		r.ServerTime.IsZero() ||
		r.SecurityCoverage.Validate() != nil ||
		r.CurrentSources == nil ||
		r.CurrentCoverage == nil ||
		r.SourceResetReceipts == nil ||
		len(r.CurrentSources) > 11 ||
		len(r.CurrentCoverage) > 64 ||
		len(r.SourceResetReceipts) > 11 ||
		r.Security.Validate() != nil ||
		r.Security.Policy.Scope.AttemptID != attemptID ||
		r.FrozenAttemptConfiguration.Validate() != nil)
}

type ExamAttemptSecurityCoverageUpdate struct {
	Access                             CandidateAttemptAccess
	ParticipationID                    model.AttemptParticipationID
	Generation                         int64
	DesktopBuild                       model.DesktopBuildTuple
	DesktopCompatibilityPolicyRevision int64
	Coverage                           model.SecurityCoverageRenewal
}
