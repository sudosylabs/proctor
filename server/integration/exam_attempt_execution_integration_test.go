// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/memory"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/assistant"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

// executionHostAuthority is a throwaway certificate authority, so the
// configured host passes validation; the test never dials it.
func executionHostAuthority(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "execution test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// integrationBodies hands a batch its bodies in order.
type integrationBodies struct{ bodies [][]byte }

func (b *integrationBodies) Next(context.Context) (io.Reader, error) {
	if len(b.bodies) == 0 {
		return nil, io.EOF
	}
	body := b.bodies[0]
	b.bodies = b.bodies[1:]
	return bytes.NewReader(body), nil
}

// TestAttemptEnvironmentEndToEnd runs the core execution runtime on the
// composed server and PostgreSQL, with an in-memory execenv host: an Exam
// with an execution policy is published, the candidate connects, the
// environment is placed and the starter file reaches the guest, a guest file
// is harvested by the environment author, an editor save reaches the guest,
// default ignore patterns keep a dependency install off the record, a
// command typed into a terminal is in the command log with the terminal as
// its origin and its input is recorded, a lost
// machine gives a new incarnation and a gap, the submit settles a guest
// change made just before it into the Submission, and the Submission revokes
// the machine.
func TestAttemptEnvironmentEndToEnd(t *testing.T) {
	runAttemptEnvironmentEndToEnd(t, false)
}

func TestAssistantSessionsEndOnConnectionCloseBetweenTurns(t *testing.T) {
	runAttemptEnvironmentEndToEnd(t, true)
}

func runAttemptEnvironmentEndToEnd(t *testing.T, disconnect bool) {
	t.Helper()
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	const origin = "https://proctor.example.edu"
	build := model.DesktopBuildTuple{
		DesktopRelease: "0.1.0", DesktopBuildID: "execution-flow-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("c", 16),
		DesktopTarget:                           "darwin-arm64", ConfigurationManifest: model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity: "execution-flow-matrix",
	}
	build.NativeAgreement = nativetest.NativeAgreement(t, build)
	guests, err := memory.New(memory.Config{Images: []execenv.Image{"python-3.12"}, WorkDir: t.TempDir()})
	require.NoError(t, err)
	gateway := &commandIntegrationModel{results: make(chan assistant.ModelMessage, 3), sessionResults: make(chan assistant.ModelMessage, 7)}
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithDesktopBuildCatalog(build),
		testlib.WithAssistantGateway(gateway),
		testlib.WithAssistantLimits(assistant.Limits{ApprovalTimeout: 5 * time.Second}),
		testlib.WithExecutionHostDial(func(context.Context, config.ExecutionHost) (execenv.Host, error) { return guests, nil }),
		testlib.WithConfig(func(cfg *config.Config) {
			cfg.Server.PublicURL = origin
			cfg.Server.ListenAddress = "127.0.0.1:0"
			cfg.Assistant.Enabled = true
			cfg.Assistant.Providers = []config.AssistantProvider{{Name: "campus", Kind: "openai_compatible",
				BaseURL: "https://llm.example.test/v1", Timeout: config.Duration{Duration: 30 * time.Second}, MaxResponseBytes: 1 << 20}}
			cfg.Execution.Enabled = true
			cfg.Execution.Hosts = []config.ExecutionHost{{Name: "alpha", Address: "alpha.example.test:7443",
				ServerName: "alpha.example.test", CAPEM: executionHostAuthority(t), Token: "execution-test-token"}}
		}))
	ctx := context.Background()
	const password = "correct horse battery staple"

	bootstrap, err := helper.App.Identity.BootstrapInstallation(ctx, invocation.Invocation{}, identity.BootstrapInstallationCommand{
		InstitutionName: "execution-flow", InstitutionDisplayName: "Execution Flow University",
		AdministratorUsername: "execution-flow-administrator", AdministratorEmail: "execution-flow-administrator@example.edu",
		AdministratorDisplayName: "Execution Flow Administrator", AdministratorLocale: "en", AdministratorTimezone: "UTC",
		Password: password, BootstrapSecret: testlib.BootstrapSecret, Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	profile := saveIntegrationEnvironmentProfile(t, ctx, persistence, bootstrap.Institution.ID, bootstrap.Administrator.ID)
	unit, err := storetest.SaveAcademicUnit(ctx, persistence, &model.AcademicUnit{
		InstitutionID: bootstrap.Institution.ID, Name: "execution-flow-computing", DisplayName: "Execution Flow Computing",
	})
	require.NoError(t, err)
	programme, err := storetest.SaveProgramme(ctx, persistence, &model.Programme{
		AcademicUnitID: unit.ID, Name: "execution-flow-cs", DisplayName: "Execution Flow CS",
	})
	require.NoError(t, err)
	level, err := storetest.SaveProgrammeLevel(ctx, persistence, &model.ProgrammeLevel{
		ProgrammeID: programme.ID, Name: "execution-flow-year-one", DisplayName: "Execution Flow Year One",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	period, err := storetest.SaveAcademicPeriod(ctx, persistence, &model.AcademicPeriod{
		Owner: model.NewInstitutionAcademicPeriodOwner(bootstrap.Institution.ID), Name: "execution-flow-period",
		DisplayName: "Execution Flow Period", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
	})
	require.NoError(t, err)
	class, err := storetest.SaveClass(ctx, persistence, &model.Class{
		ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID, Name: "execution-flow-class", DisplayName: "Execution Flow Class",
	})
	require.NoError(t, err)
	candidate, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "execution-flow-candidate", Email: "execution-flow-candidate@example.edu", DisplayName: "Execution Flow Candidate",
		EmailVerified: true, Locale: "en", Timezone: "UTC",
	}, Password: password})
	require.NoError(t, err)
	_, err = persistence.SeedAffiliation(ctx, &model.Affiliation{UserID: candidate.ID, Kind: model.AffiliationStudent, StartsAt: period.StartsAt})
	require.NoError(t, err)
	_, err = persistence.SeedClassEnrollment(ctx, &model.ClassMember{ClassID: class.ID, UserID: candidate.ID, StartsAt: period.StartsAt})
	require.NoError(t, err)
	manager, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "execution-flow-manager", Email: "execution-flow-manager@example.edu", DisplayName: "Execution Flow Manager",
	}, Password: password})
	require.NoError(t, err)
	managerLogin, err := helper.App.Identity.Login(ctx, invocation.Invocation{}, identity.LoginCommand{
		LoginID: manager.Username, Password: password, ClientType: model.SessionClientCLI,
		DeviceID: "execution-flow-manager-cli", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	managerPrincipal, err := helper.App.Identity.AuthenticateAccess(ctx, managerLogin.Tokens.AccessToken)
	require.NoError(t, err)
	managerInv := invocation.New(*managerPrincipal, model.RequestMetadata{RequestID: "execution-flow-manager"})
	_, err = persistence.SeedAcademicUnitMember(ctx, &model.AcademicUnitMember{
		AcademicUnitID: unit.ID, UserID: manager.ID, StartsAt: model.TimeFromMillis(model.GetMillis() - 1_000),
	})
	require.NoError(t, err)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{
		Name: "execution-flow-manager", DisplayName: "Execution Flow Manager",
		Permissions: []string{
			string(model.ActionExamCreate), string(model.ActionExamView), string(model.ActionExamManage),
			string(model.ActionExamPublish), string(model.ActionExamSittingCreate),
			string(model.ActionExamSittingView), string(model.ActionExamSittingManage), string(model.ActionSubmissionView),
		},
	})
	require.NoError(t, err)
	_, err = storetest.SaveRoleBinding(ctx, persistence, &model.RoleBinding{
		UserID: manager.ID, RoleID: role.ID, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: unit.ID.String(), StartsAt: model.TimeFromMillis(model.GetMillis() - 1_000),
	})
	require.NoError(t, err)

	created, err := helper.App.ExamAuthoring.CreateExam(ctx, managerInv, exam.CreateCommand{
		AcademicUnitID: unit.ID, Title: "Execution Flow", InstructionsMarkdown: "Write code.", IdempotencyKey: "execution-flow-create",
	})
	require.NoError(t, err)
	starter := []byte("print('starter')\n")
	starterSum := sha256.Sum256(starter)
	file, err := helper.App.StarterWorkspaces.CreateExamStarterWorkspaceFile(ctx, managerInv, workspace.CreateFileCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: created.Draft.Revision, Path: "main.py", MediaType: "text/x-python",
		ExpectedSHA256: hex.EncodeToString(starterSum[:]), Body: bytes.NewReader(starter), Size: int64(len(starter)),
		IdempotencyKey: "execution-flow-starter",
	})
	require.NoError(t, err)
	previewServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "execution preview")
	}))
	t.Cleanup(previewServer.Close)
	listener, ok := previewServer.Listener.(*net.TCPListener)
	require.True(t, ok)
	address, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	previewPort := address.Port
	configured, err := helper.App.ExamAuthoring.ConfigureExamDraftExecution(ctx, managerInv, exam.ConfigureDraftExecutionCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: file.DraftRevision,
		Execution: model.ExecutionPolicy{Enabled: true, Profile: profile, Terminals: 1, RecordInput: true,
			Preview: model.PreviewPolicy{Enabled: true, Ports: []model.PortRange{{From: previewPort, To: previewPort}}}},
		IdempotencyKey: "execution-flow-policy",
	})
	require.NoError(t, err)
	budgets := model.AssistantBudgets{Turns: 6, InputTokens: 500_000, OutputTokens: 24_000, ToolCalls: 16,
		Commands: 6, CommandSeconds: 180, CommandTimeoutSeconds: 30}
	assistantProfile := saveIntegrationAssistantProfile(t, ctx, persistence, bootstrap.Institution.ID, bootstrap.Administrator.ID, model.AssistantLevelPair, budgets)
	policy := model.DefaultAssistantPolicy()
	policy.Enabled, policy.ProfileID, policy.Level, policy.Budgets = true, assistantProfile, model.AssistantLevelPair, budgets
	configuredAssistant, err := helper.App.ExamAuthoring.ConfigureExamDraftAssistant(ctx, managerInv, exam.ConfigureDraftAssistantCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: configured.Draft.Revision, Assistant: policy,
		IdempotencyKey: "execution-flow-assistant-policy",
	})
	require.NoError(t, err)
	published, err := helper.App.ExamPublication.PublishExamRevision(ctx, managerInv, exam.PublishRevisionCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: configuredAssistant.Draft.Revision, IdempotencyKey: "execution-flow-publish",
	})
	require.NoError(t, err)
	startAt := time.Now().UTC().Add(2 * time.Second).Truncate(time.Millisecond)
	scheduled, err := helper.App.Sittings.ScheduleExamSitting(ctx, managerInv, sitting.ScheduleCommand{
		ExamID: created.Exam.ID, ExamRevisionID: published.ID, ClassID: class.ID,
		ScheduledStartAt: startAt, ScheduledEndAt: startAt.Add(time.Hour), IdempotencyKey: "execution-flow-schedule",
	})
	require.NoError(t, err)
	sittingID := scheduled.Sitting.ID
	storetest.WaitUntilPassed(t, ctx, persistence, startAt)
	_, err = helper.App.Sittings.AdvanceDue(ctx, invocation.System{JobID: model.NewJobID(), AttemptID: model.NewJobAttemptID()}, sittingID)
	require.NoError(t, err, "open the Sitting at its start")

	// The running server delivers domain events and owns environments.
	startIntegrationServer(t, helper)
	desktop := signInDesktop(t, ctx, helper, origin, build, candidate.Username, password)
	prepared, err := helper.App.Attempts.PrepareExamSecurityPreflight(ctx, desktop.inv, attempt.PrepareSecurityPreflightCommand{
		SittingID: sittingID, NativeRegistryDigest: build.NativeAgreement.RegistryDigest(),
		ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), IdempotencyKey: "execution-flow-prepare",
	})
	require.NoError(t, err)
	eligible, err := helper.App.Attempts.ReportExamSecurityPreflight(ctx, desktop.inv, attempt.ReportSecurityPreflightCommand{
		PreflightID: prepared.Challenge.PreflightID, IdempotencyKey: "execution-flow-report",
		Report: model.SecurityPreflightReport{
			Challenge: prepared.Challenge.Challenge, PolicyDigest: prepared.Resolved.Policy.Digest,
			PolicyContentDigest: prepared.Resolved.PolicyContentDigest, SecuritySessionID: "execution-flow-security",
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
		SittingID: sittingID, ContinuityCredential: credential, IdempotencyKey: "execution-flow-connect",
		ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), InitialConfiguration: &configuration,
	})
	require.NoError(t, err)
	access := attempt.CandidateAccess{AttemptID: connected.Attempt.ID, ConnectionID: connected.Connection.ID, ContinuityCredential: credential}
	stopRenewal := renewExecutionTestParticipation(t, helper.App.Attempts, desktop.inv, access, connected)

	t.Cleanup(func() {
		if t.Failed() {
			if environment, getErr := persistence.AttemptEnvironment().Get(context.Background(), connected.Attempt.ID); getErr == nil {
				t.Logf("environment: %+v", *environment)
			}
			for _, line := range strings.Split(helper.Logs.String(), "\n") {
				if strings.Contains(line, "environment") || strings.Contains(line, "execution") {
					t.Log(line)
				}
			}
		}
	})
	eventually := func(condition func() bool, message string) {
		t.Helper()
		require.Eventually(t, condition, 30*time.Second, 50*time.Millisecond, message)
	}
	readGuest := func(id execenv.ID, name string) string {
		body, readErr := guests.ReadGuest(ctx, id, name)
		if readErr != nil {
			return ""
		}
		return string(body)
	}
	journal := func() *attempt.WorkspaceJournalPage {
		page, journalErr := helper.App.Attempts.ListCandidateExamWorkspaceJournal(ctx, desktop.inv, attempt.WorkspaceJournalQuery{
			Access: access, AfterCursor: 0, Limit: 200,
		})
		require.NoError(t, journalErr)
		return &page
	}
	harvested := func(name string) (model.AttemptWorkspaceJournalEntry, bool) {
		var found model.AttemptWorkspaceJournalEntry
		ok := false
		for _, entry := range journal().Entries {
			if entry.NewPath == name {
				found, ok = entry, true
			}
		}
		return found, ok
	}

	// The participation started event creates the environment, and its
	// owner places it and projects the Attempt Workspace into it.
	grantID := execenv.ID(connected.Attempt.ID.String() + ".0")
	eventually(func() bool { return readGuest(grantID, "main.py") == string(starter) }, "the starter file reaches the guest")

	t.Run("assistant command approvals and attribution", func(t *testing.T) {
		checkAssistantCommandFlow(t, helper.App.Assistant, desktop.inv, access, gateway, journal)
	})

	// The guest writes a file: the core stores it as the candidate's, by the
	// environment author.
	require.NoError(t, guests.WriteGuest(ctx, grantID, "answer.py", []byte("print(42)\n")))
	eventually(func() bool { _, ok := harvested("answer.py"); return ok }, "the guest file is harvested")
	entry, _ := harvested("answer.py")
	require.Equal(t, model.AttemptWorkspaceAuthorEnvironment, entry.Author.Kind)

	// The editor saves a file: it reaches the guest.
	mutation := attempt.WorkspaceMutationAccess{CandidateAccess: access,
		ParticipationID: connected.Security.ParticipationID, Generation: connected.Security.Generation}
	saved := []byte("print('editor')\n")
	savedSum := sha256.Sum256(saved)
	_, err = helper.App.Attempts.ApplyCandidateExamWorkspaceBatch(ctx, desktop.inv, attempt.ApplyWorkspaceBatchCommand{
		Access: mutation, IdempotencyKey: "execution-flow-editor",
		Mutations: []attempt.WorkspaceBatchMutation{{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: "editor.py",
			MediaType: "text/x-python", SHA256: hex.EncodeToString(savedSum[:]), Size: int64(len(saved))}},
		Content: &integrationBodies{bodies: [][]byte{saved}},
	})
	require.NoError(t, err)
	eventually(func() bool { return readGuest(grantID, "editor.py") == string(saved) }, "the editor's save reaches the guest")

	// The candidate opens a terminal and types a command into it, from
	// whichever node serves the request.
	terminalAccess := environment.CandidateAccess{AttemptID: access.AttemptID, ConnectionID: access.ConnectionID,
		ContinuityCredential: access.ContinuityCredential}
	const previewRequest = "GET / HTTP/1.0\r\nHost: localhost\r\n\r\n"
	t.Run("preview relays a candidate request through the execution host", func(t *testing.T) {
		guest, previewErr := guests.Ensure(ctx, execenv.Spec{ID: grantID, Image: "python-3.12", Network: execenv.NetworkNone,
			Resources:   execenv.Resources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30},
			IdleTimeout: 10 * time.Minute, IdleAction: execenv.IdleSuspend})
		require.NoError(t, previewErr)
		require.NoError(t, guests.RegisterPortListener(guest, listener))
		ports, previewErr := helper.App.Previews.ListPreviewPorts(ctx, desktop.inv, terminalAccess)
		require.NoError(t, previewErr)
		require.Equal(t, []environment.PreviewPort{{ListeningPort: environment.ListeningPort{Port: previewPort,
			Address: address.IP.String()}, Allowed: true}}, ports)
		previewCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, previewErr := helper.App.Previews.OpenPreview(previewCtx, desktop.inv, terminalAccess, previewPort)
		require.NoError(t, previewErr)
		defer func() { _ = conn.Close() }()
		_, previewErr = io.WriteString(conn, previewRequest)
		require.NoError(t, previewErr)
		response, previewErr := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, previewErr)
		defer func() { _ = response.Body.Close() }()
		require.Equal(t, http.StatusOK, response.StatusCode)
		body, previewErr := io.ReadAll(response.Body)
		require.NoError(t, previewErr)
		require.Equal(t, "execution preview", string(body))
	})
	opened, err := helper.App.Terminals.OpenTerminal(ctx, desktop.inv, terminalAccess, environment.Window{})
	require.NoError(t, err)
	require.Equal(t, model.AttemptEnvironmentProcessTag("terminal:1"), opened.Tag)
	_, err = helper.App.Terminals.OpenTerminal(ctx, desktop.inv, terminalAccess, environment.Window{})
	require.True(t, apperror.Is(err, "exam.attempt.terminal_limit_reached"), "the policy allows one terminal: %v", err)
	session, err := helper.App.Terminals.AttachTerminal(ctx, desktop.inv, terminalAccess, opened.Tag)
	require.NoError(t, err)
	output, err := session.Output(ctx, 0)
	require.NoError(t, err)
	typed := []byte("echo terminal-ran\n")
	next, err := session.Write(ctx, 0, typed)
	require.NoError(t, err)
	require.EqualValues(t, len(typed), next)
	var shown []byte
	for !strings.Contains(string(shown), "terminal-ran\r\n") {
		event, nextErr := output.Next(ctx)
		require.NoError(t, nextErr)
		shown = append(shown, event.Data...)
	}
	// A file a terminal command writes is journaled by that terminal.
	redirect := []byte("echo hi > tagged.txt\n")
	_, err = session.Write(ctx, next, redirect)
	require.NoError(t, err)
	require.NoError(t, output.Close())
	session.Close(ctx)
	eventually(func() bool { _, ok := harvested("tagged.txt"); return ok }, "the terminal's file is harvested")
	tagged, _ := harvested("tagged.txt")
	require.Equal(t, model.NewEnvironmentWorkspaceAuthor(0, "terminal:1"), tagged.Author, "the first incarnation's terminal wrote it")
	eventually(func() bool {
		row, getErr := persistence.AttemptEnvironment().Get(ctx, connected.Attempt.ID)
		return getErr == nil && row.ProcessCursor >= 3
	}, "the owner stores the terminal's command log")

	// Default ignore patterns keep a dependency install in the guest.
	require.NoError(t, guests.MkdirGuest(ctx, grantID, "node_modules"))
	require.NoError(t, guests.WriteGuest(ctx, grantID, "node_modules/dep.js", []byte("module.exports = 1\n")))
	require.NoError(t, guests.WriteGuest(ctx, grantID, "after.py", []byte("pass\n")))
	eventually(func() bool { _, ok := harvested("after.py"); return ok }, "a later file is harvested")
	_, ignored := harvested("node_modules")
	require.False(t, ignored, "an ignored directory is not harvested")

	// The host loses the machine: a new incarnation is projected from the
	// Attempt Workspace.
	require.True(t, guests.Lose(grantID))
	restarted := execenv.ID(connected.Attempt.ID.String() + ".1")
	eventually(func() bool { return readGuest(restarted, "answer.py") == "print(42)\n" }, "the new incarnation holds the harvested file")

	t.Run("interactive assistant sessions", func(t *testing.T) {
		checkAssistantSessionFlow(t, helper.App.Assistant, desktop.inv, access, gateway, !disconnect)
	})

	if disconnect {
		stopRenewal()
		closed, closeErr := helper.App.Attempts.CloseExamAttemptConnection(ctx, desktop.inv, attempt.CloseConnectionCommand{
			AttemptID: access.AttemptID, SittingID: sittingID, ClassID: connected.ClassID,
			ConnectionID: access.ConnectionID, Reason: model.AttemptConnectionCloseTransport,
		})
		require.NoError(t, closeErr)
		require.True(t, closed.Changed)
		require.Eventually(t, func() bool {
			page, readErr := helper.App.Assistant.ListTranscript(ctx, desktop.inv, assistant.TranscriptQuery{AttemptID: access.AttemptID, Limit: 200})
			if readErr != nil {
				return false
			}
			killed := map[string]bool{}
			for _, record := range page.Records {
				call := record.Call
				if call != nil && call.Phase == "session_ended" && call.Outcome == "ended" && call.Signal == "KILL" && call.ExitCode == nil {
					killed[call.ProcessTag] = true
				}
			}
			return killed["assistant:3"] && killed["assistant:4"]
		}, 10*time.Second, 25*time.Millisecond)
		row, getErr := persistence.AttemptEnvironment().Get(ctx, access.AttemptID)
		require.NoError(t, getErr)
		require.Equal(t, model.AttemptEnvironmentDesiredRunning, row.Desired, "closing the connection did not end the participation")
		return
	}

	// The candidate submits with the cursor they last saw, right after the
	// guest wrote once more: the settle brings that write in, and the seal
	// takes it though it is past the candidate's cursor.
	seen := journal().CurrentCursor
	require.NoError(t, guests.WriteGuest(ctx, restarted, "last.py", []byte("print('last')\n")))
	stopRenewal()
	submitted, err := helper.App.Attempts.SubmitExamAttempt(ctx, desktop.inv, attempt.SubmitCommand{
		Access: mutation, ExpectedCurrentRevisionID: published.ID, ExpectedWorkspaceCursor: seen,
		IdempotencyKey: "execution-flow-submit",
	})
	require.NoError(t, err, "the environment's changes do not fence the candidate's seal")
	require.Greater(t, submitted.WorkspaceCursor, seen, "the settled guest write is sealed")
	eventually(func() bool {
		report, readyErr := guests.Ready(ctx)
		require.NoError(t, readyErr)
		return report.Grants == 0
	}, "the Submission revokes the machine")

	sealed, err := helper.App.Assistant.ExportTranscript(ctx, desktop.inv, connected.Attempt.ID)
	require.NoError(t, err)
	require.Equal(t, submitted.AssistantTranscriptDigest, sealed.Inventory.Digest)
	recomputed := model.NewAssistantInventory()
	var sealedSession *model.AssistantCall
	for _, record := range sealed.Records {
		recomputed, err = recomputed.Append(record)
		require.NoError(t, err)
		if call := record.Call; call != nil && call.Phase == "session_ended" && call.ProcessTag == "assistant:4" {
			sealedSession = call
		}
	}
	require.Equal(t, submitted.AssistantTranscriptDigest, recomputed.Digest)
	require.NotNil(t, sealedSession)
	require.Equal(t, "sealed", sealedSession.Outcome)

	usage, err := helper.App.Reviews.ListPreviewUsage(ctx, managerInv, submitted.SubmissionID)
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.Equal(t, previewPort, usage[0].Port)
	require.EqualValues(t, 1, usage[0].Connections)
	require.EqualValues(t, len(previewRequest), usage[0].BytesIn)
	require.Greater(t, usage[0].BytesOut, int64(len("execution preview")))
	require.Equal(t, usage[0].FirstUsedAt, usage[0].LastUsedAt)

	evidence, err := persistence.ExamIntegrityReview().ListAttemptEvidence(ctx, store.AttemptEvidenceListOptions{
		SubmissionID: submitted.SubmissionID, Limit: 100,
	})
	require.NoError(t, err)
	restartGaps, terminalCommands, terminalInputs := 0, 0, 0
	for _, item := range evidence.Items {
		switch item.Kind {
		case model.AttemptEvidenceProcess:
			var detail struct {
				Events []struct {
					Kind   string   `json:"kind"`
					Argv   []string `json:"argv"`
					Origin string   `json:"origin"`
				} `json:"events"`
			}
			require.NoError(t, json.Unmarshal(item.Detail, &detail))
			for _, event := range detail.Events {
				if event.Kind == "started" && len(event.Argv) > 1 && event.Argv[1] == "terminal-ran" {
					terminalCommands++
					require.Equal(t, "terminal:1", event.Origin, "the command log names the terminal")
				}
			}
		case model.AttemptEvidenceInput:
			terminalInputs++
			require.Equal(t, model.NewEnvironmentWorkspaceAuthor(0, "terminal:1"), item.Author)
			require.EqualValues(t, 1, item.Sequence, "numbered by the input offset")
			require.NotContains(t, string(item.Detail), "terminal-ran\r\n", "output is never recorded")
		}
		require.NotContains(t, string(item.Detail), "settle_incomplete", "the settle finished in time")
		if item.Gap() && strings.Contains(string(item.Detail), `"gap":"environment_restarted"`) {
			restartGaps++
			require.Equal(t, int64(1), mustIncarnation(t, item.Author), "the new incarnation records its restart")
		}
	}
	require.Equal(t, 1, restartGaps, "the restart is one gap")
	require.Equal(t, 1, terminalCommands, "the typed command is in the sealed command log")
	require.Equal(t, 1, terminalInputs, "the typed input is recorded once")
}

