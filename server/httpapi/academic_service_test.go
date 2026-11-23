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
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// academicFixture is the real academic.Service the academic structure and
// membership handlers call. It runs over in-memory Stores a test fills and
// reads back, the real authorizer and auditor, and the real mail Composer,
// so a handler test asserts what each request became at the Store.
type academicFixture struct {
	access       *accesstest.Fixture
	institution  *academicInstitutionRecords
	units        *academicUnitRecords
	programmes   *academicProgrammeRecords
	levels       *academicLevelRecords
	periods      *academicPeriodRecords
	classes      *academicClassRecords
	classMembers *academicClassMemberRecords
	unitMembers  *academicUnitMemberRecords
	affiliations *academicAffiliationRecords
	users        *academicUserRecords
	bindings     *academicBindingRecords
	mail         *identityMailSender
	renders      *mailtest.Renders
	publisher    *realtimetest.Publisher
	service      *academic.Service
}

// academicOptions describe the caller's grants and the Academic Units the
// authorizer and the Store both hold. Now, when set, is the service clock.
type academicOptions struct {
	Grants []accesstest.Grant
	Units  []*model.AcademicUnit
	Now    func() time.Time
}

// newAcademicFixture builds the real academic.Service over the options.
func newAcademicFixture(t *testing.T, options academicOptions) *academicFixture {
	t.Helper()
	fixture := &academicFixture{
		institution:  &academicInstitutionRecords{},
		units:        &academicUnitRecords{byID: map[model.AcademicUnitID]*model.AcademicUnit{}},
		programmes:   &academicProgrammeRecords{byID: map[model.ProgrammeID]*model.Programme{}},
		levels:       &academicLevelRecords{byID: map[model.ProgrammeLevelID]*model.ProgrammeLevel{}},
		periods:      &academicPeriodRecords{byID: map[model.AcademicPeriodID]*model.AcademicPeriod{}},
		classes:      &academicClassRecords{byID: map[model.ClassID]*model.Class{}, unitOf: map[model.ClassID]model.AcademicUnitID{}},
		classMembers: &academicClassMemberRecords{byID: map[model.ClassMemberID]*model.ClassMember{}},
		unitMembers:  &academicUnitMemberRecords{byID: map[model.AcademicUnitMemberID]*model.AcademicUnitMember{}},
		affiliations: &academicAffiliationRecords{},
		users:        &academicUserRecords{identityUsers: newIdentityUsers()},
		bindings:     &academicBindingRecords{},
		mail:         &identityMailSender{enabled: true},
		publisher:    &realtimetest.Publisher{},
	}
	fixture.access = accesstest.New(t, accesstest.Options{
		Grants:          options.Grants,
		Units:           options.Units,
		Users:           fixture.users,
		Classes:         fixture.classes,
		AcademicPeriods: fixture.periods,
		Programmes:      fixture.programmes,
		ProgrammeLevels: fixture.levels,
	})
	// accesstest places each unit in its Institution; the Store holds the
	// same units afterwards so both sides agree.
	for _, unit := range options.Units {
		fixture.units.byID[unit.ID] = unit
	}

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	var renderer *mail.TemplateRenderer
	renderer, fixture.renders = recordingHTTPMailRenderer(t)
	composer, err := mail.NewComposer(mail.ComposerDependencies{
		Renderer: renderer, Sender: fixture.mail, Sealer: sealer,
	})
	require.NoError(t, err)

	now := options.Now
	if now == nil {
		now = time.Now
	}
	service, err := academic.New(academic.Dependencies{
		Store:      academicCatalog{fixture: fixture},
		Authorizer: fixture.access.Authorizer,
		Auditor:    fixture.access.Auditor,
		Mail:       composer,
		Publisher:  fixture.publisher,
		Now:        now,
		NewID:      model.NewID,
	})
	require.NoError(t, err)
	fixture.service = service
	return fixture
}

// people names the fixture's users through its authorizer.
func (f *academicFixture) people() *people.Resolver {
	return testPeople(f.users, f.access.Authorizer)
}

// onlyAttempt is the one mutation attempt the auditor recorded.
func (f *academicFixture) onlyAttempt(t *testing.T) *model.AuditEvent {
	t.Helper()
	attempts := f.access.Audits.Attempts()
	require.Len(t, attempts, 1, "mutation attempts")
	return attempts[0]
}

