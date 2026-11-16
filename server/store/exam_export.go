// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"encoding/json"

	"github.com/sudosylabs/proctor/server/model"
)

const ExamExportCreateOperation = "exam.export.create"

type ExamExportAccess struct {
	ExamRecordsMutation
	ExportID   model.ExamExportID
	Categories []model.RetentionCategory
}

type ExamExportCreation struct {
	ExamExportAccess
	JobID         model.JobID
	SubmissionIDs []model.SubmissionID
}

// ExamExportFile is private input to building the archive. Storage locations
// and object identities must never reach the manifest or an HTTP response.
type ExamExportFile struct {
	SubmissionID model.SubmissionID
	Entry        model.ExamSubmissionManifestEntry
}

type ExamExportSnapshot struct {
	Export  *model.ExamExport
	Records json.RawMessage
	Files   []ExamExportFile
}

// BuildInputConsistent reports whether the snapshot is buildable: the export
// is queued, its records are present and within the size bound, and it lists
// as many files as the export counted. It dereferences Export unchecked, so
// call it only after validating Export.
func (s *ExamExportSnapshot) BuildInputConsistent() bool {
	return !(s == nil ||
		s.Export.State != model.ExamExportQueued ||
		len(s.Records) < 1 ||
		len(s.Records) > model.ExamExportMaximumSnapshotBytes ||
		len(s.Files) != s.Export.FileCount)
}

type ExamExportArtifact struct {
	ExportID  model.ExamExportID
	AttemptID model.JobAttemptID
}

type ExamExportBuild struct {
	ExamExportArtifact
	JobID      model.JobID
	ClaimToken model.JobClaimToken
}

type ExamExportPublication struct {
	ExamExportBuild
	SizeBytes int64
	SHA256    string
}

type ExamExportDownload struct {
	Export      *model.ExamExport
	Artifact    ExamExportArtifact
	Submissions []ExamSubmissionAuthorization
}

// ConsistentWith reports whether the result is the valid export asked for, on
// the given scope and requested by the given user.
func (d *ExamExportDownload) ConsistentWith(
	exportID model.ExamExportID,
	scope model.RetentionHoldScope,
	requesterID model.UserID,
) bool {
	return !(d == nil ||
		d.Export.Validate() != nil ||
		d.Export.ID != exportID ||
		d.Export.Scope != scope ||
		d.Export.RequesterUserID != requesterID)
}

// ExamExportStore persists exam exports. Create freezes the authorized
// snapshot and enqueues its Job, with retention holds on the sources, in one
// transaction. It locks the policy before the exam, sitting and submissions
// (in order), matching the retirement fence. Reads and replays recheck
// credentials, the manager relationship, and export and read authority. The
// integrity category also excludes the requester as candidate and requires the
// browser activity permission and current manager membership even under an
// override; retained categories, never the caller's, decide that check.
//
// Each build attempt registers a unique artifact before writing. Publication
// is fenced by the live Job claim, the source deadline and the request
// expiry, and only a verified publication releases the source holds. Failed
// or expired requests erase the frozen payload. Cleanup keeps uncertain
// writer identities for repeated removal, because seeing an object absent does
// not prove a late remote write will not land.
type ExamExportStore interface {
	ListScope(context.Context, model.RetentionHoldScope) ([]ExamSubmissionAuthorization, error)
	// ListCreationScope returns the originally captured scope for a replay, or
	// the current scope for a new command, without renewing any lifetime. It
	// grants no authority: Create repeats every check.
	ListCreationScope(context.Context, model.RetentionHoldScope, *CommandIdempotency) ([]ExamSubmissionAuthorization, error)
	Create(context.Context, *ExamExportCreation, *CommandIdempotency) (*model.ExamExport, error)
	Get(context.Context, *ExamExportAccess) (*ExamExportDownload, error)
	BeginBuild(context.Context, *ExamExportBuild) (*ExamExportSnapshot, error)
	Publish(context.Context, *ExamExportPublication) error
	FinishWriter(context.Context, ExamExportArtifact) error
	Reconcile(context.Context, int) (int, error)
	// BeginPurgeBatch selects the oldest due artifacts and defers each for one
	// hour before any I/O, so concurrent callers skip them. A failed or
	// uncertain attempt keeps the writer references and records no absence.
	// The caller reserves its work budget first, even when this call's result
	// is uncertain.
	BeginPurgeBatch(context.Context, int) ([]ExamExportArtifact, error)
	CompletePurge(context.Context, ExamExportArtifact) error
	// ListBuildTitles returns, per build Job, the title of the revision its
	// export's sitting runs. At most MaximumExamExportBuildTitles Jobs are read;
	// a Job with no export is absent. It grants no authority.
	ListBuildTitles(context.Context, []model.JobID) ([]ExamExportBuildTitle, error)
}

// MaximumExamExportBuildTitles bounds one ExamExportStore.ListBuildTitles read.
const MaximumExamExportBuildTitles = 200

// ExamExportBuildTitle is the exam title of one export build Job.
type ExamExportBuildTitle struct {
	JobID model.JobID
	Title string
}
