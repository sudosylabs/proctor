// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type examDraftUpdatedData struct {
	ExamID        string `json:"exam_id"`
	DraftRevision int64  `json:"draft_revision"`
}

type examStarterWorkspaceChangedData struct {
	ExamID        string `json:"exam_id"`
	EntryID       string `json:"entry_id"`
	Operation     string `json:"operation"`
	DraftRevision int64  `json:"draft_revision"`
	ChangedAt     string `json:"changed_at"`
}

type examArchivedData struct {
	ExamID       string `json:"exam_id"`
	ExamRevision int64  `json:"exam_revision"`
	ArchivedAt   string `json:"archived_at"`
}

type examManagerChangedData struct {
	ExamID       string `json:"exam_id"`
	UserID       string `json:"user_id"`
	Present      bool   `json:"present"`
	ExamRevision int64  `json:"exam_revision"`
	ChangedAt    string `json:"changed_at"`
}

type examOwnerTransferredData struct {
	ExamID       string `json:"exam_id"`
	OwnerUserID  string `json:"owner_user_id"`
	ExamRevision int64  `json:"exam_revision"`
	ChangedAt    string `json:"changed_at"`
}

type examRevisionPublishedData struct {
	ExamID       string `json:"exam_id"`
	RevisionID   string `json:"revision_id"`
	Number       int64  `json:"number"`
	PolicyDigest string `json:"policy_digest"`
	Kind         string `json:"kind"`
	PublishedAt  string `json:"published_at"`
}

// NewExamRevisionPublishedEvent tells viewers of an Exam that a new Exam
// Revision was published. The payload carries the Revision's number,
// publication kind, and policy digest, and none of the published content.
// policyDigest must be 64 characters long.
func NewExamRevisionPublishedEvent(
	revisionID model.ExamRevisionID,
	examID model.ExamID,
	number int64,
	policyDigest string,
	kind model.ExamRevisionPublicationKind,
	publishedAt time.Time,
) (Event, error) {
	if !revisionID.IsValid() || !examID.IsValid() || number < 1 || len(policyDigest) != 64 || !kind.IsValid() || publishedAt.IsZero() {
		return Event{}, errors.New("Exam Revision published event requires valid bounded metadata")
	}

	data, err := json.Marshal(examRevisionPublishedData{
		ExamID:       examID.String(),
		RevisionID:   revisionID.String(),
		Number:       number,
		PolicyDigest: policyDigest,
		Kind:         string(kind),
		PublishedAt:  model.FormatRFC3339(publishedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Revision published event: %w", err)
	}

	return Event{
		Name:     examRevisionPublishedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamCreatedEvent constructs the stable, content-free authoring event.
func NewExamCreatedEvent(examID model.ExamID) (Event, error) {
	if !examID.IsValid() {
		return Event{}, errors.New("exam created event requires a valid Exam ID")
	}
	return Event{
		Name:     examCreatedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
	}, nil
}

// NewExamDraftUpdatedEvent owns the bounded wire projection for a Draft text
// or policy change. Authored content and policy values are deliberately absent.
func NewExamDraftUpdatedEvent(examID model.ExamID, revision int64) (Event, error) {
	if !examID.IsValid() || revision < 1 {
		return Event{}, errors.New("exam Draft update event requires a valid identity and revision")
	}

	data, err := json.Marshal(examDraftUpdatedData{ExamID: examID.String(), DraftRevision: revision})
	if err != nil {
		return Event{}, fmt.Errorf("encode exam Draft update event: %w", err)
	}

	return Event{
		Name:     examDraftUpdatedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamStarterWorkspaceChangedEvent constructs the content-free fact emitted
// after one Starter Workspace hierarchy/content mutation commits. Logical
// paths, opaque object identities, checksums, and authored bytes are absent.
func NewExamStarterWorkspaceChangedEvent(
	examID model.ExamID,
	entryID model.StarterWorkspaceEntryID,
	revision int64,
	operation string,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() || !entryID.IsValid() || revision < 1 || !validExamStarterWorkspaceOperation(operation) || changedAt.IsZero() {
		return Event{}, errors.New("Starter Workspace change event requires valid identities, operation, revision, and time")
	}

	data, err := json.Marshal(examStarterWorkspaceChangedData{
		ExamID:        examID.String(),
		EntryID:       entryID.String(),
		Operation:     operation,
		DraftRevision: revision,
		ChangedAt:     model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Starter Workspace change event: %w", err)
	}

	return Event{
		Name:     examStarterWorkspaceChangedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

func validExamStarterWorkspaceOperation(operation string) bool {
	switch operation {
	case "directory_created", "file_created", "entry_moved", "file_replaced", "entry_removed":
		return true
	default:
		return false
	}
}

// NewExamArchivedEvent constructs the content-free Exam lifecycle event.
func NewExamArchivedEvent(examID model.ExamID, revision int64, archivedAt time.Time) (Event, error) {
	if !examID.IsValid() || revision < 1 || archivedAt.IsZero() {
		return Event{}, errors.New("exam archived event requires a valid identity, revision, and time")
	}

	data, err := json.Marshal(examArchivedData{
		ExamID:       examID.String(),
		ExamRevision: revision,
		ArchivedAt:   model.FormatRFC3339(archivedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode exam archived event: %w", err)
	}

	return Event{
		Name:     examArchivedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamManagerChangedEvent tells viewers of an Exam that userID was added
// as an Exam Manager (present true) or removed (present false). revision is
// the Exam's revision after the change. An ownership transfer is announced by
// NewExamOwnerTransferredEvent instead.
func NewExamManagerChangedEvent(
	examID model.ExamID,
	userID model.UserID,
	present bool,
	revision int64,
	changedAt time.Time,
) (Event, error) {
	if !examID.IsValid() || !userID.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("Exam Manager change event requires valid identities, revision, and time")
	}

	data, err := json.Marshal(examManagerChangedData{
		ExamID:       examID.String(),
		UserID:       userID.String(),
		Present:      present,
		ExamRevision: revision,
		ChangedAt:    model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam Manager change event: %w", err)
	}

	return Event{
		Name:     examManagerChangedMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}

// NewExamOwnerTransferredEvent tells those who may view the Exam that it has
// a new owner, at the Exam revision the transfer produced.
func NewExamOwnerTransferredEvent(examID model.ExamID, ownerID model.UserID, revision int64, changedAt time.Time) (Event, error) {
	if !examID.IsValid() || !ownerID.IsValid() || revision < 1 || changedAt.IsZero() {
		return Event{}, errors.New("Exam owner transfer event requires valid identities, revision, and time")
	}

	data, err := json.Marshal(examOwnerTransferredData{
		ExamID:       examID.String(),
		OwnerUserID:  ownerID.String(),
		ExamRevision: revision,
		ChangedAt:    model.FormatRFC3339(changedAt),
	})
	if err != nil {
		return Event{}, fmt.Errorf("encode Exam owner transfer event: %w", err)
	}

	return Event{
		Name:     examOwnerTransferredMessage.name,
		Action:   model.ActionExamView,
		Resource: model.Resource{Type: model.ResourceExam, ID: examID.String()},
		Data:     data,
	}, nil
}
