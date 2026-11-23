// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// auditExportStoreFake keeps exports in memory with the store's lifecycle:
// queued, then ready once published.
type auditExportStoreFake struct {
	store.AuditExportStore
	mu       sync.Mutex
	exports  map[model.AuditExportID]*store.AuditExportRead
	queries  map[model.AuditExportID]store.AuditListOptions
	created  int
	replayID model.AuditExportID
	finished []store.AuditExportArtifact
}

func newAuditExportStoreFake() *auditExportStoreFake {
	return &auditExportStoreFake{exports: map[model.AuditExportID]*store.AuditExportRead{}, queries: map[model.AuditExportID]store.AuditListOptions{}}
}

func (s *auditExportStoreFake) Create(_ context.Context, input *store.AuditExportCreation, _ *store.CommandIdempotency) (*model.AuditExport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.replayID.IsZero() {
		replay := *s.exports[s.replayID].Export
		return &replay, nil
	}
	s.created++
	e := *input.Export
	e.State, e.CreatedAt = model.ExamExportQueued, model.NowUTC()
	e.ExpiresAt = e.CreatedAt.Add(24 * time.Hour)
	s.exports[e.ID] = &store.AuditExportRead{Export: &e, Visibility: input.Query.Visibility}
	s.queries[e.ID] = input.Query
	result := e
	return &result, nil
}

func (s *auditExportStoreFake) Get(_ context.Context, id model.AuditExportID) (*store.AuditExportRead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	read, found := s.exports[id]
	if !found {
		return nil, store.NewErrNotFound("audit_export", id.String())
	}
	export := *read.Export
	return &store.AuditExportRead{Export: &export, Artifact: read.Artifact, Visibility: read.Visibility}, nil
}

func (s *auditExportStoreFake) BeginBuild(_ context.Context, input *store.AuditExportBuild) (*store.AuditExportSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	export := *s.exports[input.ExportID].Export
	return &store.AuditExportSnapshot{Export: &export, Query: s.queries[input.ExportID]}, nil
}

func (s *auditExportStoreFake) Publish(_ context.Context, input *store.AuditExportPublication) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	read := s.exports[input.ExportID]
	read.Export.State, read.Export.SizeBytes, read.Export.SHA256 = model.ExamExportReady, input.SizeBytes, input.SHA256
	read.Export.EventCount, read.Export.ReadyAt = input.EventCount, model.OptionalTimeFrom(read.Export.CreatedAt)
	read.Artifact = input.AuditExportArtifact
	return nil
}

func (s *auditExportStoreFake) FinishWriter(_ context.Context, a store.AuditExportArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, a)
	return nil
}

// auditExportContentFake keeps files in memory, or fails every build with err.
type auditExportContentFake struct {
	mu    sync.Mutex
	files map[store.AuditExportArtifact][]byte
	err   error
}

func (c *auditExportContentFake) BuildAuditExport(_ context.Context, id model.AuditExportID, attempt model.JobAttemptID, write func(io.Writer) error) (*AuditExportFile, error) {
	if c.err != nil {
		return nil, c.err
	}
	var body bytes.Buffer
	if err := write(&body); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files == nil {
		c.files = map[store.AuditExportArtifact][]byte{}
	}
	c.files[store.AuditExportArtifact{ExportID: id, AttemptID: attempt}] = body.Bytes()
	digest := sha256.Sum256(body.Bytes())
	return &AuditExportFile{SizeBytes: int64(body.Len()), SHA256: hex.EncodeToString(digest[:])}, nil
}

func (c *auditExportContentFake) OpenAuditExport(_ context.Context, id model.AuditExportID, attempt model.JobAttemptID) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return io.NopCloser(bytes.NewReader(c.files[store.AuditExportArtifact{ExportID: id, AttemptID: attempt}])), nil
}

func (*auditExportContentFake) PurgeAuditExport(context.Context, model.AuditExportID, model.JobAttemptID) error {
	return nil
}

type uncertainAuditExportWrite struct{}

