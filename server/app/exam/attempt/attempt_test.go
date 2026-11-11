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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestManagerAttemptOperationsAuthorizeBeforeReadingAttempts(t *testing.T) {
	t.Parallel()
	denied := apperror.New("authorization.denied")
	// Each operation records only its authorization decision before refusing.
	operations := map[string]struct {
		authorization string
		run           func(*fixture, model.ExamAttemptID) error
	}{
		"get": {authorization: "manager.authorize", run: func(f *fixture, attemptID model.ExamAttemptID) error {
			_, err := f.service.GetExamAttempt(context.Background(), f.inv, f.sitting.ExamID, f.sitting.ID, attemptID)
			return err
		}},
		"reallow": {authorization: "manager.manage", run: func(f *fixture, attemptID model.ExamAttemptID) error {
			_, err := f.service.ReallowExamAttempt(context.Background(), f.inv, ReallowCommand{
				ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: attemptID, SuspensionID: model.NewAttemptSuspensionID(),
				ExpectedAttemptRevision: 2, PrivateReason: "probe", IdempotencyKey: "probe-key"})
			return err
		}},
		"end": {authorization: "manager.manage", run: func(f *fixture, attemptID model.ExamAttemptID) error {
			_, err := f.service.EndExamAttemptByManager(context.Background(), f.inv, ManagerEndCommand{
				ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: attemptID,
				ExpectedAttemptRevision: 2, PrivateReason: "probe", IdempotencyKey: "probe-key"})
			return err
		}},
		"suspend": {authorization: "manager.manage", run: func(f *fixture, attemptID model.ExamAttemptID) error {
			_, err := f.service.SuspendExamAttempt(context.Background(), f.inv, ManualSuspensionCommand{
				ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: attemptID,
				ExpectedAttemptRevision: 2, PrivateReason: "probe", IdempotencyKey: "probe-key"})
			return err
		}},
		"browser_activity": {authorization: "browser.authorize", run: func(f *fixture, attemptID model.ExamAttemptID) error {
			_, err := f.service.ListExamAttemptBrowserActivity(context.Background(), f.inv, BrowserActivityPageQuery{
				ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: attemptID, Limit: 10})
			return err
		}},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var codes []string
			for _, existing := range []bool{true, false} {
				f := newFixture(t)
				useManagerCall(f)
				f.managerErr = denied
				f.persistence.attempt, f.persistence.managerSuspension = managerAttemptFixture(t, f, model.ExamAttemptSuspended, 2,
					model.NewAttemptSuspensionID())
				attemptID := f.attemptID
				if !existing {
					attemptID = model.NewExamAttemptID()
				}
				err := operation.run(f, attemptID)
				var fault *apperror.Error
				require.ErrorAs(t, err, &fault, "existing=%t error", existing)
				codes = append(codes, fault.Code())
				require.Equal(t, []string{operation.authorization}, f.order, "existing=%t denied caller reached Attempt state", existing)
				require.Nil(t, f.audit.values, "existing=%t denied caller reached Attempt state: order=%v audit", existing, f.order)
			}
			require.Equal(t, codes[1], codes[0])
			require.Equal(t, "resource.not_found", codes[0])
		})
	}
}

func TestContentSelectorsPreserveCandidateAccessErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	invalidCall := invocation.New(model.Principal{}, model.RequestMetadata{})
	candidateAccess := CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken()}
	for _, open := range []func() error{
		func() error {
			_, err := f.service.OpenCandidateExamResource(context.Background(), invalidCall, OpenResourceQuery{Access: candidateAccess, ResourceID: model.NewExamResourceID()})
			return err
		},
		func() error {
			_, err := f.service.OpenCandidateExamWorkspaceFile(context.Background(), invalidCall, OpenWorkspaceFileQuery{Access: candidateAccess, EntryID: model.NewAttemptWorkspaceEntryID()})
			return err
		},
	} {
		var fault *apperror.Error
		require.ErrorAs(t, open(), &fault)
		require.Equal(t, "authentication.token_invalid", fault.Code())
	}
}

