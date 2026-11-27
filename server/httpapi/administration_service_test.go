// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// administrationFixture is the real administration.Service the Role, Role
// Binding, audit, Access Policy and retention handlers call. It runs over
// in-memory Stores a test fills and reads back, the real authorizer and
// auditor, and the real mail Composer, so a handler test asserts what the
// request became at the Store.
type administrationFixture struct {
	access            *accesstest.Fixture
	users             *directoryUserStore
	roles             *administrationRoleStore
	bindings          *administrationBindingStore
	audits            *administrationAuditStore
	accessPolicies    *administrationAccessPolicyStore
	retentionPolicies *administrationRetentionPolicyStore
	assistantProfiles *httpAssistantProfileStore
	retention         *administrationRetentionStore
	publisher         *realtimetest.Publisher
	service           *administration.Service
}

// administrationRecentAuthentication is how long the fixture's service
// counts a sign-in as recent.
const administrationRecentAuthentication = 15 * time.Minute

// administrationSessions are the deployment Session lifetimes. They give
// the Access Policy Session lengths an idle and absolute floor of 16 minutes
// and defaults and ceilings of seven and thirty days.
func administrationSessions() identity.SessionPolicy {
	return identity.SessionPolicy{
		AccessTTL: 15 * time.Minute, ActivityUpdateInterval: 15 * time.Minute,
		IdleTTL: 7 * 24 * time.Hour, AbsoluteTTL: 30 * 24 * time.Hour,
		RefreshTTL: 24 * time.Hour, MaximumPerUser: 10, RetiredRetention: time.Hour,
	}
}

// newAdministrationFixture builds the real service with the callers' grants.
func newAdministrationFixture(t *testing.T, grants ...accesstest.Grant) *administrationFixture {
	t.Helper()
	fixture := &administrationFixture{
		users:             &directoryUserStore{byID: map[model.UserID]*model.User{}},
		roles:             &administrationRoleStore{},
		bindings:          &administrationBindingStore{counts: map[model.RoleID]store.RoleHolderCount{}},
		audits:            &administrationAuditStore{},
		accessPolicies:    &administrationAccessPolicyStore{},
		retentionPolicies: &administrationRetentionPolicyStore{},
		assistantProfiles: &httpAssistantProfileStore{},
		retention:         &administrationRetentionStore{},
		publisher:         &realtimetest.Publisher{},
	}
	fixture.access = accesstest.New(t, accesstest.Options{Grants: grants, Users: fixture.users})
	fixture.retention.institutionID = fixture.access.Institution.ID
	providers := identityProviders{byID: map[string]identity.ExternalIdentityProvider{}}
	sender := &identityMailSender{enabled: true}
	service, err := administration.New(administration.Dependencies{
		Store:        administrationCatalog{fixture: fixture},
		Authorizer:   fixture.access.Authorizer,
		Auditor:      fixture.access.Auditor,
		Capabilities: &identity.DeploymentCapabilities{Providers: providers, Mail: sender, Health: mail.NewHealth(true)},
		Mail:         administrationComposer(t, sender),
		Publisher:    fixture.publisher,

		CanonicalOrigin:         "https://proctor.example.edu",
		PasswordMinimumLength:   12,
		RecentAuthenticationTTL: administrationRecentAuthentication,
		Sessions:                administrationSessions(),
		AssistantProviders:      []string{"campus"},
		Now:                     time.Now,
	})
	require.NoError(t, err)
	fixture.service = service
	return fixture
}

// administrationComposer is the production mail Composer over a recording
// renderer and a sealed payload key.
func administrationComposer(t *testing.T, sender mail.Sender) *mail.Composer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: httpMailRenderer(t), Sender: sender, Sealer: sealer})
	require.NoError(t, err)
	return composer
}

// institution is the Institution the authorizer and the service share.
func (f *administrationFixture) institution() *model.Institution { return f.access.Institution }

type administrationCatalog struct {
	store.Catalog
	fixture *administrationFixture
}