// mustIncarnation reads the incarnation an environment author names.
func mustIncarnation(t *testing.T, author model.AttemptWorkspaceAuthor) int64 {
	t.Helper()
	require.Equal(t, model.AttemptWorkspaceAuthorEnvironment, author.Kind)
	var incarnation int64
	for _, digit := range strings.SplitN(author.Ref, "/", 2)[0] {
		incarnation = incarnation*10 + int64(digit-'0')
	}
	return incarnation
}

// saveIntegrationEnvironmentProfile creates a profile as the installation's
// System Administrator through the Store.
func saveIntegrationEnvironmentProfile(t *testing.T, ctx context.Context, persistence store.Store,
	institutionID model.InstitutionID, administratorID model.UserID,
) model.EnvironmentProfileID {
	t.Helper()
	profile, err := model.NewEnvironmentProfile(model.NewEnvironmentProfileID(), model.EnvironmentProfileSpecification{
		Name: "execution-flow", Image: "python-3.12",
		Resources: model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30},
		Network:   model.EnvironmentNetworkNone, IdleWindow: 10 * time.Minute, MaxTerminals: 2,
	}, administratorID, time.Now())
	require.NoError(t, err)
	audit, err := persistence.Audit().Save(ctx, &model.AuditEvent{ActorID: administratorID,
		Action:    string(model.ActionEnvironmentProfileManage),
		Resource:  model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		ScopeType: model.RoleScopeInstitution, ScopeID: institutionID.String(),
		Status: model.AuditStatusAttempt, NodeID: "execution-flow", ClientType: string(model.SessionClientWeb), AuthMethod: "password"})
	require.NoError(t, err)
	created, err := persistence.EnvironmentProfile().Create(ctx, &store.EnvironmentProfileCreation{Profile: profile,
		AuditEventID: audit.ID, AuditAt: audit.CreatedAt}, &store.CommandIdempotency{UserID: administratorID,
		Operation:   store.EnvironmentProfileCreateOperation,
		KeyDigest:   sha256.Sum256([]byte("execution-flow-profile")),
		Fingerprint: sha256.Sum256([]byte("execution-flow-profile-command")),
		Retention:   time.Hour, Wait: time.Second})
	require.NoError(t, err)
	return created.Profile.ID
}

func renewExecutionTestParticipation(t *testing.T, service *attempt.Service, inv invocation.Invocation,
	access attempt.CandidateAccess, connected attempt.ConnectionResult) func() {
	t.Helper()
	// The integration flow can outlast the initial lease on a busy runner.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var renewErr error
	go func() {
		defer close(done)
		tick := time.NewTicker(4 * time.Second)
		defer tick.Stop()
		security := connected.Security
		for seq := int64(1); ; seq++ {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			_, err := service.RenewExamAttemptParticipation(ctx, inv, attempt.RenewParticipationCommand{
				Security: model.SecurityControl{ControlSequence: seq, PolicyDigest: security.Policy.Digest,
					SecuritySessionID: security.SecuritySessionID, StreamID: security.DeliveryStreamID, DeliveryWatermarks: []model.DeliveryWatermark{}},
				AttemptID: access.AttemptID, ParticipationID: security.ParticipationID, ConnectionID: access.ConnectionID,
				Generation: security.Generation, Sequence: seq, ContinuityCredential: access.ContinuityCredential,
			})
			if err != nil {
				if ctx.Err() == nil {
					renewErr = err
				}
				return
			}
		}
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return func() {
		t.Helper()
		stop()
		require.NoError(t, renewErr)
	}
}
