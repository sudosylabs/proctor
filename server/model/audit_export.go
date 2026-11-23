// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/hex"
	"time"
)

const (
	// AuditExportMaximumEvents bounds one export; a larger selection is
	// refused when it is requested rather than cut short.
	AuditExportMaximumEvents       = 100_000
	AuditExportMaximumBytes  int64 = 512 << 20
	AuditExportMaximumAttempts     = 3

	JobTypeAuditExportBuild   JobType = "audit_export.build"
	JobTypeAuditExportCleanup JobType = "audit_export.cleanup"
)

// AuditExportFormat is the file format of an audit export.
type AuditExportFormat string

const (
	AuditExportCSV   AuditExportFormat = "csv"
	AuditExportJSONL AuditExportFormat = "jsonl"
)

func (f AuditExportFormat) IsValid() bool { return f == AuditExportCSV || f == AuditExportJSONL }

// AuditExportState is where an audit export is in its life. It shares the
// exam export's states: built once, then expired by time alone.
type AuditExportState = ExamExportState

// AuditExport is a temporary file of the audit events one reader could list
// when they asked for it. Its filters and visibility are frozen then; its
// expiry counts from the request and nothing extends it.
type AuditExport struct {
	ID              AuditExportID
	RequesterUserID UserID
	Format          AuditExportFormat
	// Redacted marks an academic-only reader's export, which carries the
	// academic projection of each event.
	Redacted   bool
	State      AuditExportState
	JobID      JobID
	EventCount int
	CreatedAt  time.Time
	ExpiresAt  time.Time
	SizeBytes  int64
	SHA256     string
	ReadyAt    OptionalTime
}

func (e *AuditExport) Validate() error {
	if e == nil ||
		!e.ID.IsValid() ||
		!e.RequesterUserID.IsValid() ||
		!e.Format.IsValid() ||
		!e.JobID.IsValid() ||
		e.EventCount < 0 ||
		e.EventCount > AuditExportMaximumEvents ||
		e.CreatedAt.IsZero() ||
		!e.ExpiresAt.After(e.CreatedAt) ||
		e.ExpiresAt.After(e.CreatedAt.Add(ExamExportMaximumDays*24*time.Hour)) {
		return invalidModelError("AuditExport.Validate", "audit_export", "", "invalid audit export", "")
	}

	switch e.State {
	case ExamExportQueued, ExamExportFailed, ExamExportExpired:
		if e.SizeBytes != 0 || e.SHA256 != "" || e.ReadyAt.Valid {
			return invalidModelError("AuditExport.Validate", "audit_export", "", "unavailable audit export contains file metadata", "")
		}
	case ExamExportReady:
		if !e.ReadyAt.Valid ||
			e.ReadyAt.Time.Before(e.CreatedAt) ||
			!e.ReadyAt.Time.Before(e.ExpiresAt) ||
			!ValidAuditExportContent(e.SizeBytes, e.SHA256) {
			return invalidModelError("AuditExport.Validate", "audit_export", "", "invalid ready audit export", "")
		}
	default:
		return invalidModelError("AuditExport.Validate", "audit_export", "", "invalid audit export state", "")
	}

	return nil
}

// ValidAuditExportContent accepts an empty file: a selection can match no
// event, and its header still makes a CSV file non-empty.
func ValidAuditExportContent(size int64, digest string) bool {
	if size < 0 || size > AuditExportMaximumBytes || len(digest) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && hex.EncodeToString(decoded) == digest
}