// academicWorld is one valid path through the academic structure: a unit
// holding a Programme, its Level, a unit-owned Academic Period and a Class of
// that Level in that Period.
type academicWorld struct {
	unit      *model.AcademicUnit
	programme *model.Programme
	level     *model.ProgrammeLevel
	period    *model.AcademicPeriod
	class     *model.Class
}

// newAcademicWorld builds the world's records as the Store would hold them.
func newAcademicWorld() academicWorld {
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	unit := &model.AcademicUnit{Name: "computing", DisplayName: "Computing"}
	unit.PrepareCreate(model.NewAcademicUnitID(), at)
	programme := &model.Programme{AcademicUnitID: unit.ID, Name: "computer-science", DisplayName: "Computer Science"}
	programme.PrepareCreate(model.NewProgrammeID(), at)
	level := &model.ProgrammeLevel{ProgrammeID: programme.ID, Name: "year-1", DisplayName: "Year 1"}
	level.PrepareCreate(model.NewProgrammeLevelID(), at)
	period := &model.AcademicPeriod{
		Owner: model.NewAcademicUnitAcademicPeriodOwner(unit.ID), Name: "2026-2027", DisplayName: "2026-2027",
		StartsAt: model.TimeFromMillis(1_000), EndsAt: at.Add(365 * 24 * time.Hour),
	}
	period.PrepareCreate(model.NewAcademicPeriodID(), at)
	class := &model.Class{ProgrammeLevelID: level.ID, AcademicPeriodID: period.ID, Name: "class-a", DisplayName: "Class A"}
	class.PrepareCreate(model.NewClassID(), at)
	return academicWorld{unit: unit, programme: programme, level: level, period: period, class: class}
}

// units is the world's unit, for academicOptions.Units.
func (w academicWorld) units() []*model.AcademicUnit { return []*model.AcademicUnit{w.unit} }

// hold places the world's Programme, Level, Period and Class in the Stores;
// its unit reaches them through academicOptions.Units.
func (f *academicFixture) hold(w academicWorld) {
	f.programmes.byID[w.programme.ID] = w.programme
	f.levels.byID[w.level.ID] = w.level
	f.periods.byID[w.period.ID] = w.period
	f.classes.byID[w.class.ID] = w.class
	f.classes.unitOf[w.class.ID] = w.unit.ID
}

// academicUser is an active User with a verified address, so relationship
// mail can be prepared for them.
func academicUser(username, displayName, email string) *model.User {
	user := &model.User{Username: username, DisplayName: displayName, Email: email, EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(model.NewUserID(), time.Now().Add(-time.Hour))
	return user
}

// academicCaller is a signed-in CLI caller; the real authorizer refuses a
// zero Principal.
func academicCaller() model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
}

type academicCatalog struct {
	store.Catalog
	fixture *academicFixture
}

func (c academicCatalog) Institution() store.InstitutionStore       { return c.fixture.institution }
func (c academicCatalog) AcademicUnit() store.AcademicUnitStore     { return c.fixture.units }
func (c academicCatalog) Programme() store.ProgrammeStore           { return c.fixture.programmes }
func (c academicCatalog) ProgrammeLevel() store.ProgrammeLevelStore { return c.fixture.levels }
func (c academicCatalog) AcademicPeriod() store.AcademicPeriodStore { return c.fixture.periods }
func (c academicCatalog) Class() store.ClassStore                   { return c.fixture.classes }
func (c academicCatalog) ClassMember() store.ClassMemberStore       { return c.fixture.classMembers }
func (c academicCatalog) User() store.UserStore                     { return c.fixture.users }
func (c academicCatalog) Affiliation() store.AffiliationStore       { return c.fixture.affiliations }
func (c academicCatalog) RoleBinding() store.RoleBindingStore       { return c.fixture.bindings }
func (c academicCatalog) ExamSitting() store.ExamSittingStore       { return academicNoSittings{} }
func (c academicCatalog) AcademicUnitMember() store.AcademicUnitMemberStore {
	return c.fixture.unitMembers
}

// academicNoSittings has no Sitting board a membership change refreshes.
type academicNoSittings struct{ store.ExamSittingStore }

func (academicNoSittings) ListInvalidationTargetsByClass(context.Context, model.ClassID, model.ExamSittingID, int) ([]store.ExamSittingInvalidationTarget, error) {
	return nil, nil
}

