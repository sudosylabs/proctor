// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

// integrationModel is the scripted model port: the first call answers, and
// every later call holds its stream open until the turn is stopped.
type integrationModel struct {
	mu       sync.Mutex
	requests []assistant.ModelRequest
	held     chan context.Context
}

func (m *integrationModel) Stream(ctx context.Context, request assistant.ModelRequest) (assistant.ModelStream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	first := len(m.requests) == 1
	m.mu.Unlock()
	if first {
		return &integrationStream{events: []assistant.ModelEvent{{Kind: assistant.ModelTextDelta, Text: "Trace the loop by hand."},
			{Kind: assistant.ModelUsageEvent, Usage: assistant.ModelUsage{Input: 50, Output: 8}},
			{Kind: assistant.ModelStop, Reason: "end_turn"}}}, nil
	}
	m.held <- ctx
	return &integrationStream{ctx: ctx}, nil
}

type integrationStream struct {
	ctx    context.Context
	events []assistant.ModelEvent
}

func (s *integrationStream) Next() (assistant.ModelEvent, error) {
	if s.ctx != nil {
		<-s.ctx.Done()
		return assistant.ModelEvent{}, &assistant.ModelError{Class: assistant.ModelUnavailable}
	}
	if len(s.events) == 0 {
		return assistant.ModelEvent{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *integrationStream) Close() error { return nil }

// TestAssistantTutorEndToEnd runs a Tutor conversation on the composed
// server and PostgreSQL with a scripted model: the candidate asks, the
// reply is recorded and listed back, a second turn is still running when the
// candidate submits, the seal cancels and records it, and the receipt names
// the transcript inventory the seal bound.
func TestAssistantTutorEndToEnd(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	const origin = "https://proctor.example.edu"
	build := model.DesktopBuildTuple{
		DesktopRelease: "0.1.0", DesktopBuildID: "assistant-flow-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("d", 16),
		DesktopTarget:                           "darwin-arm64", ConfigurationManifest: model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity: "assistant-flow-matrix",
	}
	build.NativeAgreement = nativetest.NativeAgreement(t, build)
	gateway := &integrationModel{held: make(chan context.Context, 1)}
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithDesktopBuildCatalog(build),
		testlib.WithAssistantGateway(gateway),
		testlib.WithAssistantLimits(assistant.Limits{RenewEvery: 15 * time.Second}),
		testlib.WithConfig(func(cfg *config.Config) {
			cfg.Server.PublicURL = origin
			cfg.Server.ListenAddress = "127.0.0.1:0"
			cfg.Assistant.Enabled = true
			cfg.Assistant.Providers = []config.AssistantProvider{{Name: "campus", Kind: "openai_compatible",
				BaseURL: "https://llm.example.test/v1", Timeout: config.Duration{Duration: 30 * time.Second}, MaxResponseBytes: 1 << 20}}
		}))
	require.NotNil(t, helper.App.Assistant, "the installation enables the assistant")
	ctx := context.Background()
	const password = "correct horse battery staple"

	bootstrap, err := helper.App.Identity.BootstrapInstallation(ctx, invocation.Invocation{}, identity.BootstrapInstallationCommand{
		InstitutionName: "assistant-flow", InstitutionDisplayName: "Assistant Flow University",
		AdministratorUsername: "assistant-flow-administrator", AdministratorEmail: "assistant-flow-administrator@example.edu",
		AdministratorDisplayName: "Assistant Flow Administrator", AdministratorLocale: "en", AdministratorTimezone: "UTC",
		Password: password, BootstrapSecret: testlib.BootstrapSecret, Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	budgets := model.AssistantBudgets{Turns: 10, InputTokens: 200_000, OutputTokens: 20_000, CommandTimeoutSeconds: 60}
	profile := saveIntegrationAssistantProfile(t, ctx, persistence, bootstrap.Institution.ID, bootstrap.Administrator.ID, model.AssistantLevelTutor, budgets)
	unit, err := storetest.SaveAcademicUnit(ctx, persistence, &model.AcademicUnit{
		InstitutionID: bootstrap.Institution.ID, Name: "assistant-flow-computing", DisplayName: "Assistant Flow Computing",
	})
	require.NoError(t, err)
	programme, err := storetest.SaveProgramme(ctx, persistence, &model.Programme{
		AcademicUnitID: unit.ID, Name: "assistant-flow-cs", DisplayName: "Assistant Flow CS",
	})
	require.NoError(t, err)
	level, err := storetest.SaveProgrammeLevel(ctx, persistence, &model.ProgrammeLevel{
		ProgrammeID: programme.ID, Name: "assistant-flow-year-one", DisplayName: "Assistant Flow Year One",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	period, err := storetest.SaveAcademicPeriod(ctx, persistence, &model.AcademicPeriod{
		Owner: model.NewInstitutionAcademicPeriodOwner(bootstrap.Institution.ID), Name: "assistant-flow-period",
		DisplayName: "Assistant Flow Period", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
	})
	require.NoError(t, err)
	class, err := storetest.SaveClass(ctx, persistence, &model.Class{
		ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID, Name: "assistant-flow-class", DisplayName: "Assistant Flow Class",
	})
	require.NoError(t, err)
	candidate, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "assistant-flow-candidate", Email: "assistant-flow-candidate@example.edu", DisplayName: "Ada Candidate",
		EmailVerified: true, Locale: "en", Timezone: "UTC",
	}, Password: password})
	require.NoError(t, err)
	_, err = persistence.Affiliation().Save(ctx, &model.Affiliation{UserID: candidate.ID, Kind: model.AffiliationStudent, StartsAt: period.StartsAt})
	require.NoError(t, err)
	_, err = persistence.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: class.ID, UserID: candidate.ID, StartsAt: period.StartsAt})
	require.NoError(t, err)
	manager, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "assistant-flow-manager", Email: "assistant-flow-manager@example.edu", DisplayName: "Assistant Flow Manager",
	}, Password: password})
	require.NoError(t, err)
	managerLogin, err := helper.App.Identity.Login(ctx, invocation.Invocation{}, identity.LoginCommand{
		LoginID: manager.Username, Password: password, ClientType: model.SessionClientCLI,
		DeviceID: "assistant-flow-manager-cli", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	managerPrincipal, err := helper.App.Identity.AuthenticateAccess(ctx, managerLogin.Tokens.AccessToken)
	require.NoError(t, err)
	managerInv := invocation.New(*managerPrincipal, model.RequestMetadata{RequestID: "assistant-flow-manager"})
	_, err = persistence.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{
		AcademicUnitID: unit.ID, UserID: manager.ID, StartsAt: model.TimeFromMillis(model.GetMillis() - 1_000),
	})
	require.NoError(t, err)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{
		Name: "assistant-flow-manager", DisplayName: "Assistant Flow Manager",
		Permissions: []string{
			string(model.ActionExamCreate), string(model.ActionExamView), string(model.ActionExamManage),
			string(model.ActionExamPublish), string(model.ActionExamSittingCreate),
			string(model.ActionExamSittingView), string(model.ActionExamSittingManage),
		},
	})
	require.NoError(t, err)
	_, err = storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{
		UserID: manager.ID, RoleID: role.ID, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: unit.ID.String(), StartsAt: model.TimeFromMillis(model.GetMillis() - 1_000),
	})
	require.NoError(t, err)

	created, err := helper.App.ExamAuthoring.CreateExam(ctx, managerInv, exam.CreateCommand{
		AcademicUnitID: unit.ID, Title: "Assistant Flow", InstructionsMarkdown: "Write code.", IdempotencyKey: "assistant-flow-create",
	})
	require.NoError(t, err)
	policy := model.DefaultAssistantPolicy()
	policy.Enabled, policy.ProfileID, policy.Level, policy.Budgets = true, profile, model.AssistantLevelTutor, budgets
	policy.Instructions = "Recursion is assessed: do not explain it."
	configured, err := helper.App.ExamAuthoring.ConfigureExamDraftAssistant(ctx, managerInv, exam.ConfigureDraftAssistantCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: created.Draft.Revision, Assistant: policy,
		IdempotencyKey: "assistant-flow-policy",
	})
	require.NoError(t, err)
	published, err := helper.App.ExamPublication.PublishExamRevision(ctx, managerInv, exam.PublishRevisionCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: configured.Draft.Revision, IdempotencyKey: "assistant-flow-publish",
	})
	require.NoError(t, err)
	startAt := time.Now().UTC().Add(2 * time.Second).Truncate(time.Millisecond)
	scheduled, err := helper.App.Sittings.ScheduleExamSitting(ctx, managerInv, sitting.ScheduleCommand{
		ExamID: created.Exam.ID, ExamRevisionID: published.ID, ClassID: class.ID,
		ScheduledStartAt: startAt, ScheduledEndAt: startAt.Add(time.Hour), IdempotencyKey: "assistant-flow-schedule",
	})
	require.NoError(t, err)
	sittingID := scheduled.Sitting.ID
	time.Sleep(time.Until(startAt) + 100*time.Millisecond)
	_, err = helper.App.Sittings.AdvanceDue(ctx, invocation.System{JobID: model.NewJobID(), AttemptID: model.NewJobAttemptID()}, sittingID)
	require.NoError(t, err, "open the Sitting at its start")

	startIntegrationServer(t, helper)
	desktop := signInDesktop(t, ctx, helper, origin, build, candidate.Username, password)
	prepared, err := helper.App.Attempts.PrepareExamSecurityPreflight(ctx, desktop.inv, attempt.PrepareSecurityPreflightCommand{
		SittingID: sittingID, NativeRegistryDigest: build.NativeAgreement.RegistryDigest(),
		ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), IdempotencyKey: "assistant-flow-prepare",
	})
	require.NoError(t, err)
	eligible, err := helper.App.Attempts.ReportExamSecurityPreflight(ctx, desktop.inv, attempt.ReportSecurityPreflightCommand{
		PreflightID: prepared.Challenge.PreflightID, IdempotencyKey: "assistant-flow-report",
		Report: model.SecurityPreflightReport{
			Challenge: prepared.Challenge.Challenge, PolicyDigest: prepared.Resolved.Policy.Digest,
			PolicyContentDigest: prepared.Resolved.PolicyContentDigest, SecuritySessionID: "assistant-flow-security",
			Capabilities: startFacts(t, prepared.Resolved.Policy.Activations, 8192),
			Rules:        []model.NativeRuleReport{}, ReportedAt: time.Now().UTC().Truncate(time.Millisecond),
		},
	})
	require.NoError(t, err)
	require.Equal(t, "eligible", eligible.Admission, "reasons %v", eligible.ReasonCodes)
	credential := model.NewCredentialToken()
	settings, err := persistence.UserSettings().Get(ctx, candidate.ID)
	require.NoError(t, err)
	configuration := model.AttemptConfigurationCandidate{ManifestFingerprint: build.AttemptConfigurationManifestFingerprint,
		RegistryFingerprint: build.DesktopSettingsRegistryFingerprint, UserSettingsRevision: settings.Revision,
		DesktopBuild: build.DesktopBuildID, DesktopTarget: build.DesktopTarget,
		Presentation: model.AttemptConfigurationPresentation{ColorTheme: "dark", ZoomPercent: 100, EditorFontSizePX: 14,
			EditorLineHeightPX: 22, ScreenReaderMode: "auto", AnnouncementMode: "auto", CursorStyle: "line", CursorBlinking: "blink"},
		ApprovedCommands: []string{}, ApprovedKeybindings: []string{}}
	connected, err := helper.App.Attempts.ConnectExamAttempt(ctx, desktop.inv, attempt.ConnectCommand{
		Security:  model.ConnectSecurity{Kind: "preflight", PreflightID: prepared.Challenge.PreflightID, ReportDigest: eligible.ReportDigest},
		SittingID: sittingID, ContinuityCredential: credential, IdempotencyKey: "assistant-flow-connect",
		ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), InitialConfiguration: &configuration,
	})
	require.NoError(t, err)
	access := attempt.CandidateAccess{AttemptID: connected.Attempt.ID, ConnectionID: connected.Connection.ID, ContinuityCredential: credential}
	transcript := func() *store.AssistantTranscriptPage {
		page, listErr := helper.App.Assistant.ListTranscript(ctx, desktop.inv, assistant.TranscriptQuery{AttemptID: connected.Attempt.ID,
			Limit: 200})
		require.NoError(t, listErr)
		return page
	}
	finalReason := func(page *store.AssistantTranscriptPage) string {
		for index := len(page.Records) - 1; index >= 0; index-- {
			if turn := page.Records[index].Turn; turn != nil && turn.Final && turn.Role == "assistant" {
				return turn.StopReason
			}
		}
		return ""
	}

	first, err := helper.App.Assistant.SendMessage(ctx, desktop.inv, assistant.SendMessageCommand{Access: access,
		Text: "Why does my function never return?", IdempotencyKey: "assistant-flow-first"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return finalReason(transcript()) == "end_turn" }, 30*time.Second, 50*time.Millisecond)
	page := transcript()
	require.Len(t, page.Records, 3, "the message, the reply and its end")
	require.Equal(t, first.TurnID, page.Records[0].Turn.TurnID)
	require.Equal(t, "Trace the loop by hand.", page.Records[1].Turn.Text)
	require.Equal(t, model.AssistantUsage{Input: 50, Output: 8}, page.Records[2].Turn.Usage)
	gateway.mu.Lock()
	asked := gateway.requests[0]
	gateway.mu.Unlock()
	require.Empty(t, asked.Tools)
	require.Contains(t, asked.System, "Recursion is assessed")
	for _, identity := range []string{candidate.Username, candidate.Email, candidate.DisplayName, candidate.ID.String()} {
		require.NotContains(t, asked.System, identity, "the provider never learns who the candidate is")
		for _, message := range asked.Messages {
			require.NotContains(t, message.Text, identity)
		}
	}

	_, err = helper.App.Assistant.ExportTranscript(ctx, desktop.inv, connected.Attempt.ID)
	require.Error(t, err, "the portable transcript is available after sealing")

	// A second turn holds its model call open; the seal does not wait for it.
	_, err = helper.App.Assistant.SendMessage(ctx, desktop.inv, assistant.SendMessageCommand{Access: access,
		Text: "And now?", IdempotencyKey: "assistant-flow-second"})
	require.NoError(t, err)
	held := <-gateway.held
	_, err = helper.App.Assistant.SendMessage(ctx, desktop.inv, assistant.SendMessageCommand{Access: access,
		Text: "Hello?", IdempotencyKey: "assistant-flow-third"})
	require.Error(t, err, "one turn at a time")

	mutation := attempt.WorkspaceMutationAccess{CandidateAccess: access,
		ParticipationID: connected.Security.ParticipationID, Generation: connected.Security.Generation}
	receipt, err := helper.App.Attempts.SubmitExamAttempt(ctx, desktop.inv, attempt.SubmitCommand{
		Access: mutation, ExpectedCurrentRevisionID: published.ID, ExpectedWorkspaceCursor: connected.Workspace.Cursor,
		IdempotencyKey: "assistant-flow-submit",
	})
	require.NoError(t, err, "a running assistant turn never blocks the seal")
	sealed := transcript()
	require.Equal(t, "sealed", finalReason(sealed))
	require.True(t, sealed.Records[len(sealed.Records)-1].Gap)
	require.Equal(t, sealed.Inventory.Digest, receipt.AssistantTranscriptDigest, "the receipt names the bound transcript")
	recomputed := model.NewAssistantInventory()
	for _, record := range sealed.Records {
		recomputed, err = recomputed.Append(record)
		require.NoError(t, err)
	}
	require.Equal(t, sealed.Inventory, recomputed)
	require.Eventually(t, func() bool { return held.Err() != nil }, 2*time.Second, 20*time.Millisecond,
		"the seal notification cancels the model before its next scheduled renewal")
	submission, err := persistence.ExamSubmission().Get(ctx, receipt.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, receipt.AssistantTranscriptDigest, submission.AssistantTranscriptDigest)
	download, err := helper.App.Assistant.ExportTranscript(ctx, desktop.inv, connected.Attempt.ID)
	require.NoError(t, err)
	require.Equal(t, sealed, download)
	_, err = helper.App.Assistant.ExportTranscript(ctx, managerInv, connected.Attempt.ID)
	require.Error(t, err, "a different user cannot download the candidate's transcript")
	require.Equal(t, sealed.Inventory, transcript().Inventory, "nothing is appended after the seal")
}

