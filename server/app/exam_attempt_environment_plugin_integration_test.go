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
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

// environmentPlugin is a fake execution plugin. It keeps the Attempt
// Workspace capability it is given, as the real one does for its workers.
type environmentPlugin struct {
	workspaces plugin.AttemptWorkspaces
}

func (*environmentPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "execution", Name: "Execution environment", EnabledByDefault: true}
}

func (p *environmentPlugin) Activate(_ context.Context, host plugin.Host) error {
	p.workspaces = host.AttemptWorkspaces()
	return nil
}

func (*environmentPlugin) Deactivate(context.Context) error { return nil }

type harvestedFile []byte

func (f harvestedFile) Next(context.Context) (io.Reader, error) { return bytes.NewReader(f), nil }

// TestExecutionPluginBindsAnEnvironmentAndWritesTheWorkspace runs a fake
// execution plugin on the composed server and PostgreSQL: a candidate's
// Desktop connects to an Attempt, the plugin route guard verifies the
// candidate's access, and the plugin binds an environment to the verified
// generation, harvests a file into the Attempt Workspace, sees it in the
// journal with origin environment, reads it back, and ends the binding.
func TestExecutionPluginBindsAnEnvironmentAndWritesTheWorkspace(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	const origin = "https://proctor.example.edu"
	build := model.DesktopBuildTuple{
		DesktopRelease: "0.1.0", DesktopBuildID: "environment-flow-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("c", 16),
		DesktopTarget:                           "darwin-arm64", ConfigurationManifest: model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity: "environment-flow-matrix",
	}
	build.NativeAgreement = nativetest.NativeAgreement(t, build)
	execution := &environmentPlugin{}
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithDesktopBuildCatalog(build),
		testlib.WithPlugins(execution),
		testlib.WithConfig(func(cfg *config.Config) { cfg.Server.PublicURL = origin }))
	ctx := context.Background()
	const password = "correct horse battery staple"
	require.NotNil(t, execution.workspaces, "the plugin activated with the capability")

	bootstrap, err := helper.App.Identity.BootstrapInstallation(ctx, invocation.Invocation{}, identity.BootstrapInstallationCommand{
		InstitutionName: "environment-flow", InstitutionDisplayName: "Environment Flow University",
		AdministratorUsername: "environment-flow-administrator", AdministratorEmail: "environment-flow-administrator@example.edu",
		AdministratorDisplayName: "Environment Flow Administrator", AdministratorLocale: "en", AdministratorTimezone: "UTC",
		Password: password, BootstrapSecret: testlib.BootstrapSecret, Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	unit, err := storetest.SaveAcademicUnit(ctx, persistence, &model.AcademicUnit{
		InstitutionID: bootstrap.Institution.ID, Name: "environment-flow-computing", DisplayName: "Environment Flow Computing",
	})
	require.NoError(t, err)
	programme, err := storetest.SaveProgramme(ctx, persistence, &model.Programme{
		AcademicUnitID: unit.ID, Name: "environment-flow-cs", DisplayName: "Environment Flow CS",
	})
	require.NoError(t, err)
	level, err := storetest.SaveProgrammeLevel(ctx, persistence, &model.ProgrammeLevel{
		ProgrammeID: programme.ID, Name: "environment-flow-year-one", DisplayName: "Environment Flow Year One",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	period, err := storetest.SaveAcademicPeriod(ctx, persistence, &model.AcademicPeriod{
		Owner: model.NewInstitutionAcademicPeriodOwner(bootstrap.Institution.ID), Name: "environment-flow-period",
		DisplayName: "Environment Flow Period", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
	})
	require.NoError(t, err)
	class, err := storetest.SaveClass(ctx, persistence, &model.Class{
		ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID, Name: "environment-flow-class", DisplayName: "Environment Flow Class",
	})
	require.NoError(t, err)
	candidate, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "environment-flow-candidate", Email: "environment-flow-candidate@example.edu", DisplayName: "Environment Flow Candidate",
		EmailVerified: true, Locale: "en", Timezone: "UTC",
	}, Password: password})
	require.NoError(t, err)
	_, err = persistence.Affiliation().Save(ctx, &model.Affiliation{UserID: candidate.ID, Kind: model.AffiliationStudent, StartsAt: period.StartsAt})
	require.NoError(t, err)
	_, err = persistence.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: class.ID, UserID: candidate.ID, StartsAt: period.StartsAt})
	require.NoError(t, err)
	manager, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "environment-flow-manager", Email: "environment-flow-manager@example.edu", DisplayName: "Environment Flow Manager",
	}, Password: password})
	require.NoError(t, err)
	managerLogin, err := helper.App.Identity.Login(ctx, invocation.Invocation{}, identity.LoginCommand{
		LoginID: manager.Username, Password: password, ClientType: model.SessionClientCLI,
		DeviceID: "environment-flow-manager-cli", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	managerPrincipal, err := helper.App.Identity.AuthenticateAccess(ctx, managerLogin.Tokens.AccessToken)
	require.NoError(t, err)
	managerInv := invocation.New(*managerPrincipal, model.RequestMetadata{RequestID: "environment-flow-manager"})
	_, err = persistence.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{
		AcademicUnitID: unit.ID, UserID: manager.ID, StartsAt: model.TimeFromMillis(model.GetMillis() - 1_000),
	})
	require.NoError(t, err)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{
		Name: "environment-flow-manager", DisplayName: "Environment Flow Manager",
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
		AcademicUnitID: unit.ID, Title: "Environment Flow", InstructionsMarkdown: "Write code.", IdempotencyKey: "environment-flow-create",
	})
	require.NoError(t, err)
	published, err := helper.App.ExamPublication.PublishExamRevision(ctx, managerInv, exam.PublishRevisionCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: created.Draft.Revision, IdempotencyKey: "environment-flow-publish",
	})
	require.NoError(t, err)
	startAt := time.Now().UTC().Add(2 * time.Second).Truncate(time.Millisecond)
	scheduled, err := helper.App.Sittings.ScheduleExamSitting(ctx, managerInv, sitting.ScheduleCommand{
		ExamID: created.Exam.ID, ExamRevisionID: published.ID, ClassID: class.ID,
		ScheduledStartAt: startAt, ScheduledEndAt: startAt.Add(time.Hour), IdempotencyKey: "environment-flow-schedule",
	})
	require.NoError(t, err)
	sittingID := scheduled.Sitting.ID
	time.Sleep(time.Until(startAt) + 100*time.Millisecond)
	_, err = helper.App.Sittings.AdvanceDue(ctx, invocation.System{JobID: model.NewJobID(), AttemptID: model.NewJobAttemptID()}, sittingID)
	require.NoError(t, err, "open the Sitting at its start")

	desktop := signInDesktop(t, ctx, helper, origin, build, candidate.Username, password)
	prepared, err := helper.App.Attempts.PrepareExamSecurityPreflight(ctx, desktop.inv, attempt.PrepareSecurityPreflightCommand{
		SittingID: sittingID, NativeRegistryDigest: build.NativeAgreement.RegistryDigest(),
		ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), IdempotencyKey: "environment-flow-prepare",
	})
	require.NoError(t, err)
	eligible, err := helper.App.Attempts.ReportExamSecurityPreflight(ctx, desktop.inv, attempt.ReportSecurityPreflightCommand{
		PreflightID: prepared.Challenge.PreflightID, IdempotencyKey: "environment-flow-report",
		Report: model.SecurityPreflightReport{
			Challenge: prepared.Challenge.Challenge, PolicyDigest: prepared.Resolved.Policy.Digest,
			PolicyContentDigest: prepared.Resolved.PolicyContentDigest, SecuritySessionID: "environment-flow-security",
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
		SittingID: sittingID, ContinuityCredential: credential, IdempotencyKey: "environment-flow-connect",
		ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), InitialConfiguration: &configuration,
	})
	require.NoError(t, err)
	access := attempt.CandidateAccess{AttemptID: connected.Attempt.ID, ConnectionID: connected.Connection.ID, ContinuityCredential: credential}

	// The plugin route guard verifies the candidate and hands over only
	// identifiers.
	verified, err := helper.App.Attempts.VerifyCandidateAttemptAccess(ctx, desktop.inv, access)
	require.NoError(t, err)
	require.Equal(t, connected.Attempt.ID, verified.AttemptID)
	require.Equal(t, connected.Security.ParticipationID, verified.ParticipationID)
	require.Equal(t, connected.Security.Generation, verified.Generation)
	require.Equal(t, candidate.ID, verified.CandidateUserID)
	wrong := access
	wrong.ContinuityCredential = model.NewCredentialToken()
	_, err = helper.App.Attempts.VerifyCandidateAttemptAccess(ctx, desktop.inv, wrong)
	require.Error(t, err, "another credential does not pass the guard")

	// The plugin binds its environment to the verified generation.
	identity := plugin.EnvironmentIdentity{GrantID: "grant-environment-flow", Epoch: "epoch-1"}
	bound, err := execution.workspaces.BindEnvironment(ctx, plugin.BindEnvironmentRequest{
		AttemptID: verified.AttemptID.String(), Generation: verified.Generation, Identity: identity,
	})
	require.NoError(t, err)
	require.Equal(t, verified.ParticipationID.String(), bound.Binding.ParticipationID)
	environment := plugin.BoundEnvironment{
		AttemptID: verified.AttemptID.String(), BindingID: bound.Binding.ID, Identity: identity, Generation: verified.Generation,
	}

	// It harvests a file from its guest into the Attempt Workspace.
	body := []byte("print('harvested')\n")
	digest := sha256.Sum256(body)
	cursor := connected.Workspace.Cursor
	result, err := execution.workspaces.ApplyBatch(ctx, plugin.WorkspaceBatch{
		Environment: environment,
		Mutations: []plugin.WorkspaceMutation{{
			Operation: plugin.WorkspaceCreateFile, DestinationPath: "harvested.py", MediaType: "text/x-python",
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)),
		}},
		Content:        harvestedFile(body),
		IdempotencyKey: "environment-flow-harvest-1",
	})
	require.NoError(t, err, "refusal %#v", err)
	require.Len(t, result.Changes, 1)
	require.Equal(t, "environment", result.Changes[0].Origin)

	journal, err := execution.workspaces.ListJournal(ctx, plugin.WorkspaceJournalRequest{
		Environment: environment, AfterCursor: cursor, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, journal.Entries, 1)
	require.Equal(t, "harvested.py", journal.Entries[0].NewPath)
	require.Equal(t, "environment", journal.Entries[0].Origin, "the environment can tell its own change from the editor's")

	opened, err := execution.workspaces.OpenFile(ctx, environment, result.Changes[0].EntryID)
	require.NoError(t, err)
	read, err := io.ReadAll(opened.Body)
	require.NoError(t, opened.Body.Close())
	require.NoError(t, err)
	require.Equal(t, body, read)

	// Once the binding ends, the plugin can no longer write.
	_, err = execution.workspaces.EndEnvironmentBinding(ctx, environment.AttemptID, environment.BindingID)
	require.NoError(t, err)
	_, err = execution.workspaces.ApplyBatch(ctx, plugin.WorkspaceBatch{
		Environment: environment,
		Mutations: []plugin.WorkspaceMutation{{
			Operation: plugin.WorkspaceCreateDirectory, DestinationPath: "late",
		}},
		IdempotencyKey: "environment-flow-harvest-2",
	})
	var refusal *plugin.CapabilityError
	require.ErrorAs(t, err, &refusal, "a write through an ended binding is refused with a public code")
	require.NotEmpty(t, refusal.Code)
}
