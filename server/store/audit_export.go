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

const AuditExportCreateOperation = "audit.export.create"

// UsesAcademicTopology reports whether this scope changes when academic
// ownership changes.
func (scope AuditVisibilityScope) UsesAcademicTopology() bool {
	return !scope.InstitutionWide && !scope.AcademicInstitutionWide && len(scope.AcademicUnitRootIDs) > 0
}

// AuditExportCreation queues one audit export and its build Job. Query is the
// audit listing it freezes: filters and visibility, with no page bounds.
type AuditExportCreation struct {
	Export       *model.AuditExport
	Query        AuditListOptions
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	// TopologyRevision is required for a subtree-scoped export and zero otherwise.
	TopologyRevision int64
}

type AuditExportArtifact struct {
	ExportID  model.AuditExportID
	AttemptID model.JobAttemptID
}

type AuditExportBuild struct {
	AuditExportArtifact
	JobID      model.JobID
	ClaimToken model.JobClaimToken
}

// AuditExportSnapshot is what a build reads: the queued export and the
// listing it froze. Query is zero for a ready export.
type AuditExportSnapshot struct {
	Export *model.AuditExport
	Query  AuditListOptions
}

type AuditExportPublication struct {
	AuditExportBuild
	SizeBytes  int64
	SHA256     string
	EventCount int
}

// AuditExportRead is an export with the artifact its file is stored under,
// set only while it is ready.
type AuditExportRead struct {
	Export   *model.AuditExport
	Artifact AuditExportArtifact
	// Visibility is the audit visibility frozen at the request.
	Visibility AuditVisibilityScope
	// TopologyChanged prevents access to files under an obsolete subtree.
	TopologyChanged bool
}

// AuditExportStore persists audit exports on the shared export lifecycle:
// each build attempt registers its artifact before writing, publication is
// fenced by the live Job claim and the expiry, and cleanup forgets a file
// only after observing its absence. It grants no authority; the caller checks
// the requester and their current audit visibility.
type AuditExportStore interface {
	FindCreation(context.Context, *CommandIdempotency) (model.AuditExportID, error)
	TopologyRevision(context.Context) (int64, error)
	Create(context.Context, *AuditExportCreation, *CommandIdempotency) (*model.AuditExport, error)
	Get(context.Context, model.AuditExportID) (*AuditExportRead, error)
	BeginBuild(context.Context, *AuditExportBuild) (*AuditExportSnapshot, error)
	Publish(context.Context, *AuditExportPublication) error
	FinishWriter(context.Context, AuditExportArtifact) error
	// Reconcile expires the exports past their expiry and fails the queued
	// ones whose Job has ended, up to the limit, and reports how many.
	Reconcile(context.Context, int) (int, error)
	BeginPurgeBatch(context.Context, int) ([]AuditExportArtifact, error)
	CompletePurge(context.Context, AuditExportArtifact) error
}
