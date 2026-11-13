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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/internal/nativetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store/storetest"
	"github.com/sudosylabs/proctor/server/testlib"
)

// TestDesktopExamAttemptNativeFlow plays a candidate's Desktop through one
// recorded Attempt on the composed server and PostgreSQL, calling the same
// application services the HTTP and WebSocket handlers call: it reads the
// readiness overview, is refused at Start for low disk, then admitted,
// connects, streams facts until a second display raises a Flag, renews,
// uploads the Flag's clip for a manager to play, and submits. Each transport
// is checked against the API documents by its own package's tests.
func TestDesktopExamAttemptNativeFlow(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	const origin = "https://proctor.example.edu"
	build := model.DesktopBuildTuple{
		DesktopRelease: "0.1.0", DesktopBuildID: "native-flow-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16),
		DesktopTarget:                           "darwin-arm64", ConfigurationManifest: model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity: "native-flow-matrix",
	}
	build.NativeAgreement = nativetest.NativeAgreement(t, build)
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithDesktopBuildCatalog(build),
		testlib.WithConfig(func(cfg *config.Config) { cfg.Server.PublicURL = origin }))
	ctx := context.Background()
	const password = "correct horse battery staple"

	// An institution, a Class with one enrolled candidate, and an Exam
	// Manager allowed to author, publish, schedule and manage Sittings.
	bootstrap, err := helper.App.Identity.BootstrapInstallation(ctx, invocation.Invocation{}, identity.BootstrapInstallationCommand{
		InstitutionName: "native-flow", InstitutionDisplayName: "Native Flow University",
		AdministratorUsername: "native-flow-administrator", AdministratorEmail: "native-flow-administrator@example.edu",
		AdministratorDisplayName: "Native Flow Administrator", AdministratorLocale: "en", AdministratorTimezone: "UTC",
		Password: password, BootstrapSecret: testlib.BootstrapSecret, Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	unit, err := storetest.SaveAcademicUnit(ctx, persistence, &model.AcademicUnit{
		InstitutionID: bootstrap.Institution.ID, Name: "native-flow-computing", DisplayName: "Native Flow Computing",
	})
	require.NoError(t, err)
	programme, err := storetest.SaveProgramme(ctx, persistence, &model.Programme{
		AcademicUnitID: unit.ID, Name: "native-flow-cs", DisplayName: "Native Flow CS",
	})
	require.NoError(t, err)
	level, err := storetest.SaveProgrammeLevel(ctx, persistence, &model.ProgrammeLevel{
		ProgrammeID: programme.ID, Name: "native-flow-year-one", DisplayName: "Native Flow Year One",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	period, err := storetest.SaveAcademicPeriod(ctx, persistence, &model.AcademicPeriod{
		Owner: model.NewInstitutionAcademicPeriodOwner(bootstrap.Institution.ID), Name: "native-flow-period",
		DisplayName: "Native Flow Period", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
	})
	require.NoError(t, err)
	class, err := storetest.SaveClass(ctx, persistence, &model.Class{
		ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID, Name: "native-flow-class", DisplayName: "Native Flow Class",
	})
	require.NoError(t, err)
	candidate, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "native-flow-candidate", Email: "native-flow-candidate@example.edu", DisplayName: "Native Flow Candidate",
		EmailVerified: true, Locale: "en", Timezone: "UTC",
	}, Password: password})
	require.NoError(t, err)
	_, err = persistence.Affiliation().Save(ctx, &model.Affiliation{UserID: candidate.ID, Kind: model.AffiliationStudent, StartsAt: period.StartsAt})
	require.NoError(t, err)
	_, err = persistence.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: class.ID, UserID: candidate.ID, StartsAt: period.StartsAt})
	require.NoError(t, err)
	manager, err := helper.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "native-flow-manager", Email: "native-flow-manager@example.edu", DisplayName: "Native Flow Manager",
	}, Password: password})
	require.NoError(t, err)
	managerLogin, err := helper.App.Identity.Login(ctx, invocation.Invocation{}, identity.LoginCommand{
		LoginID: manager.Username, Password: password, ClientType: model.SessionClientCLI,
		DeviceID: "native-flow-manager-cli", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	managerPrincipal, err := helper.App.Identity.AuthenticateAccess(ctx, managerLogin.Tokens.AccessToken)
	require.NoError(t, err)
	managerInv := invocation.New(*managerPrincipal, model.RequestMetadata{RequestID: "native-flow-manager"})
	_, err = persistence.AcademicUnitMember().Save(ctx, &model.AcademicUnitMember{
		AcademicUnitID: unit.ID, UserID: manager.ID, StartsAt: model.TimeFromMillis(model.GetMillis() - 1_000),
	})
	require.NoError(t, err)
	role, err := storetest.SaveRole(ctx, persistence, &model.Role{
		Name: "native-flow-manager", DisplayName: "Native Flow Manager",
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

	// The Exam records the screen; the release baseline checks one display;
	// the process family enforces two rules on one capability: the release's
	// never-allowable applications are blocked, and only Discord may run.
	created, err := helper.App.ExamAuthoring.CreateExam(ctx, managerInv, exam.CreateCommand{
		AcademicUnitID: unit.ID, Title: "Native Flow", InstructionsMarkdown: "Answer.", IdempotencyKey: "native-flow-create",
	})
	require.NoError(t, err)
	native := model.DefaultNativeSecurityPolicy()
	native.ScreenRecording.Enabled = true
	for i := range native.Families {
		if native.Families[i].ID() == "process_application" {
			require.NoError(t, json.Unmarshal([]byte(
				`{"id":"process_application","mode":"enforce","allowed_application_ids":["discord"],"outcome":"flag"}`), &native.Families[i]))
		}
	}
	configured, err := helper.App.ExamAuthoring.ConfigureExamDraftNativePolicy(ctx, managerInv, exam.ConfigureDraftNativePolicyCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: created.Draft.Revision, NativePolicy: native, IdempotencyKey: "native-flow-policy",
	})
	require.NoError(t, err)
	published, err := helper.App.ExamPublication.PublishExamRevision(ctx, managerInv, exam.PublishRevisionCommand{
		ExamID: created.Exam.ID, ExpectedDraftRevision: configured.Draft.Revision, IdempotencyKey: "native-flow-publish",
	})
	require.NoError(t, err)
	startAt := time.Now().UTC().Add(2 * time.Second).Truncate(time.Millisecond)
	scheduled, err := helper.App.Sittings.ScheduleExamSitting(ctx, managerInv, sitting.ScheduleCommand{
		ExamID: created.Exam.ID, ExamRevisionID: published.ID, ClassID: class.ID,
		ScheduledStartAt: startAt, ScheduledEndAt: startAt.Add(time.Hour), IdempotencyKey: "native-flow-schedule",
	})
	require.NoError(t, err)
	sittingID := scheduled.Sitting.ID
	time.Sleep(time.Until(startAt) + 100*time.Millisecond)
	opened, err := helper.App.Sittings.AdvanceDue(ctx, invocation.System{JobID: model.NewJobID(), AttemptID: model.NewJobAttemptID()}, sittingID)
	require.NoError(t, err, "open the Sitting at its start")
	require.NotNil(t, opened.Value)
	require.Equal(t, model.ExamSittingOpen, opened.Value.Sitting.State)

	// The candidate signs in on Desktop: a registered DPoP key.
	desktop := signInDesktop(t, ctx, helper, origin, build, candidate.Username, password)

	// Before Start the overview shows the compiled policy and the recorder
	// notice, and checks the recorder.
	overview, err := helper.App.Attempts.SecurityPolicyOverview(ctx, desktop.inv, sittingID)
	require.NoError(t, err)
	require.True(t, overview.Resolved.Policy.ScreenRecording.Enabled)
	require.NotNil(t, overview.ScreenRecordingDisclosure, "the candidate is told the screen is recorded")
	require.True(t, containsRule(overview.Resolved.Policy.Rules, "screen_recording.free_space"))
	require.True(t, containsRule(overview.Resolved.Policy.Rules, "baseline.single_display"))

	// Start with too little disk is refused; a second Start with room passes.
	prepare := func(key string) *storePrepared {
		prepared, prepareErr := helper.App.Attempts.PrepareExamSecurityPreflight(ctx, desktop.inv, attempt.PrepareSecurityPreflightCommand{
			SittingID: sittingID, NativeRegistryDigest: build.NativeAgreement.RegistryDigest(),
			ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), IdempotencyKey: key,
		})
		require.NoError(t, prepareErr)
		readiness, readinessErr := prepared.Resolved.ReadinessDigest()
		require.NoError(t, readinessErr)
		overviewReadiness, readinessErr := overview.Resolved.ReadinessDigest()
		require.NoError(t, readinessErr)
		require.Equal(t, overviewReadiness, readiness, "Start prepares what the overview showed")
		return &storePrepared{id: prepared.Challenge.PreflightID, challenge: prepared.Challenge.Challenge,
			policyDigest: prepared.Resolved.Policy.Digest, contentDigest: prepared.Resolved.PolicyContentDigest,
			activations: prepared.Resolved.Policy.Activations}
	}
	report := func(prepared *storePrepared, freeSpace int, key string) *model.SecurityPreflightResult {
		result, reportErr := helper.App.Attempts.ReportExamSecurityPreflight(ctx, desktop.inv, attempt.ReportSecurityPreflightCommand{
			PreflightID: prepared.id, IdempotencyKey: key,
			Report: model.SecurityPreflightReport{
				Challenge: prepared.challenge, PolicyDigest: prepared.policyDigest, PolicyContentDigest: prepared.contentDigest,
				SecuritySessionID: "native-flow-security", Capabilities: startFacts(t, prepared.activations, freeSpace),
				Rules: []model.NativeRuleReport{}, ReportedAt: time.Now().UTC().Truncate(time.Millisecond),
			},
		})
		require.NoError(t, reportErr)
		return result
	}
	blocked := report(prepare("native-flow-prepare-1"), 1024, "native-flow-report-1")
	require.Equal(t, "blocked", blocked.Admission)
	require.Contains(t, blocked.ReasonCodes, model.SecurityReasonNativeRuleUnmet)
	time.Sleep(time.Second) // A Session may prepare one preflight per second.
	admittedPreflight := prepare("native-flow-prepare-2")
	eligible := report(admittedPreflight, 8192, "native-flow-report-2")
	require.Equal(t, "eligible", eligible.Admission, "reasons %v", eligible.ReasonCodes)

	credential := model.NewCredentialToken()
	settings, err := persistence.UserSettings().Get(ctx, candidate.ID)
	require.NoError(t, err, "Desktop configures the Attempt from the candidate's current settings")
	configuration := model.AttemptConfigurationCandidate{ManifestFingerprint: build.AttemptConfigurationManifestFingerprint,
		RegistryFingerprint: build.DesktopSettingsRegistryFingerprint, UserSettingsRevision: settings.Revision,
		DesktopBuild: build.DesktopBuildID, DesktopTarget: build.DesktopTarget,
		Presentation: model.AttemptConfigurationPresentation{ColorTheme: "dark", ZoomPercent: 100, EditorFontSizePX: 14,
			EditorLineHeightPX: 22, ScreenReaderMode: "auto", AnnouncementMode: "auto", CursorStyle: "line", CursorBlinking: "blink"},
		ApprovedCommands: []string{}, ApprovedKeybindings: []string{}}
	connected, err := helper.App.Attempts.ConnectExamAttempt(ctx, desktop.inv, attempt.ConnectCommand{
		Security:  model.ConnectSecurity{Kind: "preflight", PreflightID: admittedPreflight.id, ReportDigest: eligible.ReportDigest},
		SittingID: sittingID, ContinuityCredential: credential, IdempotencyKey: "native-flow-connect",
		ConfigurationManifestFingerprint: build.ConfigurationManifest.Fingerprint(), InitialConfiguration: &configuration,
	})
	require.NoError(t, err)
	require.True(t, connected.FirstAdmission)
	access := attempt.CandidateAccess{AttemptID: connected.Attempt.ID, ConnectionID: connected.Connection.ID, ContinuityCredential: credential}
	security := connected.Security

	// The candidate window mounts and a second display appears: the
	// baseline rule stops being met, which raises a Flag that asks for the
	// minute around it.
	at := time.Now().UTC().Truncate(time.Millisecond)
	acknowledged, err := helper.App.Attempts.AppendNativeDelivery(ctx, desktop.inv, attempt.NativeDeliveryAppendCommand{
		Query: attempt.NativeDeliveryQuery{Access: access, StreamID: security.DeliveryStreamID}, IdempotencyKey: "native-flow-batch-1",
		Batch: model.NativeSecurityBatch{StreamID: security.DeliveryStreamID, BatchSequence: 1, ParticipationID: security.ParticipationID,
			Generation: security.Generation, SecuritySessionID: security.SecuritySessionID, PolicyDigest: security.Policy.Digest,
			ApplicationReleaseID: security.Policy.ApplicationReleaseID, MatrixID: security.Policy.MatrixID,
			Records: []model.NativeRecord{
				// The mounted window's fact comes first: confirmation starts the
				// participation rules on the latest facts.
				streamFact("candidate_window", 2, at, flowWindowMounted, `{}`),
				{Confirmed: &model.NativeParticipationConfirmed{Kind: "participation_confirmed", ObservedAt: at}},
				streamFact("display", 2, at.Add(time.Second), flowOneDisplay, flowDisplaySummary(2)),
				// TeamViewer starts: it is never allowable and not on the list,
				// so both process rules stop being met.
				streamFact("process_application", 2, at.Add(2*time.Second), flowProcesses(true), `{"checking":0}`),
			}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), acknowledged.Receipt.BatchSequence)

	renewed, err := helper.App.Attempts.RenewExamAttemptParticipation(ctx, desktop.inv, attempt.RenewParticipationCommand{
		Security: model.SecurityRenewal{ControlSequence: 1, PolicyDigest: security.Policy.Digest,
			SecuritySessionID: security.SecuritySessionID, StreamID: security.DeliveryStreamID,
			DeliveryWatermarks: []model.DeliveryWatermark{{Family: "native", SourceID: security.DeliveryStreamID,
				AllocatedThroughSequence: 1, AcknowledgedThroughSequence: 1}}},
		AttemptID: access.AttemptID, ParticipationID: security.ParticipationID, ConnectionID: access.ConnectionID,
		Generation: security.Generation, Sequence: 1, ContinuityCredential: credential,
	})
	require.NoError(t, err)
	require.True(t, renewed.LeaseExpiresAt.After(connected.Participation.LeaseExpiresAt), "the renewal extends the lease")
	require.True(t, renewed.Security.SecurityInteractionAllowed, "a Flag without suspension keeps the candidate working")
	require.Empty(t, renewed.Security.DeliveryWatermarkRejections)

	// A manager also asks for the session so far, with a private reason.
	query := attempt.ManagedScreenClipQuery{ExamID: created.Exam.ID, SittingID: sittingID, AttemptID: access.AttemptID}
	_, err = helper.App.Attempts.RequestScreenClip(ctx, managerInv, attempt.ScreenClipRequestCommand{
		ExamID: created.Exam.ID, SittingID: sittingID, AttemptID: access.AttemptID,
		Range:         model.ScreenClipRange{FromSessionStart: true, End: time.Now().UTC().Truncate(time.Millisecond)},
		PrivateReason: "The candidate looked away", IdempotencyKey: "native-flow-clip",
	})
	require.NoError(t, err)

	// Desktop lists both requests, takes the Flag's, uploads it and signs it.
	pending, err := helper.App.Attempts.PendingScreenClips(ctx, desktop.inv, access)
	require.NoError(t, err)
	require.Len(t, pending, 4, "one clip for each rule's Flag, and the manager's: %#v", pending)
	var incident model.ScreenClipID
	flags := map[model.IntegrityFlagID]bool{}
	for _, clip := range pending {
		if clip.Source == model.ScreenClipSourceIncident {
			incident = clip.ID
			require.True(t, clip.FlagID.IsValid(), "the incident clip is linked to the Flag")
			flags[clip.FlagID] = true
		}
	}
	require.Len(t, flags, 3, "the display rule and each process rule have their own Flag")
	require.True(t, incident.IsValid(), "the second display raised a Flag that asked for a clip")
	_, err = helper.App.Attempts.TakeScreenClip(ctx, desktop.inv, attempt.TakeScreenClipCommand{Access: access, ClipID: incident})
	require.NoError(t, err)
	media := bytes.Repeat([]byte("fragmented mp4 "), 4096)
	chunk := sha256.Sum256(media)
	upload := model.ScreenClipUpload{MediaType: "video/mp4", SizeBytes: int64(len(media)), ChunkSHA256: []string{hex.EncodeToString(chunk[:])}}
	_, err = helper.App.Attempts.BeginScreenClipUpload(ctx, desktop.inv, attempt.BeginScreenClipUploadCommand{
		Access: access, ClipID: incident, Upload: upload,
	})
	require.NoError(t, err)
	err = helper.App.Attempts.UploadScreenClipChunk(ctx, desktop.inv, attempt.UploadScreenClipChunkCommand{
		Access: access, ClipID: incident, Index: 0, Body: bytes.NewReader(media), Size: int64(len(media)),
	})
	require.NoError(t, err)
	digest := upload.Digest()
	ready, err := helper.App.Attempts.CompleteScreenClipUpload(ctx, desktop.inv, attempt.CompleteScreenClipUploadCommand{
		Access: access, ClipID: incident, ContentDigest: digest, Signature: signDigest(t, desktop.key, digest),
	})
	require.NoError(t, err)
	require.Equal(t, model.ScreenClipReady, ready.State)

	// The manager plays it back, byte for byte.
	played, err := helper.App.Attempts.OpenScreenClip(ctx, managerInv, attempt.OpenScreenClipQuery{
		ScreenClipQuery: attempt.ScreenClipQuery{ManagedScreenClipQuery: query, ClipID: incident}, Start: 0, Length: int64(len(media)),
	})
	require.NoError(t, err)
	body, err := io.ReadAll(played.Body)
	require.NoError(t, played.Body.Close())
	require.NoError(t, err)
	require.Equal(t, media, body)
	require.Equal(t, "video/mp4", played.MediaType)

	// Submission ends the Attempt; the manager's clip, never taken, fails.
	_, err = helper.App.Attempts.SubmitExamAttempt(ctx, desktop.inv, attempt.SubmitCommand{
		Access: attempt.WorkspaceMutationAccess{CandidateAccess: access, ParticipationID: security.ParticipationID,
			Generation: security.Generation},
		ExpectedCurrentRevisionID: published.ID, ExpectedWorkspaceCursor: connected.Workspace.Cursor,
		IdempotencyKey: "native-flow-submit",
	})
	require.NoError(t, err)
	clips, err := helper.App.Attempts.ListScreenClips(ctx, managerInv, query)
	require.NoError(t, err)
	require.Len(t, clips, 4)
	for _, clip := range clips {
		if clip.ID == incident {
			require.Equal(t, model.ScreenClipReady, clip.State)
		} else {
			require.Equal(t, model.ScreenClipFailed, clip.State, "a request never taken fails at submission")
			require.Equal(t, model.ScreenClipFailureAttemptEnded, clip.Failure)
		}
	}
}

// storePrepared is what Desktop keeps of a prepared preflight.
type storePrepared struct {
	id, challenge, policyDigest, contentDigest string
	activations                                []model.NativeActivation
}

const (
	flowOneDisplay    = `{"targets":[{"target_id":"t1","active":true,"built_in":true,"kind":"physical","connection":"direct","power":"awake","primary":"primary","mirror_of":null}]}`
	flowWindowAbsent  = `{"presence":"absent","focus":"unknown","visibility":"unknown","placement":"unknown","content_protection":"unknown","occlusion":"unknown","close_attempts":0}`
	flowWindowMounted = `{"presence":"present","focus":"focused","visibility":"visible","placement":"maximized","content_protection":"verified","occlusion":"none","close_attempts":0}`
	flowSession       = `{"coverage":"full_lifecycle","console_owner":"candidate","lock":"unlocked","remote_attachment":"absent","other_human_sessions":0,"component":"running"}`
)

// flowProcesses is the process state with both rules matched or not.
func flowProcesses(matched bool) string {
	blocked := `[]`
	if matched {
		blocked = `["teamviewer"]`
	}
	return fmt.Sprintf(`{"coverage":"full_lifecycle","checking":0,"component":"running","rule_matches":[`+
		`{"rule_id":"process_application.blocked","matched":%t,"catalog_app_ids":%s},`+
		`{"rule_id":"process_application.allowlist","matched":%t,"catalog_app_ids":[]}]}`, matched, blocked, matched)
}

func flowDisplaySummary(physical int) string {
	return fmt.Sprintf(`{"active_physical":%d,"active_total":%d,"connected_total":%d,"mirrored":0,"virtual":0,"wireless":0,"remote":0,"specialized":0,"unknown_kind":0}`,
		physical, physical, physical)
}

// startFacts is one fresh fact per activated capability, as Desktop reports
// it at Start, with freeSpace MiB free for the recorder.
func startFacts(t *testing.T, activations []model.NativeActivation, freeSpace int) []model.NativeCapabilityReadiness {
	t.Helper()
	recorder := fmt.Sprintf(`{"free_space_mib":%d}`, freeSpace)
	facts := map[model.NativeCapabilityID][2]string{
		"candidate_window":    {flowWindowAbsent, `{}`},
		"display":             {flowOneDisplay, flowDisplaySummary(1)},
		"interactive_session": {flowSession, `{}`},
		"screen_recording":    {recorder, recorder},
		"process_application": {flowProcesses(false), `{"checking":0}`},
	}
	reported := []model.NativeCapabilityReadiness{}
	for _, activation := range activations {
		fact, known := facts[activation.Capability]
		require.True(t, known, "no Start fact for %s", activation.Capability)
		reported = append(reported, model.NativeCapabilityReadiness{
			Capability: activation.Capability, SourceInstanceID: "flow-" + string(activation.Capability), Sequence: 1,
			Permission: model.NativePermissionGranted, Health: model.NativeHealthHealthy, Certainty: model.NativeCertaintyComplete,
			State: json.RawMessage(fact[0]), Summary: json.RawMessage(fact[1]),
		})
	}
	return reported
}

func streamFact(capability model.NativeCapabilityID, sequence int64, at time.Time, state, summary string) model.NativeRecord {
	return model.NativeRecord{Fact: &model.NativeCapabilityFact{Kind: "capability_fact", ObservedAt: at,
		NativeCapabilityReadiness: model.NativeCapabilityReadiness{
			Capability: capability, SourceInstanceID: "flow-" + string(capability), Sequence: sequence,
			Permission: model.NativePermissionGranted, Health: model.NativeHealthHealthy, Certainty: model.NativeCertaintyComplete,
			State: json.RawMessage(state), Summary: json.RawMessage(summary),
		}}}
}

func containsRule(rules []model.NativeRule, id string) bool {
	for _, rule := range rules {
		if rule.RuleID == id {
			return true
		}
	}
	return false
}

// signDigest is the installation's ES256 signature over a clip's content
// digest: raw r||s, base64url without padding.
func signDigest(t *testing.T, key *ecdsa.PrivateKey, digest string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(digest))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))
}