func (c administrationCatalog) Role() store.RoleStore               { return c.fixture.roles }
func (c administrationCatalog) RoleBinding() store.RoleBindingStore { return c.fixture.bindings }
func (c administrationCatalog) User() store.UserStore               { return c.fixture.users }
func (c administrationCatalog) Institution() store.InstitutionStore {
	return identityInstitution{institution: c.fixture.access.Institution}
}
func (c administrationCatalog) Audit() store.AuditStore { return c.fixture.audits }
func (c administrationCatalog) AccessPolicy() store.AccessPolicyStore {
	return c.fixture.accessPolicies
}
func (c administrationCatalog) RetentionPolicy() store.RetentionPolicyStore {
	return c.fixture.retentionPolicies
}
func (c administrationCatalog) Retention() store.RetentionStore                   { return c.fixture.retention }
func (c administrationCatalog) PluginSettings() store.PluginSettingsStore         { return nil }
func (c administrationCatalog) Class() store.ClassStore                           { return nil }
func (c administrationCatalog) EnvironmentProfile() store.EnvironmentProfileStore { return nil }
func (c administrationCatalog) AuditExport() store.AuditExportStore               { return nil }

// administrationRoleStore holds the Roles a test lists and records the
// Roles the service created.
type administrationRoleStore struct {
	store.RoleStore
	mu      sync.Mutex
	roles   []*model.Role
	created []*store.RoleCreation
}

func (s *administrationRoleStore) add(roles ...*model.Role) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles = append(s.roles, roles...)
}

func (s *administrationRoleStore) List(context.Context) ([]*model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.roles), nil
}

func (s *administrationRoleStore) Get(_ context.Context, id model.RoleID) (*model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, role := range s.roles {
		if role.ID == id {
			return role.Clone(), nil
		}
	}
	return nil, store.NewErrNotFound("role", id.String())
}

func (s *administrationRoleStore) GetByIDs(_ context.Context, ids []model.RoleID) ([]*model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := []*model.Role{}
	for _, role := range s.roles {
		if slices.Contains(ids, role.ID) {
			found = append(found, role.Clone())
		}
	}
	return found, nil
}

func (s *administrationRoleStore) Save(_ context.Context, input *store.RoleCreation) (*model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, input)
	saved := input.Role.Clone()
	saved.ID = model.NewRoleID()
	saved.CreatedAt, saved.UpdatedAt = input.AuditAt, input.AuditAt
	s.roles = append(s.roles, saved)
	return saved.Clone(), nil
}

// lastCreated is the most recent Role creation the service committed.
func (s *administrationRoleStore) lastCreated(t *testing.T) *store.RoleCreation {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.created, "the request never created a Role")
	return s.created[len(s.created)-1]
}

// administrationBindingStore holds the Role Bindings a test names: holders
// are the current holders of the listed Roles, newest first.
type administrationBindingStore struct {
	store.RoleBindingStore
	mu       sync.Mutex
	bindings []*model.RoleBinding
	holders  []*model.RoleBinding
	counts   map[model.RoleID]store.RoleHolderCount

	holderLists  []store.RoleBindingRoleListOptions
	holderCounts []store.RoleHolderCountOptions
	ended        []*store.RoleBindingEnd
	// scopeLists names each "scope_type:scope_id" a scope listing read.
	scopeLists []string
}

func (s *administrationBindingStore) Get(_ context.Context, id model.RoleBindingID) (*model.RoleBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.bindings {
		if binding.ID == id {
			copied := *binding
			return &copied, nil
		}
	}
	return nil, store.NewErrNotFound("role_binding", id.String())
}

func (s *administrationBindingStore) End(_ context.Context, input *store.RoleBindingEnd) (*model.RoleBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = append(s.ended, input)
	for _, binding := range s.bindings {
		if binding.ID == input.ID {
			ended := *binding
			ended.EndsAt = model.OptionalTimeFrom(input.EndAt)
			return &ended, nil
		}
	}
	return nil, store.NewErrNotFound("role_binding", input.ID.String())
}

// ListByScope returns every held binding at the scope, as the Store does.
func (s *administrationBindingStore) ListByScope(_ context.Context, scopeType model.RoleScopeType, scopeID string) ([]*model.RoleBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopeLists = append(s.scopeLists, string(scopeType)+":"+scopeID)
	var scoped []*model.RoleBinding
	for _, binding := range s.bindings {
		if binding.ScopeType == scopeType && binding.ScopeID == scopeID {
			copied := *binding
			scoped = append(scoped, &copied)
		}
	}
	return scoped, nil
}

// ListCurrentByRoles continues after the cursor binding and reads at most
// the probe the service asks for.
func (s *administrationBindingStore) ListCurrentByRoles(_ context.Context, options store.RoleBindingRoleListOptions) ([]*model.RoleBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holderLists = append(s.holderLists, options)
	start := 0
	if !options.BeforeID.IsZero() {
		start = slices.IndexFunc(s.holders, func(binding *model.RoleBinding) bool { return binding.ID == options.BeforeID }) + 1
	}
	page := s.holders[start:]
	return slices.Clone(page[:min(len(page), options.Limit)]), nil
}