type fixture struct {
	service                   *Service
	inv                       invocation.Invocation
	userID                    model.UserID
	attemptID                 model.ExamAttemptID
	connectionID              model.AttemptConnectionID
	at                        time.Time
	sitting                   *model.ExamSitting
	revision                  *model.ExamRevision
	order                     []string
	persistence               *attemptStoreFake
	workspace                 *attemptWorkspaceStoreFake
	submissions               *submissionStoreFake
	mail                      *submissionMailFake
	audit                     *auditFake
	systemAudit               *systemAuditFake
	publisher                 *realtimetest.Publisher
	content                   *contentFake
	managerOverride           bool
	managerErr                error
	focusSignalID             model.FocusLossSignalID
	focusFlagID               model.IntegrityFlagID
	discrepancyID             model.IntegrityDiscrepancyID
	submissionID              model.SubmissionID
	receiptCode               model.SubmissionReceiptCode
	submissionAuthorizationID model.SubmissionID
	configuration             model.AttemptConfiguration
	desktopBuild              model.DesktopBuildTuple
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{userID: model.NewUserID(), attemptID: model.NewExamAttemptID(), connectionID: model.NewAttemptConnectionID(),
		at: time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)}
	f.sitting = &model.ExamSitting{ID: model.NewExamSittingID(), ExamID: model.NewExamID(), ExamRevisionID: model.NewExamRevisionID(),
		ClassID: model.NewClassID(), State: model.ExamSittingOpen, ScheduledStartAt: f.at.Add(-time.Hour), ScheduledEndAt: f.at.Add(time.Hour),
		CreatedAt: f.at.Add(-2 * time.Hour), UpdatedAt: f.at, Revision: 2}
	f.revision = &model.ExamRevision{ID: f.sitting.ExamRevisionID, ExamID: f.sitting.ExamID, StarterWorkspace: []model.ExamRevisionStarterWorkspaceEntry{
		{EntryID: model.NewStarterWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryDirectory, Path: "cmd"},
		{EntryID: model.NewStarterWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile, Path: "cmd/main.go", ObjectID: model.NewStarterWorkspaceObjectID(),
			ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: 4, SHA256: strings.Repeat("a", 64)},
	}}
	f.persistence = &attemptStoreFake{f: f, firstAdmission: true, connectionOpened: true}
	f.workspace = &attemptWorkspaceStoreFake{f: f}
	f.submissions = &submissionStoreFake{f: f}
	f.mail = &submissionMailFake{f: f}
	f.audit = &auditFake{f: f}
	f.systemAudit = &systemAuditFake{f: f}
	f.publisher = publisherIn(&f.order)
	f.content = &contentFake{f: f}
	f.desktopBuild = model.DesktopBuildTuple{DesktopRelease: "1.0.0", DesktopBuildID: "attempt-test",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16), DesktopTarget: "darwin-arm64", ConfigurationManifest: model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity: "attempt-test-matrix"}
	f.desktopBuild.NativeAgreement = syntheticNativeAgreement(t, f.desktopBuild)
	var configurationErr error
	f.configuration, configurationErr = (model.AttemptConfigurationCandidate{ManifestFingerprint: f.desktopBuild.AttemptConfigurationManifestFingerprint, RegistryFingerprint: f.desktopBuild.DesktopSettingsRegistryFingerprint, UserSettingsRevision: model.NewUserSettingsRevision(), DesktopBuild: f.desktopBuild.DesktopBuildID, DesktopTarget: f.desktopBuild.TargetTuple(), Presentation: model.AttemptConfigurationPresentation{ColorTheme: "dark", ZoomPercent: 100, EditorFontSizePX: 14, EditorLineHeightPX: 22, ScreenReaderMode: "auto", AnnouncementMode: "auto", CursorStyle: "line", CursorBlinking: "blink"}, ApprovedCommands: []string{}, ApprovedKeybindings: []string{}}).Freeze(model.NewID())
	require.NoError(t, configurationErr)
	service, err := New(Dependencies{
		Persistence: f.persistence, FocusLoss: f.persistence, SecurityPreflight: f.persistence, NativeDelivery: f.persistence,
		BrowserDelivery: f.persistence, DeliveryBudget: f.persistence, Workspace: f.workspace, Submissions: f.submissions, Sittings: &sittingFake{f: f}, Managers: &managerFake{f: f},
		Auditor: f.audit, SystemAuditor: f.systemAudit, Publisher: f.publisher, Content: f.content, Mail: f.mail,
		DesktopBuilds: &desktopBuildResolverFake{f: f},
		Now:           func() time.Time { return f.at }, NewAttemptID: model.NewExamAttemptID, NewWorkspaceID: model.NewExamAttemptWorkspaceID,
		NewParticipation: model.NewAttemptParticipationID, NewConnection: model.NewAttemptConnectionID,
		NewEvidence: model.NewIntegrityEvidenceID, NewFlag: func() model.IntegrityFlagID {
			if f.focusFlagID.IsValid() {
				return f.focusFlagID
			}
			return model.NewIntegrityFlagID()
		}, NewSuspension: model.NewAttemptSuspensionID,
		NewFocusLossSignal: func() model.FocusLossSignalID {
			if f.focusSignalID.IsValid() {
				return f.focusSignalID
			}
			return model.NewFocusLossSignalID()
		},
		NewDiscrepancy: func() model.IntegrityDiscrepancyID {
			if f.discrepancyID.IsValid() {
				return f.discrepancyID
			}
			return model.NewIntegrityDiscrepancyID()
		},
		NewWorkspaceEntry:   func() model.AttemptWorkspaceEntryID { return entryIDOrNew(f.workspace) },
		NewWorkspaceObject:  model.NewAttemptWorkspaceObjectID,
		NewWorkspaceVersion: func() model.WorkspaceContentVersion { return workspaceVersionOrNew(f.workspace) },
		NewSubmission: func() model.SubmissionID {
			if f.submissionID.IsValid() {
				return f.submissionID
			}
			return model.NewSubmissionID()
		},
		NewReceiptCode: func() model.SubmissionReceiptCode {
			if f.receiptCode.IsValid() {
				return f.receiptCode
			}
			return model.NewSubmissionReceiptCode()
		},
	})
	require.NoError(t, err)
	f.service = service
	f.inv = invocation.New(model.Principal{UserID: f.userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, DesktopRegistrationID: model.NewDesktopRegistrationID(),
		DPoPKeyThumbprint: strings.Repeat("A", 43), RegisteredDesktopKey: true,
		DesktopRelease: "1.0.0", DesktopBuildID: "attempt-test", DesktopPlatform: model.DesktopPlatformDarwin,
		DesktopArchitecture: model.DesktopArchitectureARM64, DesktopRealtimeProtocol: 1,
		AuthenticatedAt: f.at}, model.RequestMetadata{RequestID: "attempt-test"})
	return f
}

