// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamAttemptExpiryPeriodicRunnerUsesBoundedBatchAndPreservesFailure(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("database unavailable")
	stores := &jobAttemptStores{participationErr: wantErr}
	attempts := newJobAttempts(t, stores, nil)
	err := (ExamAttemptExpiryRunner{Attempts: attempts.service}).Run(context.Background())
	require.ErrorIs(t, err, wantErr, "Run() error")
	require.Equal(t, []int{examAttemptExpiryBatchLimit}, stores.participationLimits, "Run() error")
	// The service refuses an invalid system caller before it reads anything,
	// so the read shows the runner named a valid runtime task.
	require.Empty(t, stores.deliveryLimits, "a failed Participation scan ends the pass")
	require.Equal(t, 2*time.Second, ExamAttemptExpiryScanInterval, "Run() error")
	require.NotEmpty(t, ExamAttemptExpiryTaskName, "Run() error")
}

func TestExpiryRunnerAlsoSettlesAbandonedDeliveries(t *testing.T) {
	stores := &jobAttemptStores{}
	attempts := newJobAttempts(t, stores, nil)
	require.NoError(t, (ExamAttemptExpiryRunner{Attempts: attempts.service}).Run(context.Background()), "delivery scan bound")
	require.Equal(t, []int{200}, stores.deliveryLimits, "delivery scan bound")
}

// jobAttempts is a real Attempt service over the Store contracts the Attempt
// Jobs reach, deciding and auditing with the real accesstest auditor.
type jobAttempts struct {
	service *attempt.Service
	access  *accesstest.Fixture
}

// newJobAttempts builds the service. The Sitting service it authorizes
// managers through is sittings, or one over an empty lifecycle store when
// nil; Store contracts the Attempt Jobs never reach are nil embeds, so an
// unexpected call panics.
func newJobAttempts(t *testing.T, stores *jobAttemptStores, sittings *sitting.Service) jobAttempts {
	t.Helper()
	control := accesstest.New(t, accesstest.Options{})
	if sittings == nil {
		sittings, _ = newLifecycleSittings(t, newSittingLifecycleStore())
	}
	composer, err := mail.NewComposer(mail.ComposerDependencies{
		Renderer: mailtest.NewRenderer(t),
		Sender:   &mailSenderFake{enabled: true, from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}},
		Sealer:   mailTestSealer(t),
	})
	require.NoError(t, err)
	sittingStore := sealingSittingStore{sitting: stores.sitting}
	service, err := attempt.New(attempt.Dependencies{
		Persistence:       stores,
		FocusLoss:         struct{ store.ExamFocusLossStore }{},
		SecurityPreflight: struct{ store.SecurityPreflightStore }{},
		NativeDelivery:    struct{ store.NativeDeliveryStore }{},
		BrowserDelivery:   struct{ store.BrowserDeliveryStore }{},
		DeliveryBudget:    deliveryExpiryStore{stores: stores},
		Workspace: struct {
			store.ExamAttemptWorkspaceStore
		}{},
		Submissions: sealingSubmissionStore{stores: stores},
		Sittings:    sittingStore,
		Managers:    attempt.SittingManagerAuthorizer{Sittings: sittings, Submissions: sealingSubmissionStore{stores: stores}, Audit: control.Auditor},
		Auditor:     control.Auditor,
		Publisher:   &realtimetest.Publisher{},
		Content:     struct{ attempt.Content }{},
		Mail: attempt.SubmissionReceiptMail{Composer: composer, Users: sealingCandidateStore{}, Sittings: sittingStore,
			Revisions: sittingMailRevisionStoreFake{summary: &store.ExamRevisionSummary{Title: "Algorithms"}}},
		DesktopBuilds:       struct{ attempt.DesktopBuildResolver }{},
		Now:                 sealingTestNow,
		NewAttemptID:        model.NewExamAttemptID,
		NewWorkspaceID:      model.NewExamAttemptWorkspaceID,
		NewParticipation:    model.NewAttemptParticipationID,
		NewConnection:       model.NewAttemptConnectionID,
		NewEvidence:         model.NewIntegrityEvidenceID,
		NewFlag:             model.NewIntegrityFlagID,
		NewSuspension:       model.NewAttemptSuspensionID,
		NewFocusLossSignal:  model.NewFocusLossSignalID,
		NewDiscrepancy:      model.NewIntegrityDiscrepancyID,
		NewWorkspaceEntry:   model.NewAttemptWorkspaceEntryID,
		NewWorkspaceObject:  model.NewAttemptWorkspaceObjectID,
		NewWorkspaceVersion: model.NewWorkspaceContentVersion,
		NewSubmission:       model.NewSubmissionID,
		NewReceiptCode:      model.NewSubmissionReceiptCode,
		NewDomainEvent:      model.NewDomainEventID,
		NewJob:              model.NewJobID,
	})
	require.NoError(t, err)
	return jobAttempts{service: service, access: control}
}