// signedInDesktop is a candidate's registered Desktop: its key and the
// request invocation its DPoP-authenticated access gives the services.
type signedInDesktop struct {
	key *ecdsa.PrivateKey
	inv invocation.Invocation
}

// signInDesktop runs Desktop authorization for a local account and
// authenticates one DPoP request with the issued access token.
func signInDesktop(t *testing.T, ctx context.Context, helper *testlib.Helper, origin string,
	build model.DesktopBuildTuple, username, password string,
) signedInDesktop {
	t.Helper()
	web, err := helper.App.Identity.Login(ctx, invocation.Invocation{}, identity.LoginCommand{
		LoginID: username, Password: password, ClientType: model.SessionClientWeb, DeviceID: "flow-browser", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	browser, err := helper.App.Identity.AuthenticateAccess(ctx, web.Tokens.AccessToken)
	require.NoError(t, err)
	state, verifier := model.NewCredentialToken(), model.NewCredentialToken()
	key, jwk := integrationDesktopDPoPKey(t)
	started, err := helper.App.Identity.StartDesktopAuthorization(ctx, invocation.Invocation{}, identity.StartDesktopAuthorizationCommand{
		CallbackURL: "http://127.0.0.1:49152/" + model.NewCredentialToken(), State: state,
		CodeChallenge: model.PKCES256Challenge(verifier), DeviceID: "flow-desktop", DeviceName: "Exam laptop",
		PublicJWK: jwk, DesktopRelease: build.DesktopRelease, DesktopBuildID: build.DesktopBuildID,
		Platform: build.Platform, Architecture: build.Architecture, RealtimeProtocol: build.RealtimeProtocol, Source: "127.0.0.1:2",
	})
	require.NoError(t, err)
	authorization, err := url.Parse(started.AuthorizationURL)
	require.NoError(t, err)
	binding, err := helper.App.Identity.BindDesktopAuthorization(ctx, invocation.Invocation{}, identity.BindDesktopAuthorizationCommand{
		Handle: authorization.Query().Get("request"), BrowserProof: authorization.Fragment[len("proof="):], State: state,
	})
	require.NoError(t, err)
	_, err = helper.App.Identity.AuthenticateDesktopAuthorizationSession(ctx,
		invocation.New(*browser, model.RequestMetadata{RequestID: "flow-authorize"}),
		identity.AuthenticateDesktopAuthorizationCommand{Binding: binding.Binding})
	require.NoError(t, err)
	approved, err := helper.App.Identity.ApproveDesktopAuthorization(ctx, invocation.Invocation{},
		identity.ApproveDesktopAuthorizationCommand{Binding: binding.Binding, State: state})
	require.NoError(t, err)
	callback, err := url.Parse(approved.RedirectURL)
	require.NoError(t, err)
	exchanged, err := helper.App.Identity.ExchangeDesktopAuthorization(ctx, invocation.Invocation{}, identity.ExchangeDesktopAuthorizationCommand{
		Code: callback.Query().Get("code"), State: state, CodeVerifier: verifier, PublicJWK: jwk,
		DPoPProof: integrationDesktopDPoPProof(t, key, jwk, map[string]any{"jti": model.NewCredentialToken(), "htm": "POST",
			"htu": origin + "/api/v1/auth/desktop/token", "iat": time.Now().Unix(), "nonce": started.DPoPNonce}),
		DesktopRelease: build.DesktopRelease, DesktopBuildID: build.DesktopBuildID,
		Platform: build.Platform, Architecture: build.Architecture, RealtimeProtocol: build.RealtimeProtocol, Source: "127.0.0.1:2",
	})
	require.NoError(t, err)
	tokenHash := sha256.Sum256([]byte(exchanged.Tokens.AccessToken))
	const path = "/api/v1/exam-sittings"
	authenticate := func(nonce string) (*model.Principal, error) {
		return helper.App.Identity.AuthenticateDPoP(ctx, exchanged.Tokens.AccessToken,
			integrationDesktopDPoPProof(t, key, jwk, map[string]any{"jti": model.NewCredentialToken(), "htm": "GET",
				"htu": origin + path, "iat": time.Now().Unix(), "nonce": nonce,
				"ath": base64.RawURLEncoding.EncodeToString(tokenHash[:])}), "GET", path)
	}
	principal, err := authenticate(exchanged.DPoPNonce)
	if apperror.Is(err, "authentication.dpop.use_nonce") {
		nonce, ok := identity.DPoPChallengeNonce(err)
		require.True(t, ok)
		principal, err = authenticate(nonce)
	}
	require.NoError(t, err)
	require.True(t, principal.RegisteredDesktopKey)
	return signedInDesktop{key: key, inv: invocation.New(*principal, model.RequestMetadata{RequestID: "flow-desktop"})}
}