func validConnectCommand(f *fixture, credential, key string) ConnectCommand {
	configuration := f.configuration.Clone()
	return ConnectCommand{Security: model.ConnectSecurity{Kind: "preflight", PreflightID: "fixture-preflight", ReportDigest: model.SHA256Fingerprint([]byte("fixture-report"))}, SittingID: f.sitting.ID, ContinuityCredential: credential,
		ConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		InitialConfiguration:             &configuration.AttemptConfigurationCandidate, IdempotencyKey: key}
}

func useManagerCall(f *fixture) model.UserID {
	principal := f.inv.Principal()
	principal.UserID = model.NewUserID()
	f.inv = invocation.New(principal, f.inv.RequestMetadata())
	return principal.UserID
}

func managerAttemptFixture(t *testing.T, f *fixture, state model.ExamAttemptState, revision int64,
	suspensionID model.AttemptSuspensionID,
) (*model.ExamAttempt, *store.ExamAttemptSuspensionView) {
	t.Helper()
	attempt, err := model.NewExamAttempt(f.attemptID, f.sitting.ExamID, f.sitting.ID, f.userID, f.revision.ID,
		f.at.Add(-time.Hour))
	require.NoError(t, err)
	attempt.State, attempt.UpdatedAt, attempt.Revision = state, f.at, revision
	err = attempt.Validate()
	require.NoError(t, err)
	if state != model.ExamAttemptSuspended {
		return attempt, nil
	}
	suspension := &store.ExamAttemptSuspensionView{ID: suspensionID, AttemptID: attempt.ID,
		ParticipationID: model.NewAttemptParticipationID(), FlagID: model.NewIntegrityFlagID(), Generation: 1,
		State: model.AttemptSuspensionActive, Source: model.AttemptSuspensionSourcePolicy,
		CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost, StartedAt: f.at.Add(-time.Minute)}
	return attempt, suspension
}