func (uncertainAuditExportWrite) Error() string         { return "write outcome unknown" }
func (uncertainAuditExportWrite) ExportWriteUncertain() {}

// auditExportService is one caller's view of shared export state: the
// grants decide what the caller may list today.
func auditExportService(
	t *testing.T,
	persistence *auditListingStoreFake,
	exports *auditExportStoreFake,
	content *auditExportContentFake,
	grants ...accesstest.Grant,
) (*Service, *accesstest.Fixture) {
	t.Helper()
	fixture := accessWith(t, nil, accesstest.Options{Grants: grants})
	return &Service{
		institutions: anyInstitution(), audits: persistence, authorizer: fixture.Authorizer,
		people: people.New(peopleCatalog{}, fixture.Authorizer), auditor: fixture.Auditor.MutationAudit(),
		auditExports: exports, auditExportContent: content, now: time.Now,
	}, fixture
}

// buildAuditExport runs the build as the Job that claimed it would.
func buildAuditExport(t *testing.T, service *Service, export *model.AuditExport) store.AuditExportBuild {
	t.Helper()
	build := store.AuditExportBuild{
		AuditExportArtifact: store.AuditExportArtifact{ExportID: export.ID, AttemptID: model.NewJobAttemptID()},
		JobID:               export.JobID,
	}
	var err error
	build.ClaimToken, err = model.NewJobClaimToken()
	require.NoError(t, err)
	require.NoError(t, service.BuildAuditExportFromJob(context.Background(), invocation.Job(build.JobID, build.AttemptID), build))
	return build
}

func readAuditExport(t *testing.T, service *Service, inv invocation.Invocation, id model.AuditExportID) string {
	t.Helper()
	opened, err := service.OpenAuditExport(context.Background(), inv, id)
	require.NoError(t, err)
	defer opened.Body.Close()
	body, err := io.ReadAll(opened.Body)
	require.NoError(t, err)
	return string(body)
}

// sensitiveAuditEvent carries everything the listing hides from somebody:
// parameters, prior state and result from everyone, and the request's
// security metadata from an academic auditor.
func sensitiveAuditEvent(root model.AcademicUnitID) *model.AuditEvent {
	return &model.AuditEvent{
		ID: model.NewAuditEventID(), ActorID: model.NewUserID(), SessionID: model.NewSessionID(),
		CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Action: string(model.ActionClassMembersManage), Resource: model.Resource{Type: model.ResourceClass, ID: model.NewClassID().String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: root.String(), Status: model.AuditStatusSuccess,
		RequestID: "request-7", NodeID: "node-3", ClientType: "web", AuthMethod: "password",
		IPAddress: "198.51.100.7", UserAgent: "=HYPERLINK(\"x\")",
		Parameters: json.RawMessage(`{"operation":"add","value":{"note":"parameter-secret"}}`),
		PriorState: json.RawMessage(`{"was":"prior-secret"}`),
		Result:     json.RawMessage(`{"is":"result-secret"}`),
	}
}

func TestAuditExportBuildsTheListingTheCreatorSees(t *testing.T) {
	t.Parallel()
	events := []string{}
	caller, inv := newCaller()
	event := sensitiveAuditEvent(model.NewAcademicUnitID())
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}}
	exports, content := newAuditExportStoreFake(), &auditExportContentFake{}
	service, fixture := auditExportService(t, persistence, exports, content, auditReader(caller.UserID))

	export, err := service.CreateAuditExport(context.Background(), inv, CreateAuditExportCommand{
		Query:  ListAuditEventsQuery{Actions: []string{event.Action}},
		Format: model.AuditExportCSV, IdempotencyKey: "audit-export-1",
	})
	require.NoError(t, err)
	require.False(t, export.Redacted)
	require.Equal(t, 1, export.EventCount)
	frozen := exports.queries[export.ID]
	require.True(t, frozen.Visibility.InstitutionWide)
	require.Equal(t, []string{event.Action}, frozen.Actions)
	require.False(t, frozen.Until.IsZero(), "the export did not freeze its end")

	attempts := fixture.Audits.Attempts()
	require.Len(t, attempts, 1, "the request was not audited")
	require.Equal(t, string(model.ActionAuditView), attempts[0].Action)
	require.Contains(t, string(attempts[0].Parameters), "create_audit_export")
	require.Contains(t, string(attempts[0].Parameters), export.ID.String())

	_, err = service.OpenAuditExport(context.Background(), inv, export.ID)
	require.True(t, apperror.Is(err, "audit.export.not_ready"), "error = %v", err)

	build := buildAuditExport(t, service, export)
	require.Equal(t, frozen.Visibility, persistence.input.Visibility)
	require.Equal(t, frozen.Until, persistence.input.Until, "the build read past the frozen end")
	require.Equal(t, []store.AuditExportArtifact{build.AuditExportArtifact}, exports.finished, "the writer was not released")

	file := readAuditExport(t, service, inv, export.ID)
	lines := strings.Split(strings.TrimSpace(file), "\n")
	require.Len(t, lines, 2)
	require.True(t, strings.HasPrefix(lines[0], "id,created_at,updated_at,actor_id,session_id,action,"), lines[0])
	require.Contains(t, lines[1], event.ID.String())
	require.Contains(t, lines[1], "198.51.100.7", "institution-wide readers see the request metadata the listing shows")
	require.Contains(t, lines[1], `"'=HYPERLINK(""x"")"`, "a formula cell was left to run")
	for _, hidden := range []string{"parameter-secret", "prior-secret", "result-secret"} {
		require.NotContains(t, file, hidden, "the file carries what the listing hides")
	}
}

