// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ResultReleaseMailPreparation is the safe identity of one result release;
// the preparer resolves the recipient and what the notice shows.
type ResultReleaseMailPreparation struct {
	ExpectedReviewRevision int64
	CandidateUserID        model.UserID
	ExamID                 model.ExamID
	SittingID              model.ExamSittingID
	ReviewID               model.SubmissionReviewID
	ReleasedAt             time.Time
}

// PreparedResultReleaseMail is a frozen release notice together with the
// recipient User revision it was prepared against, which the releasing Store
// operation receives as its fence.
type PreparedResultReleaseMail struct {
	Notice                    *store.PreparedMail
	ExpectedRecipientRevision int64
}

// Dependencies are the collaborators New validates once and distributes to
// the Service.
type Dependencies struct {
	Persistence   store.ExamIntegrityReviewStore
	Authorizer    SittingManagerAuthorizer
	Auditor       *access.Auditor
	Publisher     realtime.Publisher
	Mail          ResultReleaseMail
	Now           func() time.Time
	NewReviewID   func() model.SubmissionReviewID
	NewDecisionID func() model.IntegrityReviewDecisionID
}

// Service holds the Integrity Review use cases: recording decisions,
// finalizing a Review, releasing the result, and the manager and candidate
// reads.
type Service struct {
	reviews store.ExamIntegrityReviewStore

	authorizer SittingManagerAuthorizer
	// auditor records the attempt and failure of each audited Review
	// mutation; the Store completes the attempt with the change it commits.
	auditor       access.MutationAuditAdapter
	publisher     realtime.Publisher
	mail          ResultReleaseMail
	now           func() time.Time
	newReviewID   func() model.SubmissionReviewID
	newDecisionID func() model.IntegrityReviewDecisionID
}

// New builds the Service, or fails when a collaborator is missing.
func New(deps Dependencies) (*Service, error) {
	if deps.Persistence == nil ||
		deps.Authorizer.Sittings == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Mail.Composer == nil ||
		deps.Mail.Users == nil ||
		deps.Mail.Sittings == nil ||
		deps.Mail.Revisions == nil ||
		deps.Now == nil ||
		deps.NewReviewID == nil ||
		deps.NewDecisionID == nil {
		return nil, errors.New("Exam Integrity Review dependencies are required")
	}

	return &Service{
		reviews:       deps.Persistence,
		authorizer:    deps.Authorizer,
		auditor:       deps.Auditor.MutationAudit(),
		publisher:     deps.Publisher,
		mail:          deps.Mail,
		now:           deps.Now,
		newReviewID:   deps.NewReviewID,
		newDecisionID: deps.NewDecisionID,
	}, nil
}