// academicNameRecords is a name-ordered listing: the records listed, plus one
// filler past the probe when more is set, so the page reports another.
type academicNameRecords[T any] struct {
	listed []T
	filler T
	more   bool
	total  int

	listOptions  []store.AcademicNameListOptions
	countOptions []store.AcademicNameListOptions
	err          error
}

func (p *academicNameRecords[T]) list(options store.AcademicNameListOptions) ([]T, error) {
	p.listOptions = append(p.listOptions, options)
	if p.err != nil {
		return nil, p.err
	}
	page := slices.Clone(p.listed)
	if p.more {
		page = append(page, p.filler)
	}
	return page, nil
}

func (p *academicNameRecords[T]) count(options store.AcademicNameListOptions, maximum int) (int, error) {
	p.countOptions = append(p.countOptions, options)
	return min(max(p.total, len(p.listed)), maximum), nil
}

// lastList is the options of the most recent listing.
func (p *academicNameRecords[T]) lastList(t *testing.T) store.AcademicNameListOptions {
	t.Helper()
	require.NotEmpty(t, p.listOptions, "the request never reached the listing")
	return p.listOptions[len(p.listOptions)-1]
}

type academicInstitutionRecords struct {
	store.InstitutionStore
	current *model.Institution
	updates []*store.InstitutionUpdate
}

func (s *academicInstitutionRecords) GetSingleton(context.Context) (*model.Institution, error) {
	if s.current == nil {
		return nil, store.NewErrNotFound("institution", "")
	}
	copied := *s.current
	return &copied, nil
}

func (s *academicInstitutionRecords) Update(_ context.Context, input *store.InstitutionUpdate) (*model.Institution, error) {
	s.updates = append(s.updates, input)
	copied := *input.Institution
	return &copied, nil
}

type academicUnitRecords struct {
	store.AcademicUnitStore
	byID     map[model.AcademicUnitID]*model.AcademicUnit
	created  []*store.AcademicUnitCreation
	updates  []*store.AcademicUnitUpdate
	archives []*store.AcademicUnitArchive
}

func (s *academicUnitRecords) Get(_ context.Context, id model.AcademicUnitID) (*model.AcademicUnit, error) {
	unit, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("academic_unit", id.String())
	}
	copied := *unit
	return &copied, nil
}

func (s *academicUnitRecords) ListAncestors(_ context.Context, id model.AcademicUnitID) ([]*model.AcademicUnit, error) {
	var chain []*model.AcademicUnit
	for unit, ok := s.byID[id]; ok; unit, ok = s.byID[unit.ParentID] {
		chain = append(chain, unit)
	}
	if len(chain) == 0 {
		return nil, store.NewErrNotFound("academic_unit", id.String())
	}
	return chain, nil
}

func (s *academicUnitRecords) Create(_ context.Context, input *store.AcademicUnitCreation) (*model.AcademicUnit, error) {
	s.created = append(s.created, input)
	copied := *input.Unit
	return &copied, nil
}

func (s *academicUnitRecords) Update(_ context.Context, input *store.AcademicUnitUpdate) (*model.AcademicUnit, error) {
	s.updates = append(s.updates, input)
	copied := *input.Unit
	return &copied, nil
}

func (s *academicUnitRecords) Archive(_ context.Context, input *store.AcademicUnitArchive) (*model.AcademicUnit, error) {
	s.archives = append(s.archives, input)
	copied := *s.byID[input.ID]
	copied.ArchivedAt = model.OptionalTimeFrom(input.ArchiveAt)
	return &copied, nil
}

type academicProgrammeRecords struct {
	store.ProgrammeStore
	byID     map[model.ProgrammeID]*model.Programme
	page     academicNameRecords[*model.Programme]
	created  []*store.ProgrammeCreation
	updates  []*store.ProgrammeUpdate
	archives []*store.ProgrammeArchive
}

func (s *academicProgrammeRecords) Get(_ context.Context, id model.ProgrammeID) (*model.Programme, error) {
	programme, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("programme", id.String())
	}
	copied := *programme
	return &copied, nil
}

func (s *academicProgrammeRecords) SearchByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions) ([]*model.Programme, error) {
	return s.page.list(options)
}

func (s *academicProgrammeRecords) CountByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions, maximum int) (int, error) {
	return s.page.count(options, maximum)
}

func (s *academicProgrammeRecords) Create(_ context.Context, input *store.ProgrammeCreation) (*model.Programme, error) {
	s.created = append(s.created, input)
	copied := *input.Programme
	return &copied, nil
}