func TestAcademicAuditExportCarriesTheRedactedListing(t *testing.T) {
	t.Parallel()
	events := []string{}
	root := model.NewAcademicUnitID()
	caller, inv := newCaller()
	event := sensitiveAuditEvent(root)
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{event}}
	exports, content := newAuditExportStoreFake(), &auditExportContentFake{}
	service, fixture := auditExportService(t, persistence, exports, content, academicAuditorAt(root)(caller.UserID))

	export, err := service.CreateAuditExport(context.Background(), inv, CreateAuditExportCommand{Format: model.AuditExportJSONL, IdempotencyKey: "audit-export-2"})
	require.NoError(t, err)
	require.True(t, export.Redacted)
	require.Equal(t, string(model.ActionAcademicAuditView), fixture.Audits.Attempts()[0].Action)
	buildAuditExport(t, service, export)
	require.Equal(t, []model.AcademicUnitID{root}, persistence.input.Visibility.AcademicUnitRootIDs)
	require.Equal(t, model.AcademicAuditActions(), persistence.input.Visibility.AllowedActions)
	require.False(t, persistence.input.Visibility.InstitutionWide)

	file := readAuditExport(t, service, inv, export.ID)
	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(file)), &record))
	require.Equal(t, event.ID.String(), record["id"])
	require.Equal(t, event.ActorID.String(), record["actor_id"])
	for _, field := range []string{"session_id", "request_id", "node_id", "client_type", "authentication_method", "ip_address", "user_agent", "reason"} {
		require.NotContains(t, record, field, "an academic auditor's file carries %s", field)
	}
	for _, hidden := range []string{"parameter-secret", "prior-secret", "result-secret", "198.51.100.7"} {
		require.NotContains(t, file, hidden)
	}
	require.NotEmpty(t, event.IPAddress, "the projection changed the stored event")
}

