// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Content opens and stages the bytes behind exam resources and starter and
// attempt workspace objects. It never checks access: the store decides which
// object a candidate or manager may reach.
type Content interface {
	OpenExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error)
	OpenStarterWorkspaceObject(context.Context, model.StarterWorkspaceObjectID) (io.ReadCloser, error)
	StageAttemptWorkspaceObject(
		context.Context,
		model.AttemptWorkspaceObjectID,
		io.Reader,
		int64,
		string,
	) (*model.AttemptWorkspaceContent, error)
	OpenAttemptWorkspaceObject(context.Context, model.AttemptWorkspaceObjectID) (io.ReadCloser, error)
	// StageScreenClipChunk stores one chunk of a screen clip if its bytes are
	// the given size and match the given hash.
	StageScreenClipChunk(context.Context, model.ScreenClipID, int, io.Reader, int64, string) error
	// OpenScreenClipChunk opens length bytes of one stored chunk from offset.
	OpenScreenClipChunk(context.Context, model.ScreenClipID, int, int64, int64) (io.ReadCloser, error)
}

// SubmissionMailPreparation carries only the safe identity needed to freeze a
// candidate receipt; the preparer resolves the recipient and exam title so the
// attempt use cases never see them.
type SubmissionMailPreparation struct {
	CandidateUserID model.UserID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	SubmissionID    model.SubmissionID
	ReceiptCode     model.SubmissionReceiptCode
	SealedAt        time.Time
	Provenance      model.ExamSubmissionProvenance
}

// PreparedSubmissionMail is a frozen receipt notice and the recipient's user
// revision it was prepared against, which the sealing store operation uses as
// its fence.
type PreparedSubmissionMail struct {
	Notice                    *store.PreparedMail
	ExpectedRecipientRevision int64
}

// DesktopBuildResolution is the Desktop build resolved for a principal and the
// compatibility policy revision it was resolved under.
type DesktopBuildResolution struct {
	Build                       model.DesktopBuildTuple
	CompatibilityPolicyRevision int64
}

// NativeDeliveryBuildIdentity is the release and compatibility matrix a
// native delivery source declares for itself.
type NativeDeliveryBuildIdentity struct {
	ReleaseID    string
	MatrixID     string
	MatrixDigest string
	TargetTuple  string
}

// DesktopBuildResolver maps a principal, or the build identity a native
// delivery source declares, to a known Desktop build.
type DesktopBuildResolver interface {
	ResolveNativeDeliveryBuild(context.Context, NativeDeliveryBuildIdentity) (model.DesktopBuildTuple, error)
	ResolveAttemptDesktopBuild(context.Context, model.Principal) (DesktopBuildResolution, error)
}

// Dependencies are the collaborators New validates once.
type Dependencies struct {
	AssistantSessions   store.AssistantTranscriptStore
	Persistence         store.ExamAttemptStore
	FocusLoss           store.ExamFocusLossStore
	SecurityPreflight   store.SecurityPreflightStore
	NativeDelivery      store.NativeDeliveryStore
	BrowserDelivery     store.BrowserDeliveryStore
	DeliveryBudget      store.DeliveryBudgetStore
	Workspace           store.ExamAttemptWorkspaceStore
	Submissions         store.ExamSubmissionStore
	Sittings            store.ExamSittingStore
	Managers            SittingManagerAuthorizer
	Auditor             *access.Auditor
	Publisher           realtime.Publisher
	Content             Content
	Mail                SubmissionReceiptMail
	DesktopBuilds       DesktopBuildResolver
	Now                 func() time.Time
	NewAttemptID        func() model.ExamAttemptID
	NewWorkspaceID      func() model.ExamAttemptWorkspaceID
	NewParticipation    func() model.AttemptParticipationID
	NewConnection       func() model.AttemptConnectionID
	NewEvidence         func() model.IntegrityEvidenceID
	NewFlag             func() model.IntegrityFlagID
	NewSuspension       func() model.AttemptSuspensionID
	NewFocusLossSignal  func() model.FocusLossSignalID
	NewDiscrepancy      func() model.IntegrityDiscrepancyID
	NewWorkspaceEntry   func() model.AttemptWorkspaceEntryID
	NewWorkspaceObject  func() model.AttemptWorkspaceObjectID
	NewWorkspaceVersion func() model.WorkspaceContentVersion
	NewSubmission       func() model.SubmissionID
	NewReceiptCode      func() model.SubmissionReceiptCode
	// NewDomainEvent and NewJob propose the exam.attempt.submitted outbox row
	// and its dispatch job, committed with every fresh seal.
	NewDomainEvent func() model.DomainEventID
	NewJob         func() model.JobID
	Operational    operational.Recorder
	// EnvironmentWaker is optional: without one, the environment owner's
	// backstop finds a candidate's workspace change.
	EnvironmentWaker EnvironmentWaker
	// EnvironmentSettler is optional: without one, nothing is settled before
	// a seal.
	EnvironmentSettler EnvironmentSettler
	// Environments is optional: without it, no attempt has an environment.
	Environments store.AttemptEnvironmentStore
}