func (s *academicProgrammeRecords) Update(_ context.Context, input *store.ProgrammeUpdate) (*model.Programme, error) {
	s.updates = append(s.updates, input)
	copied := *input.Programme
	return &copied, nil
}

func (s *academicProgrammeRecords) Archive(_ context.Context, input *store.ProgrammeArchive) (*model.Programme, error) {
	s.archives = append(s.archives, input)
	copied := *s.byID[input.ID]
	return &copied, nil
}

type academicLevelRecords struct {
	store.ProgrammeLevelStore
	byID     map[model.ProgrammeLevelID]*model.ProgrammeLevel
	created  []*store.ProgrammeLevelCreation
	updates  []*store.ProgrammeLevelUpdate
	archives []*store.ProgrammeLevelArchive
}

func (s *academicLevelRecords) Get(_ context.Context, id model.ProgrammeLevelID) (*model.ProgrammeLevel, error) {
	level, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("programme_level", id.String())
	}
	copied := *level
	return &copied, nil
}

func (s *academicLevelRecords) Create(_ context.Context, input *store.ProgrammeLevelCreation) (*model.ProgrammeLevel, error) {
	s.created = append(s.created, input)
	copied := *input.Level
	return &copied, nil
}

func (s *academicLevelRecords) Update(_ context.Context, input *store.ProgrammeLevelUpdate) (*model.ProgrammeLevel, error) {
	s.updates = append(s.updates, input)
	copied := *input.Level
	return &copied, nil
}

func (s *academicLevelRecords) Archive(_ context.Context, input *store.ProgrammeLevelArchive) (*model.ProgrammeLevel, error) {
	s.archives = append(s.archives, input)
	copied := *s.byID[input.ID]
	return &copied, nil
}

type academicPeriodRecords struct {
	store.AcademicPeriodStore
	byID     map[model.AcademicPeriodID]*model.AcademicPeriod
	lists    int
	created  []*store.AcademicPeriodCreation
	updates  []*store.AcademicPeriodUpdate
	archives []*store.AcademicPeriodArchive
}

func (s *academicPeriodRecords) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	period, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("academic_period", id.String())
	}
	copied := *period
	return &copied, nil
}

func (s *academicPeriodRecords) ListVisible(context.Context, store.AcademicPeriodVisibilityScope, store.AcademicPeriodListOptions) ([]*model.AcademicPeriod, error) {
	s.lists++
	return nil, nil
}

func (s *academicPeriodRecords) Create(_ context.Context, input *store.AcademicPeriodCreation) (*model.AcademicPeriod, error) {
	s.created = append(s.created, input)
	copied := *input.Period
	return &copied, nil
}

func (s *academicPeriodRecords) Update(_ context.Context, input *store.AcademicPeriodUpdate) (*model.AcademicPeriod, error) {
	s.updates = append(s.updates, input)
	copied := *input.Period
	return &copied, nil
}

func (s *academicPeriodRecords) Archive(_ context.Context, input *store.AcademicPeriodArchive) (*model.AcademicPeriod, error) {
	s.archives = append(s.archives, input)
	copied := *s.byID[input.ID]
	return &copied, nil
}

// academicClassRecords holds Classes and the unit each belongs to, which the
// authorizer resolves a Class through.
type academicClassRecords struct {
	store.ClassStore
	byID     map[model.ClassID]*model.Class
	unitOf   map[model.ClassID]model.AcademicUnitID
	search   academicNameRecords[*model.Class]
	byLevel  academicNameRecords[*model.Class]
	created  []*store.ClassCreation
	updates  []*store.ClassUpdate
	archives []*store.ClassArchive
}

func (s *academicClassRecords) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	class, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("class", id.String())
	}
	copied := *class
	return &copied, nil
}

func (s *academicClassRecords) GetAcademicUnitID(_ context.Context, id model.ClassID) (model.AcademicUnitID, error) {
	unitID, ok := s.unitOf[id]
	if !ok {
		return "", store.NewErrNotFound("class", id.String())
	}
	return unitID, nil
}

func (s *academicClassRecords) SearchByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions) ([]*model.Class, error) {
	return s.search.list(options)
}

func (s *academicClassRecords) CountByAcademicUnit(_ context.Context, _ model.AcademicUnitID, options store.AcademicNameListOptions, maximum int) (int, error) {
	return s.search.count(options, maximum)
}