func TestAuditExportIsOnlyItsCreatorsWhileTheyHoldItsVisibility(t *testing.T) {
	t.Parallel()
	events := []string{}
	caller, inv := newCaller()
	other := strangerTo(inv)
	persistence := &auditListingStoreFake{events: &events, list: []*model.AuditEvent{sensitiveAuditEvent(model.NewAcademicUnitID())}}
	exports, content := newAuditExportStoreFake(), &auditExportContentFake{}
	creator, _ := auditExportService(t, persistence, exports, content, auditReader(caller.UserID), auditReader(other.Principal().UserID))
	export, err := creator.CreateAuditExport(context.Background(), inv, CreateAuditExportCommand{Format: model.AuditExportCSV, IdempotencyKey: "audit-export-3"})
	require.NoError(t, err)
	buildAuditExport(t, creator, export)

	_, err = creator.GetAuditExport(context.Background(), other, export.ID)
	require.True(t, apperror.Is(err, "audit.export.not_found"), "another auditor read the export: %v", err)
	_, err = creator.OpenAuditExport(context.Background(), other, export.ID)
	require.True(t, apperror.Is(err, "audit.export.not_found"), "another auditor downloaded the export: %v", err)

	// The creator now holds only academic audit visibility, which no longer
	// covers the Institution-wide file.
	narrowed, _ := auditExportService(t, persistence, exports, content, academicAuditorEverywhere(caller.UserID))
	_, err = narrowed.GetAuditExport(context.Background(), inv, export.ID)
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v", err)
	_, err = narrowed.OpenAuditExport(context.Background(), inv, export.ID)
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v", err)

	exports.replayID = export.ID
	_, err = narrowed.CreateAuditExport(context.Background(), inv, CreateAuditExportCommand{
		Format: model.AuditExportCSV, IdempotencyKey: "audit-export-3",
	})
	require.True(t, apperror.Is(err, "authorization.denied"), "replay disclosed export metadata: %v", err)

	revoked, _ := auditExportService(t, persistence, exports, content)
	_, err = revoked.OpenAuditExport(context.Background(), inv, export.ID)
	require.True(t, apperror.Is(err, "authorization.denied"), "error = %v", err)
}

func TestAuditExportBuildReleasesItsWriterUnlessTheWriteIsUncertain(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		err      error
		released bool
	}{
		"failed write":    {err: errors.New("disk full"), released: true},
		"uncertain write": {err: uncertainAuditExportWrite{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events := []string{}
			caller, inv := newCaller()
			persistence := &auditListingStoreFake{events: &events}
			exports, content := newAuditExportStoreFake(), &auditExportContentFake{err: test.err}
			service, _ := auditExportService(t, persistence, exports, content, auditReader(caller.UserID))
			export, err := service.CreateAuditExport(context.Background(), inv, CreateAuditExportCommand{Format: model.AuditExportCSV, IdempotencyKey: "audit-export-4"})
			require.NoError(t, err)

			build := store.AuditExportBuild{
				AuditExportArtifact: store.AuditExportArtifact{ExportID: export.ID, AttemptID: model.NewJobAttemptID()},
				JobID:               export.JobID,
			}
			build.ClaimToken, err = model.NewJobClaimToken()
			require.NoError(t, err)
			err = service.BuildAuditExportFromJob(context.Background(), invocation.Job(build.JobID, build.AttemptID), build)
			require.True(t, apperror.Is(err, "audit.export.unavailable"), "error = %v", err)
			if test.released {
				require.Equal(t, []store.AuditExportArtifact{build.AuditExportArtifact}, exports.finished)
			} else {
				require.Empty(t, exports.finished, "an uncertain writer's key was released")
			}
			got, err := service.GetAuditExport(context.Background(), inv, export.ID)
			require.NoError(t, err)
			require.Equal(t, model.ExamExportQueued, got.State, "a failed build published")
		})
	}
}

func TestAuditExportRefusesMoreEventsThanOneFileHolds(t *testing.T) {
	t.Parallel()
	events := []string{}
	caller, inv := newCaller()
	persistence := &auditListingStoreFake{events: &events, listCount: model.AuditExportMaximumEvents + 1}
	exports, content := newAuditExportStoreFake(), &auditExportContentFake{}
	service, fixture := auditExportService(t, persistence, exports, content, auditReader(caller.UserID))
	_, err := service.CreateAuditExport(context.Background(), inv, CreateAuditExportCommand{Format: model.AuditExportCSV, IdempotencyKey: "audit-export-5"})
	require.True(t, apperror.Is(err, "audit.export.limit"), "error = %v", err)
	require.Zero(t, exports.created)
	require.Empty(t, fixture.Audits.Attempts(), "a refused request began a mutation")
}