// EnvironmentSettler harvests the guest's changes into an attempt's workspace
// before its seal. Settle returns once the settle is done or its bounded budget
// is spent; AwaitPendingSettle only waits, within what is left of the budget,
// for a settle a closing sitting already asked for. An incomplete settle is
// recorded as an execution gap, never returned as a refusal, so the seal always
// goes ahead.
type EnvironmentSettler interface {
	Settle(ctx context.Context, attemptID model.ExamAttemptID)
	AwaitPendingSettle(ctx context.Context, attemptID model.ExamAttemptID)
}

// EnvironmentWaker wakes the owner of an attempt's execution environment,
// wherever it runs. It never blocks.
type EnvironmentWaker interface {
	Wake(attemptID model.ExamAttemptID)
}

// Service holds every attempt use case. liveControls and deliveryControls
// rate-limit control commands by user, per node.
type Service struct {
	assistantSessions store.AssistantTranscriptStore
	attempts          store.ExamAttemptStore
	focusLoss         store.ExamFocusLossStore
	securityPreflight store.SecurityPreflightStore
	nativeDelivery    store.NativeDeliveryStore
	browserDelivery   store.BrowserDeliveryStore
	deliveryBudget    store.DeliveryBudgetStore
	workspaces        store.ExamAttemptWorkspaceStore
	submissions       store.ExamSubmissionStore

	sittings store.ExamSittingStore
	managers SittingManagerAuthorizer
	auditor  access.MutationAuditAdapter
	// systemAuditor audits trusted work that has no principal: continuity
	// enforcement records no actor, and an environment writing a candidate's
	// workspace names that candidate. Neither manufactures a user principal.
	systemAuditor access.SystemAuditAdapter
	publisher     realtime.Publisher
	content       Content
	mail          SubmissionReceiptMail
	desktopBuilds DesktopBuildResolver
	now           func() time.Time
	operational   operational.Recorder
	environments  EnvironmentWaker
	settler       EnvironmentSettler
	// environmentStore is nil when no attempt has an environment.
	environmentStore store.AttemptEnvironmentStore

	newAttemptID         func() model.ExamAttemptID
	newWorkspaceID       func() model.ExamAttemptWorkspaceID
	newParticipationID   func() model.AttemptParticipationID
	newConnectionID      func() model.AttemptConnectionID
	newEvidenceID        func() model.IntegrityEvidenceID
	newFlagID            func() model.IntegrityFlagID
	newSuspensionID      func() model.AttemptSuspensionID
	newFocusLossSignalID func() model.FocusLossSignalID
	newDiscrepancyID     func() model.IntegrityDiscrepancyID
	newWorkspaceEntryID  func() model.AttemptWorkspaceEntryID
	newWorkspaceObjectID func() model.AttemptWorkspaceObjectID
	newWorkspaceVersion  func() model.WorkspaceContentVersion
	newSubmissionID      func() model.SubmissionID
	newReceiptCode       func() model.SubmissionReceiptCode
	newDomainEventID     func() model.DomainEventID
	newJobID             func() model.JobID

	liveControls     controlAdmission
	deliveryControls controlAdmission
	workspaceBudget  *workspaceWriteBudget
}

