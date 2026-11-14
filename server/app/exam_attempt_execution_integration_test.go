// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

//go:build integration

package app_test

import (
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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/memory"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/plugin/execution"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

// enabledExecution is the built-in execution plugin turned on by default,
// as if an administrator had enabled it.
type enabledExecution struct{ *execution.Plugin }

func (p enabledExecution) Manifest() plugin.Manifest {
	manifest := p.Plugin.Manifest()
	manifest.EnabledByDefault = true
	return manifest
}

func executionSecrets(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "execution test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	authority, err := json.Marshal(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	require.NoError(t, err)
	return `{"hosts":[{"name":"alpha","address":"alpha.example.test:7443","server_name":"alpha.example.test","ca_pem":` +
		string(authority) + `,"token":"execution-test-token"}]}`
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

// TestExecutionPluginEndToEnd runs the built-in execution plugin on the
// composed server and PostgreSQL, with an in-memory execenv host: an Exam
// with the execution section is published, the candidate connects, the
// grant is created and the starter file reaches the guest, a guest file is
// harvested with origin environment, an editor save reaches the guest,
// default ignore patterns keep a dependency install off the record, a
// command becomes process evidence, a lost host gives a new incarnation and
// a gap, a flush answers with the cursor the candidate submits with, and
// the Submission seals the evidence and revokes the grant.
func TestExecutionPluginEndToEnd(t *testing.T) {
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
	guests, err := memory.New(memory.Config{Images: []execenv.Image{"python-3.12"}, Slots: 2})
	require.NoError(t, err)
	terminal := execution.NewPlugin(execution.Options{Dial: func(context.Context, execution.HostConfig) (execenv.Host, error) {
		return guests, nil
	}})
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithDesktopBuildCatalog(build),
		testlib.WithPlugins(enabledExecution{terminal}),
		testlib.WithConfig(func(cfg *config.Config) {
			cfg.Server.PublicURL = origin
			cfg.Server.ListenAddress = "127.0.0.1:0"
			cfg.Plugins = map[string]config.Plugin{"execution": {Secrets: json.RawMessage(executionSecrets(t))}}
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
	_, err = persistence.Affiliation().Save(ctx, &model.Affiliation{UserID: candidate.ID, Kind: model.AffiliationStudent, StartsAt: period.StartsAt})
	require.NoError(t, err)
	_, err = persistence.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: class.ID, UserID: candidate.ID, StartsAt: period.StartsAt})
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
	_, err = persistence.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{
		AcademicUnitID: unit.ID, UserID: manager.ID, StartsAt: model.TimeFromMillis(model.GetMillis() - 1_000),
	})
	require.NoError(t, err)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{
		Name: "execution-flow-manager", DisplayName: "Execution Flow Manager",
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
	sectioned, err := helper.App.ExamAuthoring.ReplaceExamDraftPluginSection(ctx, managerInv, exam.ReplaceDraftPluginSectionCommand{
		ExamID: created.Exam.ID, PluginID: model.PluginID("execution"), ExpectedDraftRevision: file.DraftRevision,
		Section: json.RawMessage(`{"image":"python-3.12"}`), IdempotencyKey: "execution-flow-section",
	})
	require.NoError(t, err, "the plugin accepts a section whose image a usable host offers")
	published, err := helper.App.ExamPublication.PublishExamRevision(ctx, managerInv, exam.PublishRevisionCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: sectioned.Draft.Revision, IdempotencyKey: "execution-flow-publish",
	})
	require.NoError(t, err)
	require.NoError(t, err)
	startAt := time.Now().UTC().Add(2 * time.Second).Truncate(time.Millisecond)
	scheduled, err := helper.App.Sittings.ScheduleExamSitting(ctx, managerInv, sitting.ScheduleCommand{
		ExamID: created.Exam.ID, ExamRevisionID: published.ID, ClassID: class.ID,
		ScheduledStartAt: startAt, ScheduledEndAt: startAt.Add(time.Hour), IdempotencyKey: "execution-flow-schedule",
	})
	require.NoError(t, err)
	sittingID := scheduled.Sitting.ID
	time.Sleep(time.Until(startAt) + 100*time.Millisecond)
	_, err = helper.App.Sittings.AdvanceDue(ctx, invocation.System{JobID: model.NewJobID(), AttemptID: model.NewJobAttemptID()}, sittingID)
	require.NoError(t, err, "open the Sitting at its start")

	// The running server delivers domain events and runs plugin workers.
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

	t.Cleanup(func() {
		if t.Failed() {
			for _, kind := range []string{"grant", "live", "projection", "flush"} {
				records, _ := persistence.PluginRecord().List(context.Background(), store.PluginRecordListOptions{PluginID: "execution", Kind: kind, Limit: 10})
				for _, record := range records {
					t.Logf("record %s %s: %s", kind, record.Key, record.Document)
				}
			}
			for _, line := range strings.Split(helper.Logs.String(), "\n") {
				if strings.Contains(line, "plugin") || strings.Contains(line, "execution") {
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
		body, err := guests.ReadGuest(ctx, id, name)
		if err != nil {
			return ""
		}
		return string(body)
	}
	harvested := func(name string) (model.AttemptWorkspaceJournalEntry, bool) {
		page, err := helper.App.Attempts.ListCandidateExamWorkspaceJournal(ctx, desktop.inv, attempt.WorkspaceJournalQuery{
			Access: access, AfterCursor: 0, Limit: 200,
		})
		require.NoError(t, err)
		var found model.AttemptWorkspaceJournalEntry
		ok := false
		for _, entry := range page.Entries {
			if entry.NewPath == name {
				found, ok = entry, true
			}
		}
		return found, ok
	}

	// The participation started event creates the grant, and its owner
	// projects the Attempt Workspace into it.
	grantID := execenv.ID(connected.Attempt.ID.String() + ".0")
	eventually(func() bool { return readGuest(grantID, "main.py") == string(starter) }, "the starter file reaches the guest")

	// The guest writes a file: the core stores it as the candidate's, with
	// origin environment.
	require.NoError(t, guests.WriteGuest(ctx, grantID, "answer.py", []byte("print(42)\n")))
	eventually(func() bool { _, ok := harvested("answer.py"); return ok }, "the guest file is harvested")
	entry, _ := harvested("answer.py")
	require.Equal(t, model.AttemptWorkspaceOrigin("environment"), entry.Origin)

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

	// Default ignore patterns keep a dependency install on the grant.
	require.NoError(t, guests.MkdirGuest(ctx, grantID, "node_modules"))
	require.NoError(t, guests.WriteGuest(ctx, grantID, "node_modules/dep.js", []byte("module.exports = 1\n")))
	require.NoError(t, guests.WriteGuest(ctx, grantID, "after.py", []byte("pass\n")))
	eventually(func() bool { _, ok := harvested("after.py"); return ok }, "a later file is harvested")
	_, ignored := harvested("node_modules")
	require.False(t, ignored, "an ignored directory is not harvested")

	// A command in the terminal becomes process evidence.
	env, err := guests.Ensure(ctx, execenv.Spec{ID: grantID, Image: "python-3.12", Network: execenv.NetworkNone})
	require.NoError(t, err)
	shell, err := env.Attach(ctx, execenv.AttachRequest{})
	require.NoError(t, err)
	_, err = shell.Write([]byte("echo hello\n"))
	require.NoError(t, err)
	require.NoError(t, shell.Close())
	time.Sleep(2 * time.Second)

	// The host loses the grant: a new incarnation is projected from the
	// Attempt Workspace.
	require.True(t, guests.Lose(grantID))
	restarted := execenv.ID(connected.Attempt.ID.String() + ".1")
	eventually(func() bool { return readGuest(restarted, "answer.py") == "print(42)\n" }, "the new incarnation holds the harvested file")

	// The flush route writes this request record; the owner freezes,
	// drains and answers with the cursor.
	key := "attempt/" + connected.Attempt.ID.String()
	_, err = persistence.PluginRecord().Put(ctx, &store.PluginRecordPut{PluginID: "execution", Kind: "flush", Key: key,
		Document: json.RawMessage(`{"nonce":"execution-flow-flush","requested_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`)})
	require.NoError(t, err)
	var flushed struct {
		Done            bool  `json:"done"`
		Failed          bool  `json:"failed"`
		WorkspaceCursor int64 `json:"workspace_cursor"`
	}
	eventually(func() bool {
		record, err := persistence.PluginRecord().Get(ctx, "execution", "flush", key)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(record.Document, &flushed))
		return flushed.Done
	}, "the owner answers the flush")
	require.False(t, flushed.Failed)

	// The candidate submits with that cursor; the Submission revokes the
	// grant and seals the evidence.
	submitted, err := helper.App.Attempts.SubmitExamAttempt(ctx, desktop.inv, attempt.SubmitCommand{
		Access: mutation, ExpectedCurrentRevisionID: published.ID, ExpectedWorkspaceCursor: flushed.WorkspaceCursor,
		IdempotencyKey: "execution-flow-submit",
	})
	require.NoError(t, err, "the flushed cursor is the Workspace's")
	eventually(func() bool {
		report, err := guests.Ready(ctx)
		require.NoError(t, err)
		return report.Grants == 0
	}, "the Submission revokes the grant")

	evidence, err := persistence.ExamIntegrityReview().ListCapabilityEvidence(ctx, store.CapabilityEvidenceListOptions{
		SubmissionID: submitted.SubmissionID, Limit: 100,
	})
	require.NoError(t, err)
	kinds := map[string]int{}
	commands := false
	for _, item := range evidence.Items {
		kinds[string(item.Kind)]++
		commands = commands || strings.Contains(string(item.Detail), `"echo"`)
	}
	require.True(t, commands, "the command is sealed process evidence: %v", kinds)
	require.Equal(t, 1, kinds["execution.environment_restarted"], "the restart is one gap: %v", kinds)

}