func (s *academicClassRecords) ListByProgrammeLevel(_ context.Context, _ model.ProgrammeLevelID, options store.AcademicNameListOptions) ([]*model.Class, error) {
	return s.byLevel.list(options)
}

func (s *academicClassRecords) CountByProgrammeLevel(_ context.Context, _ model.ProgrammeLevelID, options store.AcademicNameListOptions, maximum int) (int, error) {
	return s.byLevel.count(options, maximum)
}

func (s *academicClassRecords) Create(_ context.Context, input *store.ClassCreation) (*model.Class, error) {
	s.created = append(s.created, input)
	copied := *input.Class
	return &copied, nil
}

func (s *academicClassRecords) Update(_ context.Context, input *store.ClassUpdate) (*model.Class, error) {
	s.updates = append(s.updates, input)
	copied := *input.Class
	return &copied, nil
}

func (s *academicClassRecords) Archive(_ context.Context, input *store.ClassArchive) (*model.Class, error) {
	s.archives = append(s.archives, input)
	copied := *s.byID[input.ID]
	return &copied, nil
}

// academicClassMemberRecords holds Class Memberships and answers one page of
// a roster at a time.
type academicClassMemberRecords struct {
	store.ClassMemberStore
	byID  map[model.ClassMemberID]*model.ClassMember
	page  store.ClassMemberPage
	total int

	pageOptions  []store.ClassMemberPageOptions
	countOptions []store.ClassMemberPageOptions
	enrollments  []*store.ClassMemberEnrollment
	ends         []*store.ClassMemberEnd

	// studentCounts answers a Class listing's count; countedClasses records
	// the Classes each count asked for.
	studentCounts  []store.ClassStudentCount
	countedClasses [][]model.ClassID
}

func (s *academicClassMemberRecords) CountCurrentByClasses(_ context.Context, classIDs []model.ClassID, _ time.Time, _ int) ([]store.ClassStudentCount, error) {
	s.countedClasses = append(s.countedClasses, slices.Clone(classIDs))
	return s.studentCounts, nil
}

func (s *academicClassMemberRecords) Get(_ context.Context, id model.ClassMemberID) (*model.ClassMember, error) {
	member, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("class_member", id.String())
	}
	copied := *member
	return &copied, nil
}

func (s *academicClassMemberRecords) ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.ClassMember, error) {
	return nil, nil
}

func (s *academicClassMemberRecords) ListPageByClass(_ context.Context, options store.ClassMemberPageOptions) (*store.ClassMemberPage, error) {
	s.pageOptions = append(s.pageOptions, options)
	page := s.page
	page.Members = slices.Clone(page.Members)
	return &page, nil
}

func (s *academicClassMemberRecords) CountPageByClass(_ context.Context, options store.ClassMemberPageOptions, maximum int) (int, error) {
	s.countOptions = append(s.countOptions, options)
	return min(max(s.total, len(s.page.Members)), maximum), nil
}

// lastPage is the options of the most recent roster page.
func (s *academicClassMemberRecords) lastPage(t *testing.T) store.ClassMemberPageOptions {
	t.Helper()
	require.NotEmpty(t, s.pageOptions, "the request never reached the roster")
	return s.pageOptions[len(s.pageOptions)-1]
}

func (s *academicClassMemberRecords) Enroll(_ context.Context, input *store.ClassMemberEnrollment) (*store.ClassEnrollmentResult, error) {
	s.enrollments = append(s.enrollments, input)
	return &store.ClassEnrollmentResult{Membership: input.Member}, nil
}

func (s *academicClassMemberRecords) End(_ context.Context, input *store.ClassMemberEnd) (*model.ClassMember, error) {
	s.ends = append(s.ends, input)
	ended := *s.byID[input.ID]
	ended.EndsAt = model.OptionalTimeFrom(input.EndAt)
	ended.Revision = input.ExpectedRevision + 1
	return &ended, nil
}

// academicUnitMemberRecords answers one page of a unit's members at a time.
type academicUnitMemberRecords struct {
	store.AcademicUnitMemberStore
	byID  map[model.AcademicUnitMemberID]*model.AcademicUnitMember
	page  store.AcademicUnitMemberPage
	total int

	pageOptions  []store.AcademicUnitMemberPageOptions
	countOptions []store.AcademicUnitMemberPageOptions
	created      []*store.AcademicUnitMemberCreation
}