func runtimeCapabilitiesFixture(f *fixture, currentRevisionID model.ExamRevisionID) store.CandidateRuntimeCapabilities {
	configuration := f.configuration.Clone()
	return store.CandidateRuntimeCapabilities{SchemaVersion: 1, ServerTime: f.at,
		InteractionState: store.CandidateInteractionInteractive,
		AttemptConfiguration: store.CandidateAttemptConfiguration{Revision: configuration.Revision, Presentation: configuration.Presentation,
			ApprovedCommands: append([]string{}, configuration.ApprovedCommands...), ApprovedKeybindings: append([]string{}, configuration.ApprovedKeybindings...), Digest: configuration.Digest},
		FocusLossCollectionEnabled: true, WorkspaceMutationAllowed: true, SubmissionAllowed: true,
		Browser: store.CandidateBrowserCapability{State: store.CandidateBrowserDisabled},
		ExamRevision: store.CandidateExamRevisionCapability{AdmissionRevisionID: f.revision.ID,
			CurrentRevisionID: currentRevisionID},
		Departure: store.CandidateDepartureCapability{Allowed: false, Reason: "attempt_in_progress"}}
}

type desktopBuildResolverFake struct{ f *fixture }

func (f *desktopBuildResolverFake) ResolveAttemptDesktopBuild(context.Context, model.Principal) (DesktopBuildResolution, error) {
	return DesktopBuildResolution{Build: f.f.desktopBuild, CompatibilityPolicyRevision: 1}, nil
}

type sittingFake struct{ f *fixture }

func (f *sittingFake) Resolve(context.Context, model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	f.f.order = append(f.f.order, "sitting")
	return &store.ExamSittingSnapshot{Sitting: f.f.sitting}, nil
}

type managerFake struct{ f *fixture }

func (f *managerFake) AuthorizeSittingView(context.Context, invocation.Invocation, model.ExamSittingID) error {
	f.f.order = append(f.f.order, "manager.authorize")
	return f.f.managerErr
}
func (f *managerFake) AuthorizeSittingManage(context.Context, invocation.Invocation, model.ExamSittingID) (access.ManagerAuthorization, error) {
	f.f.order = append(f.f.order, "manager.manage")
	return access.ManagerAuthorization{Override: f.f.managerOverride}, f.f.managerErr
}
func (f *managerFake) AuthorizeSubmissionView(_ context.Context, _ invocation.Invocation, submissionID model.SubmissionID) error {
	f.f.order = append(f.f.order, "submission.authorize")
	f.f.submissionAuthorizationID = submissionID
	return nil
}
func (f *managerFake) AuthorizeBrowserActivityView(context.Context, invocation.Invocation, model.ExamSittingID) (access.BrowserActivityAuthorization, error) {
	f.f.order = append(f.f.order, "browser.authorize")
	return access.BrowserActivityAuthorization{AcademicUnitID: model.NewAcademicUnitID()}, f.f.managerErr
}

