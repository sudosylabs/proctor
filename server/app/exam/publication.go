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

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Publication owns the authorization, audit, persistence and after-commit
// effect recipe for freezing Drafts into immutable Exam Revisions.
type Publication struct {
	revisions store.ExamRevisionStore
	// exams reads the Exam ownership a publication is authorized against.
	exams               store.ExamAuthoringStore
	academicUnitMembers store.AcademicUnitMemberStore

	authorizer        Authorizer
	auditor           Auditor
	publisher         realtime.Publisher
	now               func() time.Time
	newID             func() model.ExamRevisionID
	institutionOrigin string
	operational       OperationalRecorder
}

// PublicationDependencies are the collaborators NewPublication validates once
// and distributes to the Publication.
type PublicationDependencies struct {
	Persistence       store.ExamRevisionStore
	Access            store.ExamAuthoringStore
	Memberships       store.AcademicUnitMemberStore
	Authorizer        Authorizer
	Auditor           Auditor
	Publisher         realtime.Publisher
	Now               func() time.Time
	NewID             func() model.ExamRevisionID
	InstitutionOrigin string
	Operational       OperationalRecorder
}

// NewPublication returns a Publication service, or an error when a required
// dependency is missing.
func NewPublication(deps PublicationDependencies) (*Publication, error) {
	if deps.Persistence == nil ||
		deps.Access == nil ||
		deps.Memberships == nil ||
		deps.Authorizer == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Now == nil ||
		deps.NewID == nil {
		return nil, errors.New("Exam Revision publication dependencies are required")
	}

	return &Publication{
		revisions:           deps.Persistence,
		exams:               deps.Access,
		academicUnitMembers: deps.Memberships,
		authorizer:          deps.Authorizer,
		auditor:             deps.Auditor,
		publisher:           deps.Publisher,
		now:                 deps.Now,
		newID:               deps.NewID,
		institutionOrigin:   deps.InstitutionOrigin,
		operational:         deps.Operational,
	}, nil
}
