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

// ExamCopyReservation reserves a copy of an exam before its bytes are
// written: the new exam with its draft and owner as manager, invisible to
// every exam read until CommitCopy, and a staged Starter Workspace object for
// each file to copy. Nothing is reserved for the Resources here; their
// uploads are reserved through ExamResourceStore.ReserveUpload. A reservation
// never committed lapses at CopyingUntil, and the file purge job removes its
// objects and then the exam.
type ExamCopyReservation struct {
	Exam           *model.Exam
	Draft          *model.ExamDraft
	Manager        *model.ExamManager
	CopyingUntil   time.Time
	StarterObjects []*model.StarterWorkspaceObject
}

// ExamCopyResource is one Resource of the copy, with the upload its bytes
// were written to.
type ExamCopyResource struct {
	Resource  *model.ExamResource
	LeaseID   model.UploadLeaseID
	Rendition *model.FileRendition
}

// ExamCopyStarterEntry is one Starter Workspace entry of the copy. A file
// names the staged object its bytes were written to and their content.
type ExamCopyStarterEntry struct {
	Entry          *model.StarterWorkspaceEntry
	ContentVersion model.WorkspaceContentVersion
	MediaType      string
	SizeBytes      int64
	SHA256         string
}

// ExamCopyCommit makes a reserved copy real. It rechecks the actor's manager
// relationship with the source unless SourceManagerOverride, and completes
// both audits: the creation on the new exam and the copy on the source.
type ExamCopyCommit struct {
	SourceExamID          model.ExamID
	ExamID                model.ExamID
	ActorUserID           model.UserID
	SourceManagerOverride bool
	Resources             []ExamCopyResource
	StarterEntries        []ExamCopyStarterEntry
	ChangedAt             time.Time
	CreateAuditEventID    model.AuditEventID
	SourceAuditEventID    model.AuditEventID
	AuditAt               time.Time
}