// jobAttemptStores records what the Attempt Jobs ask of persistence: the
// expiry scans' bounds and the automatic seals the sealing Job commits.
type jobAttemptStores struct {
	store.ExamAttemptStore
	mu                  sync.Mutex
	participationErr    error
	participationLimits []int
	deliveryLimits      []int
	sitting             *model.ExamSitting
	targets             []store.ExamSubmissionAutomaticSealTarget
	seals               []*store.ExamSubmissionAutomaticSeal
}

func (s *jobAttemptStores) ListExpiredParticipations(_ context.Context, limit int) ([]store.ExamAttemptParticipationExpiryDue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.participationLimits = append(s.participationLimits, limit)
	return nil, s.participationErr
}

// sealed returns the Attempts the sealing Job sealed, in order.
func (s *jobAttemptStores) sealed() []model.ExamAttemptID {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]model.ExamAttemptID, 0, len(s.seals))
	for _, seal := range s.seals {
		ids = append(ids, seal.Target.AttemptID)
	}
	return ids
}

type deliveryExpiryStore struct {
	store.DeliveryBudgetStore
	stores *jobAttemptStores
}

func (s deliveryExpiryStore) ListExpiredDeliveries(_ context.Context, limit int) ([]store.DeliveryExpiryDue, error) {
	s.stores.mu.Lock()
	defer s.stores.mu.Unlock()
	s.stores.deliveryLimits = append(s.stores.deliveryLimits, limit)
	return nil, nil
}

// sealingSubmissionStore pages the targets by Attempt ID and seals each one
// fresh, as a Closing Sitting's first sealing pass finds them.
type sealingSubmissionStore struct {
	store.ExamSubmissionStore
	stores *jobAttemptStores
}

func (s sealingSubmissionStore) ListAutomaticSealTargets(_ context.Context, options store.ExamSubmissionAutomaticSealListOptions) ([]store.ExamSubmissionAutomaticSealTarget, error) {
	start := 0
	if !options.AfterAttemptID.IsZero() {
		for index := range s.stores.targets {
			if s.stores.targets[index].AttemptID == options.AfterAttemptID {
				start = index + 1
				break
			}
		}
	}
	end := min(start+options.Limit, len(s.stores.targets))
	return append([]store.ExamSubmissionAutomaticSealTarget(nil), s.stores.targets[start:end]...), nil
}

func (sealingSubmissionStore) PrepareAutomaticSeal(context.Context, store.ExamSubmissionAutomaticSealTarget) (*store.ExamSubmissionAutomaticSealPreparation, error) {
	return &store.ExamSubmissionAutomaticSealPreparation{SealAt: sealingTestNow()}, nil
}

func (s sealingSubmissionStore) SealForSittingClose(_ context.Context, seal *store.ExamSubmissionAutomaticSeal) (*store.ExamSubmissionAutomaticSealResult, error) {
	s.stores.mu.Lock()
	defer s.stores.mu.Unlock()
	s.stores.seals = append(s.stores.seals, seal)
	target := seal.Target
	return &store.ExamSubmissionAutomaticSealResult{ExamSubmissionSealResult: store.ExamSubmissionSealResult{
		Receipt: store.ExamSubmissionReceipt{AssistantTranscriptDigest: model.NewAssistantInventory().Digest, SubmissionID: seal.SubmissionID, ReceiptCode: seal.ReceiptCode, AttemptID: target.AttemptID,
			ExamRevisionID: target.CurrentRevisionID, State: model.ExamAttemptSubmitted,
			ManifestDigest: "0000000000000000000000000000000000000000000000000000000000000000", SubmittedAt: seal.AuditAt},
		ExamID: target.ExamID, SittingID: target.SittingID, ClassID: target.ClassID, CandidateUserID: target.CandidateUserID,
		ParticipationID: target.ParticipationID, Generation: target.Generation, ConnectionID: target.ConnectionID,
	}, ConnectionClosed: true}, nil
}

// sealingSittingStore answers the Sitting a receipt names.
type sealingSittingStore struct {
	store.ExamSittingStore
	sitting *model.ExamSitting
}

func (s sealingSittingStore) Get(_ context.Context, _ model.ExamID, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if s.sitting == nil || s.sitting.ID != id {
		return nil, store.NewErrNotFound("exam_sitting", id.String())
	}
	copy := *s.sitting
	return &store.ExamSittingSnapshot{Sitting: &copy}, nil
}

// sealingCandidateStore answers every candidate as a verified User a
// receipt can be addressed to.
type sealingCandidateStore struct{ store.UserStore }

func (sealingCandidateStore) Get(_ context.Context, id model.UserID) (*model.User, error) {
	user := &model.User{Email: "candidate@example.test", DisplayName: "Candidate", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(id, sealingTestNow().Add(-time.Hour))
	return user, nil
}