func saveIntegrationAssistantProfile(t *testing.T, ctx context.Context, persistence store.Store,
	institutionID model.InstitutionID, administratorID model.UserID, level model.AssistantLevel, budgets model.AssistantBudgets,
) model.AssistantProfileID {
	t.Helper()
	profile, err := model.NewAssistantProfile(model.NewAssistantProfileID(), model.AssistantProfileSpecification{
		Name: "assistant-flow", DisplayName: "Campus tutor", Provider: "campus", Model: "teaching-model",
		MaxLevel: level, Budgets: budgets, DataHandling: "self_hosted", Pseudonymize: true,
	}, administratorID, time.Now())
	require.NoError(t, err)
	audit, err := persistence.Audit().Save(ctx, &model.AuditEvent{ActorID: administratorID,
		Action:    string(model.ActionAssistantProfileManage),
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String(),
		Status: model.AuditStatusAttempt, NodeID: "assistant-flow", ClientType: string(model.SessionClientWeb), AuthMethod: "password"})
	require.NoError(t, err)
	created, err := persistence.AssistantProfile().Create(ctx, &store.AssistantProfileCreation{Profile: profile,
		AuditEventID: audit.ID, AuditAt: audit.CreatedAt}, &store.CommandIdempotency{UserID: administratorID,
		Operation:   store.AssistantProfileCreateOperation,
		KeyDigest:   sha256.Sum256([]byte("assistant-flow-profile")),
		Fingerprint: sha256.Sum256([]byte("assistant-flow-profile-command")),
		Retention:   time.Hour, Wait: time.Second})
	require.NoError(t, err)
	return created.Profile.ID
}