func (s *academicUnitMemberRecords) ListPageByAcademicUnit(_ context.Context, options store.AcademicUnitMemberPageOptions) (*store.AcademicUnitMemberPage, error) {
	s.pageOptions = append(s.pageOptions, options)
	page := s.page
	page.Members = slices.Clone(page.Members)
	return &page, nil
}

func (s *academicUnitMemberRecords) CountPageByAcademicUnit(_ context.Context, options store.AcademicUnitMemberPageOptions, maximum int) (int, error) {
	s.countOptions = append(s.countOptions, options)
	return min(max(s.total, len(s.page.Members)), maximum), nil
}

// lastPage is the options of the most recent members page.
func (s *academicUnitMemberRecords) lastPage(t *testing.T) store.AcademicUnitMemberPageOptions {
	t.Helper()
	require.NotEmpty(t, s.pageOptions, "the request never reached the members listing")
	return s.pageOptions[len(s.pageOptions)-1]
}

func (s *academicUnitMemberRecords) Create(_ context.Context, input *store.AcademicUnitMemberCreation) (*model.AcademicUnitMember, error) {
	s.created = append(s.created, input)
	copied := *input.Member
	return &copied, nil
}

type academicAffiliationRecords struct {
	store.AffiliationStore
	listed  []*model.Affiliation
	listErr error
	lists   []model.UserID
	created []*store.AffiliationCreation
}

func (s *academicAffiliationRecords) ListByUser(_ context.Context, userID model.UserID) ([]*model.Affiliation, error) {
	s.lists = append(s.lists, userID)
	return s.listed, s.listErr
}

func (s *academicAffiliationRecords) Create(_ context.Context, input *store.AffiliationCreation) (*model.Affiliation, error) {
	s.created = append(s.created, input)
	copied := *input.Affiliation
	return &copied, nil
}

// academicUserRecords are the Users the service and the authorizer resolve;
// it names the members of a page from the same Users.
type academicUserRecords struct {
	*identityUsers
	// memberships answers ListMemberships, which records each read in
	// membershipReads.
	memberships     []store.UserMembership
	membershipReads []store.UserMembershipListOptions
}

func (s *academicUserRecords) ListMemberships(_ context.Context, options store.UserMembershipListOptions) ([]store.UserMembership, error) {
	s.membershipReads = append(s.membershipReads, options)
	return s.memberships[:min(len(s.memberships), options.Limit)], nil
}

func (s *academicUserRecords) add(users ...*model.User) {
	for _, user := range users {
		s.byID[user.ID] = user
	}
}

// ListDisplayNames names every unarchived user the fake holds.
func (s *academicUserRecords) ListDisplayNames(_ context.Context, ids []model.UserID) ([]store.UserDisplayName, error) {
	names := make([]store.UserDisplayName, 0, len(ids))
	for _, id := range ids {
		if user, ok := s.byID[id]; ok && !user.ArchivedAt.Valid {
			names = append(names, store.UserDisplayName{UserID: id, DisplayName: user.DisplayName})
		}
	}
	return names, nil
}

// ListVisibleDisplayNames sees only through institution-wide visibility.
func (s *academicUserRecords) ListVisibleDisplayNames(ctx context.Context, ids []model.UserID, visibility store.UserVisibilityScope) ([]store.UserDisplayName, error) {
	if !visibility.InstitutionWide {
		return nil, nil
	}
	return s.ListDisplayNames(ctx, ids)
}

func (s *academicUserRecords) ListIdentities(_ context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	identities := make([]store.UserIdentity, 0, len(ids))
	for _, id := range ids {
		if user, ok := s.byID[id]; ok {
			identities = append(identities, store.UserIdentity{ID: id, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email})
		}
	}
	return identities, nil
}

// academicBindingRecords answers the members' Role Bindings scoped to the
// unit and records what it was asked for.
type academicBindingRecords struct {
	store.RoleBindingStore
	summaries []store.UserRoleBindingSummary
	options   []store.RoleBindingSummaryOptions
	teachers  []store.ClassTeachers
	teacherOf []store.ClassTeacherOptions
}

func (s *academicBindingRecords) ListTeachersByClasses(_ context.Context, options store.ClassTeacherOptions) ([]store.ClassTeachers, error) {
	s.teacherOf = append(s.teacherOf, options)
	return s.teachers, nil
}

func (s *academicBindingRecords) ListVisibleSummariesByUsers(_ context.Context, options store.RoleBindingSummaryOptions) ([]store.UserRoleBindingSummary, error) {
	s.options = append(s.options, options)
	return s.summaries, nil
}