func (s *administrationBindingStore) CountCurrentByRoles(_ context.Context, _ store.RoleBindingRoleListOptions, maximum int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return min(len(s.holders), maximum), nil
}

func (s *administrationBindingStore) CountHoldersByRoles(_ context.Context, options store.RoleHolderCountOptions) ([]store.RoleHolderCount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holderCounts = append(s.holderCounts, options)
	counts := []store.RoleHolderCount{}
	for _, id := range options.RoleIDs {
		if count, ok := s.counts[id]; ok {
			counts = append(counts, count)
		}
	}
	return counts, nil
}

func (s *administrationBindingStore) setCount(count store.RoleHolderCount) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[count.RoleID] = count
}

func (s *administrationBindingStore) listedHolders() []store.RoleBindingRoleListOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.holderLists)
}

// administrationAuditStore is the audit history the listings read. events
// and self are what a listing returns, already in the requested order and
// including the probe row a test adds to say another page follows.
type administrationAuditStore struct {
	store.AuditStore
	mu          sync.Mutex
	repeats     map[model.AuditEventID]int
	repeatReads []store.AuditRepeatOptions
	events      []*model.AuditEvent
	total       int
	listErr     error
	self        []*store.AuditSelfEntry
	selfTotal   int
	names       store.AuditNames

	lists     []store.AuditListOptions
	selfLists []store.AuditSelfListOptions
	nameReads []store.AuditNameOptions
}

func (s *administrationAuditStore) List(_ context.Context, options store.AuditListOptions) ([]*model.AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists = append(s.lists, options)
	if s.listErr != nil {
		return nil, s.listErr
	}
	return slices.Clone(s.events), nil
}

func (s *administrationAuditStore) Count(_ context.Context, _ store.AuditListOptions, maximum int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return min(max(s.total, len(s.events)), maximum), nil
}

func (s *administrationAuditStore) ListSelf(_ context.Context, options store.AuditSelfListOptions) ([]*store.AuditSelfEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selfLists = append(s.selfLists, options)
	return slices.Clone(s.self), nil
}

func (s *administrationAuditStore) CountSelf(context.Context, store.AuditSelfListOptions) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selfTotal, nil
}

func (s *administrationAuditStore) ListNames(_ context.Context, options store.AuditNameOptions) (store.AuditNames, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nameReads = append(s.nameReads, options)
	return s.names, nil
}

// lastList is the options of the most recent audit listing.
func (s *administrationAuditStore) lastList(t *testing.T) store.AuditListOptions {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.lists, "the request never reached the audit history")
	return s.lists[len(s.lists)-1]
}

// lastSelfList is the options of the most recent own-history listing.
func (s *administrationAuditStore) lastSelfList(t *testing.T) store.AuditSelfListOptions {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.selfLists, "the request never reached the own audit history")
	return s.selfLists[len(s.selfLists)-1]
}

func (s *administrationAuditStore) selfListCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.selfLists)
}

// administrationSelfEntry is one recorded event of the caller's own
// history under the first action and operation the event is recorded as.
func administrationSelfEntry(t *testing.T, event model.AuditSelfEvent, at time.Time) *store.AuditSelfEntry {
	t.Helper()
	keys := model.AuditSelfEventKeys([]model.AuditSelfEvent{event}, model.AuditStatusSuccess)
	require.NotEmpty(t, keys, "no audit action records %s", event)
	return &store.AuditSelfEntry{
		ID: model.NewAuditEventID(), CreatedAt: at, Action: keys[0].Action, Operation: keys[0].Operation,
		Status: model.AuditStatusSuccess,
	}
}

// administrationAccessPolicyStore holds the current Access Policy and keeps
// the outcome of each replacement under its Idempotency-Key, as PostgreSQL
// does: the same command replays the outcome and another command under the
// same key conflicts.
type administrationAccessPolicyStore struct {
	store.AccessPolicyStore
	mu        sync.Mutex
	snapshot  *store.AccessPolicySnapshot
	preflight store.AccessPolicyPreflightResult
	// revoked is how many sessions each replacement reports it ended.
	revoked int

	preflights   []*store.AccessPolicyPreflight
	replacements []*store.AccessPolicyReplacement
	outcomes     map[[32]byte]administrationReplacementOutcome
}

type administrationReplacementOutcome struct {
	fingerprint [32]byte
	result      store.AccessPolicyReplacementResult
}