// New fails when a required collaborator is missing. Operational and the
// three environment collaborators are optional.
func New(deps Dependencies) (*Service, error) {
	if deps.Persistence == nil ||
		deps.FocusLoss == nil ||
		deps.SecurityPreflight == nil ||
		deps.NativeDelivery == nil ||
		deps.BrowserDelivery == nil ||
		deps.DeliveryBudget == nil ||
		deps.Workspace == nil ||
		deps.Submissions == nil ||
		deps.Sittings == nil ||
		deps.Managers.Sittings == nil ||
		deps.Auditor == nil ||
		deps.Publisher == nil ||
		deps.Content == nil ||
		deps.Mail.Composer == nil ||
		deps.Mail.Users == nil ||
		deps.Mail.Sittings == nil ||
		deps.Mail.Revisions == nil ||
		deps.DesktopBuilds == nil ||
		deps.Now == nil ||
		deps.NewAttemptID == nil ||
		deps.NewWorkspaceID == nil ||
		deps.NewParticipation == nil ||
		deps.NewConnection == nil ||
		deps.NewEvidence == nil ||
		deps.NewFlag == nil ||
		deps.NewSuspension == nil ||
		deps.NewFocusLossSignal == nil ||
		deps.NewDiscrepancy == nil ||
		deps.NewWorkspaceEntry == nil ||
		deps.NewWorkspaceObject == nil ||
		deps.NewWorkspaceVersion == nil ||
		deps.NewSubmission == nil ||
		deps.NewReceiptCode == nil ||
		deps.NewDomainEvent == nil ||
		deps.NewJob == nil {
		return nil, errors.New("Exam Attempt dependencies are required")
	}

	return &Service{
		assistantSessions:    deps.AssistantSessions,
		attempts:             deps.Persistence,
		focusLoss:            deps.FocusLoss,
		securityPreflight:    deps.SecurityPreflight,
		nativeDelivery:       deps.NativeDelivery,
		browserDelivery:      deps.BrowserDelivery,
		deliveryBudget:       deps.DeliveryBudget,
		workspaces:           deps.Workspace,
		submissions:          deps.Submissions,
		sittings:             deps.Sittings,
		managers:             deps.Managers,
		auditor:              deps.Auditor.MutationAudit(),
		systemAuditor:        deps.Auditor.SystemAudit(),
		publisher:            deps.Publisher,
		content:              deps.Content,
		mail:                 deps.Mail,
		desktopBuilds:        deps.DesktopBuilds,
		now:                  deps.Now,
		operational:          deps.Operational,
		environments:         deps.EnvironmentWaker,
		settler:              deps.EnvironmentSettler,
		environmentStore:     deps.Environments,
		newAttemptID:         deps.NewAttemptID,
		newWorkspaceID:       deps.NewWorkspaceID,
		newParticipationID:   deps.NewParticipation,
		newConnectionID:      deps.NewConnection,
		newEvidenceID:        deps.NewEvidence,
		newFlagID:            deps.NewFlag,
		newSuspensionID:      deps.NewSuspension,
		newFocusLossSignalID: deps.NewFocusLossSignal,
		newDiscrepancyID:     deps.NewDiscrepancy,
		newWorkspaceEntryID:  deps.NewWorkspaceEntry,
		newWorkspaceObjectID: deps.NewWorkspaceObject,
		newWorkspaceVersion:  deps.NewWorkspaceVersion,
		newSubmissionID:      deps.NewSubmission,
		newReceiptCode:       deps.NewReceiptCode,
		newDomainEventID:     deps.NewDomainEvent,
		newJobID:             deps.NewJob,
		liveControls:         controlAdmission{lane: liveControlLane},
		deliveryControls:     controlAdmission{lane: deliveryControlLane},
		workspaceBudget:      newWorkspaceWriteBudget(workspaceWriteRateFor),
	}, nil
}