type auditFake struct {
	f          *fixture
	values     map[string]any
	failedCode string
	action     model.Action
	resource   model.Resource
	scopeType  model.RoleScopeType
	scopeID    string
	operation  string
	failErr    error
}

type systemAuditFake struct {
	f          *fixture
	values     map[string]any
	failedCode string
}

func (f *systemAuditFake) Begin(_ context.Context, _ model.Action, _ model.Resource, _ model.RoleScopeType, _, _ string, values map[string]any) (model.AuditEventID, error) {
	f.f.order = append(f.f.order, "system.audit")
	f.values = values
	return model.NewAuditEventID(), nil
}
func (f *systemAuditFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	f.f.order = append(f.f.order, "system.audit.fail")
	f.failedCode = code
	return nil
}

func (f *auditFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	f.f.order = append(f.f.order, "audit")
	f.values = attempt.Value
	f.action, f.resource, f.scopeType, f.scopeID, f.operation = attempt.Action, attempt.Resource, attempt.ScopeType, attempt.ScopeID, attempt.Operation
	return model.NewAuditEventID(), nil
}
func (f *auditFake) PrepareAtScope(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource,
	scopeType model.RoleScopeType, scopeID, operation string, values map[string]any,
) (*model.AuditEvent, error) {
	f.f.order = append(f.f.order, "audit.prepare")
	f.values = values
	f.action, f.resource, f.scopeType, f.scopeID, f.operation = action, resource, scopeType, scopeID, operation
	return &model.AuditEvent{Action: string(action), Resource: resource, ScopeType: scopeType, ScopeID: scopeID,
		Status: model.AuditStatusAttempt, NodeID: "test", Parameters: []byte(`{"operation":"` + operation + `"}`)}, nil
}
func (f *auditFake) RecordAuthorizationDecision(_ context.Context, _ invocation.Invocation, _ model.Action, _ model.Resource,
	_ model.RoleScopeType, _ string, _ bool,
) error {
	f.f.order = append(f.f.order, "audit.authorization")
	return nil
}
func (f *auditFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	f.f.order = append(f.f.order, "audit.fail")
	f.failedCode = code
	return f.failErr
}
func (f *auditFake) Complete(_ context.Context, _ model.AuditEventID, _ map[string]any) error {
	f.f.order = append(f.f.order, "audit.complete")
	return nil
}

type contentFake struct {
	f               *fixture
	starterID       model.StarterWorkspaceObjectID
	attemptObjectID model.AttemptWorkspaceObjectID
	attemptContent  *model.AttemptWorkspaceContent
	openAttemptID   model.AttemptWorkspaceObjectID
	openAttemptBody string
}

func (f *contentFake) StageAttemptWorkspaceObject(_ context.Context, id model.AttemptWorkspaceObjectID, _ io.Reader, _ int64, _ string) (*model.AttemptWorkspaceContent, error) {
	f.f.order = append(f.f.order, "content.stage")
	f.attemptObjectID = id
	return f.attemptContent, nil
}
func (f *contentFake) OpenAttemptWorkspaceObject(_ context.Context, id model.AttemptWorkspaceObjectID) (io.ReadCloser, error) {
	f.openAttemptID = id
	return io.NopCloser(strings.NewReader(f.openAttemptBody)), nil
}

func (*contentFake) OpenExamResource(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("resource")), nil
}
func (f *contentFake) OpenStarterWorkspaceObject(_ context.Context, id model.StarterWorkspaceObjectID) (io.ReadCloser, error) {
	f.starterID = id
	return io.NopCloser(strings.NewReader("workspace")), nil
}