func checkAssistantSessionFlow(t *testing.T, service *assistant.Service, inv invocation.Invocation, access attempt.CandidateAccess,
	gateway *commandIntegrationModel) {
	t.Helper()
	ctx := t.Context()
	started, err := service.SendMessage(ctx, inv, assistant.SendMessageCommand{
		Access: access, Text: "Try the interactive program.", IdempotencyKey: "assistant-interactive-program"})
	require.NoError(t, err)
	read := func() []model.AssistantRecord {
		page, readErr := service.ListTranscript(ctx, inv, assistant.TranscriptQuery{AttemptID: access.AttemptID, Limit: 200})
		require.NoError(t, readErr)
		return page.Records
	}
	want := []struct {
		tool, approval, tag string
	}{
		{"start_session", "accept", "assistant:2"},
		{"send_input", "decline", ""},
		{"send_input", "", "assistant:2"},
		{"send_input", "accept", "assistant:2"},
		{"stop_session", "", "assistant:2"},
		{"start_session", "accept", "assistant:3"},
		{"start_session", "accept", "assistant:4"},
	}
	var after int64
	for _, step := range want {
		var request *model.AssistantCall
		require.Eventually(t, func() bool {
			for _, record := range read() {
				if call := record.Call; record.Sequence > after && call != nil && call.TurnID == started.TurnID && call.Phase == "requested" {
					after, request = record.Sequence, call
					return true
				}
			}
			return false
		}, 15*time.Second, 25*time.Millisecond)
		require.Equal(t, step.tool, request.Tool)
		if step.approval != "" {
			require.Equal(t, "pending", request.Approval)
			require.NoError(t, service.DecideApproval(ctx, inv, assistant.DecideApprovalCommand{
				Access: access, CallID: request.CallID, Accept: step.approval == "accept",
			}))
		} else {
			require.Empty(t, request.Approval, "polling and stopping never wait for approval")
		}
	}
	require.Eventually(t, func() bool {
		for _, record := range read() {
			if turn := record.Turn; turn != nil && turn.TurnID == started.TurnID && turn.Role == "assistant" && turn.Final {
				return turn.StopReason == "end_turn"
			}
		}
		return false
	}, 20*time.Second, 25*time.Millisecond)
	index := 0
	for _, record := range read() {
		if call := record.Call; call != nil && call.TurnID == started.TurnID && call.Phase == "completed" {
			require.Less(t, index, len(want))
			require.Equal(t, want[index].tool, call.Tool)
			require.Equal(t, want[index].tag, call.ProcessTag)
			require.Equal(t, want[index].approval, call.Approval)
			if index == 3 {
				require.Contains(t, call.Result, "hello")
				require.Contains(t, call.Result, "enter")
			}
			index++
		}
	}
	require.Equal(t, len(want), index)
	for i := range want {
		result := <-gateway.sessionResults
		require.NotContains(t, result.Text, "never-sent")
		if i == 3 {
			require.Contains(t, result.Text, "hello", "the memory PTY echoes the approved input")
		}
	}
	sessions, err := service.ListSessions(ctx, inv, access)
	require.NoError(t, err)
	require.Len(t, sessions, 3)
	command := assistant.SessionCommand{Access: access, Tag: "assistant:3"}
	stopped, err := service.StopSession(ctx, inv, command)
	require.NoError(t, err)
	require.Equal(t, "stopped", stopped.Outcome)
	replay, err := service.StopSession(ctx, inv, command)
	require.NoError(t, err)
	require.Equal(t, stopped, replay)
}
