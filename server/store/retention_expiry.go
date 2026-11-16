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

// RetentionExpiryListOptions pages one scan. Before is the event-time bound
// every page of the scan shares; zero on the first page means the database
// clock.
type RetentionExpiryListOptions struct {
	Kind         model.RetentionExpiryKind
	CleanupAudit bool
	AfterID      string
	Before       time.Time
	Limit        int
}

type RetentionExpiryPage struct {
	InstitutionID model.InstitutionID
	Before        time.Time
	Items         []model.RetentionExpiryRecord
	HasMore       bool
}

type RetentionExpiryReconciliation struct {
	Kind         model.RetentionExpiryKind
	RecordID     string
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type RetentionExpiryResult struct {
	Scheduled bool
	Canceled  bool
	Expired   bool
}

// RetentionCleanupAuditReconciliation expires the cleanup workflow's own audit
// history. AuditEvent is written, with counts, only when a batch changes
// something; a no-change scan writes no audit and queues no further work.
type RetentionCleanupAuditReconciliation struct {
	AfterID    model.AuditEventID
	Before     time.Time
	Limit      int
	AuditEvent *model.AuditEvent
}
type RetentionCleanupAuditResult struct {
	Before    time.Time
	AfterID   model.AuditEventID
	HasMore   bool
	Examined  int
	Changed   int
	Scheduled int
	Canceled  int
	Expired   int
}