func syntheticNativeAgreement(t *testing.T, build model.DesktopBuildTuple) *model.DesktopNativeAgreement {
	t.Helper()
	digest := model.SHA256Fingerprint([]byte("synthetic-source-schema"))
	definitions := []model.NativeCoverageDefinition{}
	matrix := model.NativeCapabilityMatrix{RegistryDigest: model.NativeRegistryDigest, MatrixID: build.CapabilityMatrixIdentity, ReleaseID: "synthetic-release", TargetTuple: build.DesktopTarget}
	for _, item := range []struct {
		key    string
		source model.NativeSourceID
	}{{"baseline.capture", model.NativeSourceCapture}, {"baseline.display", model.NativeSourceDisplay}, {"baseline.window", model.NativeSourceWindow}} {
		definitions = append(definitions, model.NativeCoverageDefinition{CoverageKey: item.key, CapabilityID: "baseline", SourceID: item.source, SourceSchemaDigest: digest, PermittedClaims: []model.NativeCapabilityClaim{model.NativeClaimEnforce}, RequiredPermissions: []string{}, Baseline: true})
		matrix.Entries = append(matrix.Entries, model.NativeCapabilityMatrixEntry{CoverageKey: item.key, SourceSchemaDigest: digest, Claim: model.NativeClaimEnforce, ComponentID: "synthetic-component", AdapterVersion: "synthetic-adapter", RequiredPermissions: []string{}, Limitations: []string{}, Verification: "passed"})
	}
	agreement, err := model.NewDesktopNativeAgreement(model.NativeRegistryDigest, model.SHA256Fingerprint([]byte("synthetic-manifest")), model.SHA256Fingerprint([]byte("synthetic-matrix")), model.SHA256Fingerprint([]byte("synthetic-detectors")), definitions, matrix, []model.NativeDetectorDefinition{})
	require.NoError(t, err)
	return agreement
}

func renewalCoverageFixture() model.SecurityCoverageRenewal {
	return model.SecurityCoverageRenewal{ControlSequence: 1, PolicyDigest: model.SHA256Fingerprint([]byte("renewal-policy")), SecuritySessionID: model.NewID(), StreamID: model.NewID(), Posture: "compliant", Sources: []model.NativeSourceCoverage{}, Coverage: []model.NativeCoverageClaim{}, SourceResets: []model.NativeSourceReset{}, DeliveryWatermarks: []model.DeliveryWatermark{}}
}
func renewalCoverageResultFixture() model.SecurityCoverageResult {
	digest := model.SHA256Fingerprint([]byte("renewal-control"))
	return model.SecurityCoverageResult{ProcessedControlSequence: 1, ProcessedControlDigest: &digest, CoverageResult: "accepted", SourceResetReceipts: []model.SourceResetReceipt{}, SecurityInteractionAllowed: true, DeliveryWatermarkRejections: []model.DeliveryWatermarkRejection{}}
}

func (f *desktopBuildResolverFake) ResolveNativeDeliveryBuild(context.Context, NativeDeliveryBuildIdentity) (model.DesktopBuildTuple, error) {
	return f.f.desktopBuild, nil
}

func disabledCandidateBrowserPolicyFixture(revisionID model.ExamRevisionID) *store.CandidateBrowserPolicy {
	policy := model.DisabledBrowserPolicy()
	digest, _ := model.BrowserPolicyDigest(policy)
	return &store.CandidateBrowserPolicy{Policy: policy, PolicyRevisionID: revisionID, PolicyRevisionNumber: 1, PolicyDigest: digest,
		BrowserActivityDisclosure: model.BrowserActivityDisclosure{NoticeID: "integrated_browser_activity", Audience: "exam_managers_with_browser_activity_permission", RetentionPolicyRevision: 1, RetentionAnchor: "eligible_closed_sitting_records_completion"}}
}

// chainHasCode reports whether any application error in err's chain carries
// code, so an assertion can see the cause a public concealment wraps.
func chainHasCode(err error, code string) bool {
	for err != nil {
		var failure *apperror.Error
		if !errors.As(err, &failure) {
			return false
		}
		if failure.Code() == code {
			return true
		}
		err = failure.Unwrap()
	}
	return false
}