func (s *administrationAccessPolicyStore) Get(context.Context, int) (*store.AccessPolicySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil {
		return nil, store.NewErrNotFound("access_policy", "")
	}
	return s.snapshot, nil
}

func (s *administrationAccessPolicyStore) Preflight(_ context.Context, input *store.AccessPolicyPreflight) (*store.AccessPolicyPreflightResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preflights = append(s.preflights, input)
	result := s.preflight
	return &result, nil
}

func (s *administrationAccessPolicyStore) Replace(_ context.Context, input *store.AccessPolicyReplacement, command *store.CommandIdempotency) (*store.AccessPolicyReplacementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcomes == nil {
		s.outcomes = map[[32]byte]administrationReplacementOutcome{}
	}
	if kept, ok := s.outcomes[command.KeyDigest]; ok {
		if kept.fingerprint != command.Fingerprint {
			return nil, &store.ErrIdempotencyConflict{}
		}
		replayed := kept.result
		replayed.Replayed = true
		return &replayed, nil
	}
	s.replacements = append(s.replacements, input)
	result := store.AccessPolicyReplacementResult{Snapshot: s.snapshot, RevokedSessionCount: s.revoked}
	s.outcomes[command.KeyDigest] = administrationReplacementOutcome{fingerprint: command.Fingerprint, result: result}
	return &result, nil
}

func (s *administrationAccessPolicyStore) lastPreflight(t *testing.T) *store.AccessPolicyPreflight {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.preflights, "the request never reached the Access Policy preflight")
	return s.preflights[len(s.preflights)-1]
}

func (s *administrationAccessPolicyStore) touched() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.preflights) + len(s.replacements) + len(s.outcomes)
}

// administrationRetentionPolicyStore holds the Retention Policy and records
// each read and replacement.
type administrationRetentionPolicyStore struct {
	store.RetentionPolicyStore
	mu     sync.Mutex
	policy *model.RetentionPolicy
	calls  int

	replacement *store.RetentionPolicyReplacement
	command     *store.CommandIdempotency
}

func (s *administrationRetentionPolicyStore) Get(context.Context) (*model.RetentionPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.policy.Clone(), nil
}

func (s *administrationRetentionPolicyStore) Replace(_ context.Context, input *store.RetentionPolicyReplacement, command *store.CommandIdempotency) (*store.RetentionPolicyReplacementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.replacement, s.command = input, command
	return &store.RetentionPolicyReplacementResult{Policy: s.policy.Clone(), Changed: true}, nil
}

