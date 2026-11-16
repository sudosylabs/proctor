// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Records owns records completion, retention holds and review waivers after
// delivery. Retention scheduling and deletion live elsewhere.
type Records struct {
	records             store.ExamRecordsStore
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	// authorizer also audits the denial of a self-waiver.
	authorizer *access.Authorizer
	auditor    access.MutationAuditAdapter
	recentTTL  time.Duration
	now        func() time.Time
	newHoldID  func() model.RetentionHoldID
}

// RecordsDependencies are the collaborators of NewRecords.
type RecordsDependencies struct {
	Persistence store.ExamRecordsStore
	Access      store.ExamAuthoringStore
	Memberships store.AcademicUnitMemberStore
	Authorizer  *access.Authorizer
	Auditor     *access.Auditor
	RecentTTL   time.Duration
	Now         func() time.Time
	NewHoldID   func() model.RetentionHoldID
}

// NewRecords returns a Records service, or an error when a required
// dependency is missing.
func NewRecords(deps RecordsDependencies) (*Records, error) {
	if deps.Persistence == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.RecentTTL <= 0 ||
		deps.Now == nil ||
		deps.NewHoldID == nil {
		return nil, errors.New("Exam records dependencies are required")
	}

	return &Records{
		records:             deps.Persistence,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor.MutationAudit(),
		recentTTL:           deps.RecentTTL,
		now:                 deps.Now,
		newHoldID:           deps.NewHoldID,
	}, nil
}
