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
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Content opens and stages the bytes behind Exam Resources, Starter
// Workspace objects and Attempt Workspace objects. The Store decides which
// object a candidate or manager may reach; this port only moves bytes.
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

// SubmissionMailPreparation contains only the safe identity needed to freeze
// a candidate receipt. The preparer resolves recipient and published Exam
// title without exposing them to the Attempt use cases.
type SubmissionMailPreparation struct {
	CandidateUserID model.UserID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	SubmissionID    model.SubmissionID
	ReceiptCode     model.SubmissionReceiptCode
	SealedAt        time.Time
	Provenance      model.ExamSubmissionProvenance
}

// PreparedSubmissionMail is a frozen receipt notice together with the
// recipient User revision it was prepared against, which the Store operation
// that seals the Submission receives as its fence.
type PreparedSubmissionMail struct {
	Notice                    *store.PreparedMail
	ExpectedRecipientRevision int64
}

// SubmissionMailPreparer prepares the candidate's Submission receipt.
// SubmissionReceiptMail satisfies it and tests substitute a fake.
type SubmissionMailPreparer interface {
	PrepareSubmissionReceipt(context.Context, SubmissionMailPreparation) (*PreparedSubmissionMail, error)
}

// DesktopBuildResolution is the Desktop build resolved for a Principal and
// the Desktop compatibility policy revision it was resolved under.
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

// DesktopBuildResolver maps a Principal, or the build identity a native
// delivery source declares, to a known Desktop build.
type DesktopBuildResolver interface {
	ResolveNativeDeliveryBuild(context.Context, NativeDeliveryBuildIdentity) (model.DesktopBuildTuple, error)
	ResolveAttemptDesktopBuild(context.Context, model.Principal) (DesktopBuildResolution, error)
}

// Dependencies are the collaborators New validates once and distributes to
// the Service.
type Dependencies struct {
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
	Mail                SubmissionMailPreparer
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
	// and its dispatch Job that a fresh voluntary, automatic or manager seal
	// commits with the Submission.
	NewDomainEvent func() model.DomainEventID
	NewJob         func() model.JobID
	Operational    OperationalRecorder
	// EnvironmentWaker wakes the owner of an Attempt's execution environment
	// after a candidate changes its Workspace. It is optional: without one,
	// the owner's backstop finds the change.
	EnvironmentWaker EnvironmentWaker
	// EnvironmentSettler brings an execution environment's last guest
	// changes into the Workspace before a voluntary, manager or automatic
	// seal. It is optional: without one, nothing is settled.
	EnvironmentSettler EnvironmentSettler
	// Environments are the Attempt Environments a manager replaces. It is
	// optional: without it, no Attempt has one.
	Environments store.AttemptEnvironmentStore
}

// EnvironmentSettler settles an Attempt's execution environment before its
// seal: the owner harvests every change the guest made. Settle asks for a
// settle and returns once it is done or its bounded budget is spent;
// AwaitPendingSettle asks for none and waits, within what is left of its
// budget, for the settle a closing Sitting already asked. An incomplete
// settle is recorded as an execution gap, never returned as a refusal, so
// the seal always goes ahead. An Attempt without an environment, or with
// nothing pending, settles at once.
type EnvironmentSettler interface {
	Settle(ctx context.Context, attemptID model.ExamAttemptID)
	AwaitPendingSettle(ctx context.Context, attemptID model.ExamAttemptID)
}

// EnvironmentWaker wakes the owner of an Attempt's execution environment,
// wherever it runs. It never blocks.
type EnvironmentWaker interface {
	Wake(attemptID model.ExamAttemptID)
}

// OperationalRecorder receives the outcome of each use case the module
// observes. It is optional; a module built without one records nothing.
type OperationalRecorder interface {
	RecordOperational(subsystem, event string, err error)
}

// Service holds every Attempt use case, from admission to the sealed
// Submission. liveControls and deliveryControls are the per-node admission
// lanes that rate-limit control commands by User.
type Service struct {
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
	// auditor brackets each audited mutation and read a Principal makes,
	// and builds the event a Store operation inserts with its change.
	auditor access.MutationAuditAdapter
	// systemAuditor audits trusted core work that has no Principal: periodic
	// continuity enforcement records no actor, and an execution environment
	// writing a candidate's Workspace names that candidate. Neither
	// manufactures a User Principal.
	systemAuditor access.SystemAuditAdapter
	publisher     realtime.Publisher
	content       Content
	mail          SubmissionMailPreparer
	desktopBuilds DesktopBuildResolver
	now           func() time.Time
	operational   OperationalRecorder
	environments  EnvironmentWaker
	settler       EnvironmentSettler
	// environmentStore holds the Attempt Environments; nil when none exist.
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

// New builds the Service, or fails when a required collaborator is missing.
// Operational is the only optional one.
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
		deps.Mail == nil ||
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