func (s *administrationRetentionPolicyStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// administrationRetentionStore answers retention control, notices and
// records, and records what each call was asked.
type administrationRetentionStore struct {
	holds       *store.RetentionHoldPage
	holdOptions []store.RetentionHoldListOptions
	preview     *store.RetentionPreviewCreation
	store.RetentionStore
	mu            sync.Mutex
	institutionID model.InstitutionID
	notices       []model.RetentionNotice
	records       *store.RetentionRecordPage
	examSummaries *store.RetentionExamSummaryPage
	examOptions   store.RetentionExamSummaryOptions
	calls         int

	change        *store.RetentionControlChange
	changeCommand *store.CommandIdempotency
	noticeUser    model.UserID
	noticeAfter   model.RetentionRetirementID
	noticeLimit   int
	recordOptions store.RetentionRecordListOptions
}

func (s *administrationRetentionStore) ChangeControl(_ context.Context, input *store.RetentionControlChange, command *store.CommandIdempotency) (*store.RetentionControlResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.change, s.changeCommand = input, command
	return &store.RetentionControlResult{Control: &model.RetentionControl{
		InstitutionID: s.institutionID, Revision: input.ExpectedRevision + 1, State: input.State,
		ApprovedPolicyRevision: input.ExpectedPolicyRevision, ApprovedPreviewID: input.PreviewID,
		ChangedByUserID: input.Principal.UserID, UpdatedAt: model.NowUTC(),
	}}, nil
}

func (s *administrationRetentionStore) ListNotices(_ context.Context, userID model.UserID, after model.RetentionRetirementID, limit int) ([]model.RetentionNotice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.noticeUser, s.noticeAfter, s.noticeLimit = userID, after, limit
	return slices.Clone(s.notices), nil
}

func (s *administrationRetentionStore) ListRecords(_ context.Context, options store.RetentionRecordListOptions) (*store.RetentionRecordPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.recordOptions = options
	return s.records, nil
}

func (s *administrationRetentionStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// administrationCounters are the counting functions behind the
// administration counts. Failed mail deliveries are the real mail
// Operations count; the onboarding and Job counts stand in for services this
// package does not build, authorizing their list Action through the real
// authorizer and answering the totals a test sets.
type administrationCounters struct {
	mail        *administrationMailStore
	invitations int
	calls       int
	mu          sync.Mutex
}

// newAdministrationCounts builds the real Counts over the fixture's
// authorizer.
func newAdministrationCounts(t *testing.T, fixture *administrationFixture) (*administration.Counts, *administrationCounters) {
	t.Helper()
	counters := &administrationCounters{mail: &administrationMailStore{}}
	institutions := identityInstitution{institution: fixture.institution()}
	operations, err := mail.NewOperations(mail.OperationsDependencies{
		Mail:                    counters.mail,
		Users:                   fixture.users,
		Authorization:           mail.InstitutionAuthorization{Authorizer: fixture.access.Authorizer, Institutions: institutions},
		Audit:                   fixture.access.Auditor,
		Attempts:                administrationMailTestLimiter{},
		Renderer:                httpMailRenderer(t),
		Sender:                  &identityMailSender{enabled: false},
		Metrics:                 administrationMailMetrics{},
		RecentAuthenticationTTL: administrationRecentAuthentication,
		Now:                     time.Now,
	})
	require.NoError(t, err)
	listed := func(action model.Action, total func() int) func(context.Context, invocation.Invocation, int) (int, error) {
		return func(ctx context.Context, inv invocation.Invocation, maximum int) (int, error) {
			counters.mu.Lock()
			counters.calls++
			counters.mu.Unlock()
			resource := model.Resource{Type: model.ResourceInstitution, ID: fixture.institution().ID.String()}
			if err := fixture.access.Authorizer.Authorize(ctx, inv, action, resource); err != nil {
				return 0, err
			}
			return min(total(), maximum), nil
		}
	}
	jobs := listed(model.ActionJobView, func() int { return 0 })
	counts := administration.NewCounts(administration.CountsDependencies{
		PendingInvitations: listed(model.ActionInvitationView, func() int {
			counters.mu.Lock()
			defer counters.mu.Unlock()
			return counters.invitations
		}),
		RecentJobFailures: func(ctx context.Context, inv invocation.Invocation, _ time.Duration, maximum int) (int, error) {
			return jobs(ctx, inv, maximum)
		},
		FailedMailDeliveries: func(ctx context.Context, inv invocation.Invocation, maximum int) (int, error) {
			counters.mu.Lock()
			counters.calls++
			counters.mu.Unlock()
			return operations.CountFailedMailDeliveries(ctx, inv, maximum)
		},
	})
	return counts, counters
}

func (c *administrationCounters) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// administrationMailStore counts the failed mail deliveries a test sets.
type administrationMailStore struct {
	store.MailStore
	mu     sync.Mutex
	failed int
	counts []store.MailDeliveryListOptions
}

func (s *administrationMailStore) CountDeliveries(_ context.Context, options store.MailDeliveryListOptions, maximum int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = append(s.counts, options)
	return min(s.failed, maximum), nil
}

type administrationMailTestLimiter struct{}

func (administrationMailTestLimiter) AccountMailTest(context.Context, model.UserID, time.Duration, int) (bool, error) {
	return true, nil
}

type administrationMailMetrics struct{}

func (administrationMailMetrics) Snapshot() mail.MetricsSnapshot { return mail.MetricsSnapshot{} }

// newAdministrationAcademic builds the real academic.Service over the
// fixture's Institution; no other academic Store is reachable.
func newAdministrationAcademic(t *testing.T, fixture *administrationFixture) *academic.Service {
	t.Helper()
	service, err := academic.New(academic.Dependencies{
		Store:      administrationAcademicCatalog{institution: fixture.institution()},
		Authorizer: fixture.access.Authorizer,
		Auditor:    fixture.access.Auditor,
		Publisher:  &realtimetest.Publisher{},
		Now:        time.Now,
		NewID:      model.NewID,
	})
	require.NoError(t, err)
	return service
}

type administrationAcademicCatalog struct {
	store.Catalog
	institution *model.Institution
}

func (c administrationAcademicCatalog) Institution() store.InstitutionStore {
	return identityInstitution{institution: c.institution}
}
func (administrationAcademicCatalog) AcademicUnit() store.AcademicUnitStore {
	return struct{ store.AcademicUnitStore }{}
}
func (administrationAcademicCatalog) Programme() store.ProgrammeStore {
	return struct{ store.ProgrammeStore }{}
}
func (administrationAcademicCatalog) ProgrammeLevel() store.ProgrammeLevelStore {
	return struct{ store.ProgrammeLevelStore }{}
}
func (administrationAcademicCatalog) AcademicPeriod() store.AcademicPeriodStore {
	return struct{ store.AcademicPeriodStore }{}
}
func (administrationAcademicCatalog) Class() store.ClassStore { return struct{ store.ClassStore }{} }
func (administrationAcademicCatalog) ClassMember() store.ClassMemberStore {
	return struct{ store.ClassMemberStore }{}
}
func (administrationAcademicCatalog) User() store.UserStore { return struct{ store.UserStore }{} }
func (administrationAcademicCatalog) Affiliation() store.AffiliationStore {
	return struct{ store.AffiliationStore }{}
}
func (administrationAcademicCatalog) AcademicUnitMember() store.AcademicUnitMemberStore {
	return struct{ store.AcademicUnitMemberStore }{}
}
func (administrationAcademicCatalog) RoleBinding() store.RoleBindingStore {
	return struct{ store.RoleBindingStore }{}
}
func (administrationAcademicCatalog) ExamSitting() store.ExamSittingStore {
	return struct{ store.ExamSittingStore }{}
}

// administrationAttempt is one mutation attempt the real auditor recorded,
// with the operation and value its Parameters carry.
type administrationAttempt struct {
	*model.AuditEvent
	Operation string
	Value     map[string]any
}

// onlyAdministrationAttempt returns the one mutation attempt recorded.
func onlyAdministrationAttempt(t *testing.T, fixture *administrationFixture) administrationAttempt {
	t.Helper()
	attempts := fixture.access.Audits.Attempts()
	require.Len(t, attempts, 1, "recorded mutation attempts")
	var parameters struct {
		Operation string         `json:"operation"`
		Value     map[string]any `json:"value"`
	}
	require.NoError(t, json.Unmarshal(attempts[0].Parameters, &parameters))
	return administrationAttempt{AuditEvent: attempts[0], Operation: parameters.Operation, Value: parameters.Value}
}

// administrationStrongPrincipal is a recent multi-factor Web Session, the
// assurance the policy and retention changes require.
func administrationStrongPrincipal() model.Principal {
	now := model.NowUTC()
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationMultiFactor, AuthenticatedAt: now,
		MFACompletedAt: model.OptionalTimeFrom(now), ClientType: model.SessionClientWeb,
	}
}

// administrationAccessPolicyGrant lets the caller read and replace the
// Access Policy.
func administrationAccessPolicyGrant(userID model.UserID) accesstest.Grant {
	return institutionWide(userID, model.ActionAccessPolicyView, model.ActionAccessPolicyManage)
}

func (c administrationCatalog) AssistantProfile() store.AssistantProfileStore {
	return c.fixture.assistantProfiles
}

func (c administrationCatalog) AssistantTranscript() store.AssistantTranscriptStore { return nil }

// CountRepeats reports the repeats the test set; an event without one has
// no entry.
func (s *administrationAuditStore) CountRepeats(_ context.Context, options store.AuditRepeatOptions) ([]store.AuditRepeat, error) {
	s.repeatReads = append(s.repeatReads, options)
	repeats := []store.AuditRepeat{}
	for _, id := range options.EventIDs {
		if count, repeated := s.repeats[id]; repeated {
			repeats = append(repeats, store.AuditRepeat{EventID: id, Count: count})
		}
	}
	return repeats, nil
}

func (s *administrationRetentionStore) SummarizeRecordsByExam(_ context.Context, options store.RetentionExamSummaryOptions) (*store.RetentionExamSummaryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.examOptions = options
	return s.examSummaries, nil
}

func (s *administrationRetentionStore) ListHolds(_ context.Context, options store.RetentionHoldListOptions) (*store.RetentionHoldPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.holdOptions = append(s.holdOptions, options)
	return s.holds, nil
}

func (s *administrationRetentionStore) CreatePreview(_ context.Context, input *store.RetentionPreviewCreation, _ *store.CommandIdempotency) (*model.RetentionPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.preview = input
	at := model.NowUTC()
	return &model.RetentionPreview{ID: input.PreviewID, InstitutionID: s.institutionID, PolicyRevision: input.ExpectedPolicyRevision,
		Proposed: input.Proposed != nil, CreatedByUserID: input.Principal.UserID, CreatedAt: at, ExpiresAt: at.Add(time.Minute)}, nil
}
