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
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Sittings reads the ownership of the Sitting an Attempt use case names.
type Sittings interface {
	// Resolve returns stable Sitting ownership metadata. Store remains the
	// authority for connect-time lifecycle, deadline, and membership checks.
	Resolve(context.Context, model.ExamSittingID) (*store.ExamSittingSnapshot, error)
}

// ManagerAuthorizer is the manager authorization the Attempt use cases ask.
// SittingManagerAuthorizer satisfies it and tests substitute a fake.
type ManagerAuthorizer interface {
	AuthorizeSittingView(context.Context, invocation.Invocation, model.ExamSittingID) error
	AuthorizeSittingManage(context.Context, invocation.Invocation, model.ExamSittingID) (access.ManagerAuthorization, error)
	AuthorizeSubmissionView(context.Context, invocation.Invocation, model.SubmissionID) error
	AuthorizeBrowserActivityView(context.Context, invocation.Invocation, model.ExamSittingID) (access.BrowserActivityAuthorization, error)
}

// Auditor is the part of *access.Auditor the Attempt use cases call. Begin
// and Fail bracket an audited mutation, Complete closes an audited read, and
// PrepareAtScope builds the event a Store operation inserts with its change,
// after RecordAuthorizationDecision has recorded the decision.
type Auditor interface {
	Begin(context.Context, access.Mutation) (model.AuditEventID, error)
	PrepareAtScope(
		context.Context,
		invocation.Invocation,
		model.Action,
		model.Resource,
		model.RoleScopeType,
		string,
		string,
		map[string]any,
	) (*model.AuditEvent, error)
	RecordAuthorizationDecision(context.Context, invocation.Invocation, model.Action, model.Resource, model.RoleScopeType, string, bool) error
	Fail(context.Context, model.AuditEventID, string) error
	Complete(context.Context, model.AuditEventID, map[string]any) error
}

// SystemAuditor creates actor-less audit attempts for trusted periodic
// continuity enforcement. A runtime scanner must never manufacture a User
// Principal merely to persist this system decision.
// access.SystemAuditAdapter satisfies it and tests substitute a fake.
type SystemAuditor interface {
	Begin(context.Context, model.Action, model.Resource, model.RoleScopeType, string, string, map[string]any) (model.AuditEventID, error)
	Fail(context.Context, model.AuditEventID, string) error
}

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
	Sittings            Sittings
	Managers            ManagerAuthorizer
	Auditor             Auditor
	SystemAuditor       SystemAuditor
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
	Operational         OperationalRecorder
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

	sittings      Sittings
	managers      ManagerAuthorizer
	auditor       Auditor
	systemAuditor SystemAuditor
	publisher     realtime.Publisher
	content       Content
	mail          SubmissionMailPreparer
	desktopBuilds DesktopBuildResolver
	now           func() time.Time
	operational   OperationalRecorder

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

	liveControls     controlAdmission
	deliveryControls controlAdmission
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
		deps.Managers == nil ||
		deps.Auditor == nil ||
		deps.SystemAuditor == nil ||
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
		deps.NewReceiptCode == nil {
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
		auditor:              deps.Auditor,
		systemAuditor:        deps.SystemAuditor,
		publisher:            deps.Publisher,
		content:              deps.Content,
		mail:                 deps.Mail,
		desktopBuilds:        deps.DesktopBuilds,
		now:                  deps.Now,
		operational:          deps.Operational,
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
		liveControls:         controlAdmission{lane: liveControlLane},
	}, nil
}
