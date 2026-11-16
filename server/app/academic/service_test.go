// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// catalogFake is the Store catalog of a test: it returns the store fakes the
// test sets and panics on any other accessor.
type catalogFake struct {
	store.Catalog
	institution        store.InstitutionStore
	academicUnit       store.AcademicUnitStore
	programme          store.ProgrammeStore
	programmeLevel     store.ProgrammeLevelStore
	academicPeriod     store.AcademicPeriodStore
	class              store.ClassStore
	classMember        store.ClassMemberStore
	user               store.UserStore
	affiliation        store.AffiliationStore
	academicUnitMember store.AcademicUnitMemberStore
	roleBinding        store.RoleBindingStore
	examSitting        store.ExamSittingStore
}

func (c *catalogFake) Institution() store.InstitutionStore       { return c.institution }
func (c *catalogFake) AcademicUnit() store.AcademicUnitStore     { return c.academicUnit }
func (c *catalogFake) Programme() store.ProgrammeStore           { return c.programme }
func (c *catalogFake) ProgrammeLevel() store.ProgrammeLevelStore { return c.programmeLevel }
func (c *catalogFake) AcademicPeriod() store.AcademicPeriodStore { return c.academicPeriod }
func (c *catalogFake) Class() store.ClassStore                   { return c.class }
func (c *catalogFake) ClassMember() store.ClassMemberStore       { return c.classMember }
func (c *catalogFake) User() store.UserStore                     { return c.user }
func (c *catalogFake) Affiliation() store.AffiliationStore       { return c.affiliation }
func (c *catalogFake) AcademicUnitMember() store.AcademicUnitMemberStore {
	return c.academicUnitMember
}
func (c *catalogFake) RoleBinding() store.RoleBindingStore { return c.roleBinding }
func (c *catalogFake) ExamSitting() store.ExamSittingStore { return c.examSitting }

// mustNew builds the Service under test; New fails only without a catalog.
func mustNew(deps Dependencies) *Service {
	service, err := New(deps)
	if err != nil {
		panic(err)
	}
	return service
}

// academicUnitPublisherIn returns a recording Publisher that also places each
// call in the log a test shares with its other fakes: "effect",
// "effect-update" and "effect-archive" for the three Academic Unit events and
// "effect-report" for a reported failure.
func academicUnitPublisherIn(events *[]string) *realtimetest.Publisher {
	return &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		switch {
		case call.Method == realtimetest.Report:
			*events = append(*events, "effect-report")
		case call.Event.Name == "academic_unit_updated":
			*events = append(*events, "effect-update")
		case call.Event.Name == "academic_unit_archived":
			*events = append(*events, "effect-archive")
		default:
			*events = append(*events, "effect")
		}
	}}
}

// academicUnitEventCall is the publication of one Academic Unit event: a
// payload-free event for everyone who may view the unit.
func academicUnitEventCall(name string, unitID model.AcademicUnitID) realtimetest.Call {
	return realtimetest.Call{Method: realtimetest.Publish, Event: realtime.Event{
		Name:     name,
		Action:   model.ActionAcademicUnitView,
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()},
	}}
}

// knownProgrammes resolves the Programmes it holds and no other.
type knownProgrammes struct {
	store.ProgrammeStore
	byID map[model.ProgrammeID]*model.Programme
}

func programmesOf(programmes ...*model.Programme) knownProgrammes {
	known := knownProgrammes{byID: map[model.ProgrammeID]*model.Programme{}}
	for _, programme := range programmes {
		known.byID[programme.ID] = programme
	}
	return known
}

func (p knownProgrammes) Get(_ context.Context, id model.ProgrammeID) (*model.Programme, error) {
	if programme, ok := p.byID[id]; ok {
		return programme, nil
	}
	return nil, store.NewErrNotFound("programme", id.String())
}

// knownProgrammeLevels resolves the Programme Levels it holds and no other.
type knownProgrammeLevels struct {
	store.ProgrammeLevelStore
	byID map[model.ProgrammeLevelID]*model.ProgrammeLevel
}

func programmeLevelsOf(levels ...*model.ProgrammeLevel) knownProgrammeLevels {
	known := knownProgrammeLevels{byID: map[model.ProgrammeLevelID]*model.ProgrammeLevel{}}
	for _, level := range levels {
		known.byID[level.ID] = level
	}
	return known
}

func (l knownProgrammeLevels) Get(_ context.Context, id model.ProgrammeLevelID) (*model.ProgrammeLevel, error) {
	if level, ok := l.byID[id]; ok {
		return level, nil
	}
	return nil, store.NewErrNotFound("programme_level", id.String())
}

// knownAcademicPeriods resolves the Academic Periods it holds and no other.
type knownAcademicPeriods struct {
	store.AcademicPeriodStore
	byID map[model.AcademicPeriodID]*model.AcademicPeriod
}

func academicPeriodsOf(periods ...*model.AcademicPeriod) knownAcademicPeriods {
	known := knownAcademicPeriods{byID: map[model.AcademicPeriodID]*model.AcademicPeriod{}}
	for _, period := range periods {
		known.byID[period.ID] = period
	}
	return known
}

func (p knownAcademicPeriods) Get(_ context.Context, id model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	if period, ok := p.byID[id]; ok {
		return period, nil
	}
	return nil, store.NewErrNotFound("academic_period", id.String())
}

// classWorld is the structure the real authorizer resolves a Class through:
// a Programme in one Academic Unit, its Levels, and an Institution-owned
// Academic Period.
type classWorld struct {
	unitID    model.AcademicUnitID
	programme *model.Programme
	levels    []*model.ProgrammeLevel
	period    *model.AcademicPeriod
}

// newClassWorld builds a unit with one Programme holding levels Programme
// Levels and one Academic Period.
func newClassWorld(levels int) classWorld {
	unitID := model.NewAcademicUnitID()
	world := classWorld{
		unitID:    unitID,
		programme: &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: unitID},
		period: &model.AcademicPeriod{
			ID: model.NewAcademicPeriodID(), Owner: model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()),
			StartsAt: model.TimeFromMillis(0), EndsAt: model.TimeFromMillis(1_000_000),
		},
	}
	for range levels {
		world.levels = append(world.levels, &model.ProgrammeLevel{ID: model.NewProgrammeLevelID(), ProgrammeID: world.programme.ID})
	}
	return world
}

// class returns a Class of the world's first Level in its Period.
func (w classWorld) class() *model.Class {
	return &model.Class{ID: model.NewClassID(), ProgrammeLevelID: w.levels[0].ID, AcademicPeriodID: w.period.ID, DisplayName: "Class"}
}

// access builds the real authorizer over the grants, resolving the world and
// the Classes the test's own store holds.
func (w classWorld) access(t *testing.T, events *[]string, classes store.ClassStore, grants ...accesstest.Grant) *accesstest.Fixture {
	t.Helper()
	return accessWith(t, events, accesstest.Options{
		Grants:          grants,
		Units:           []*model.AcademicUnit{{ID: w.unitID, Name: "unit"}},
		Classes:         classes,
		AcademicPeriods: academicPeriodsOf(w.period),
		Programmes:      programmesOf(w.programme),
		ProgrammeLevels: programmeLevelsOf(w.levels...),
	})
}

// sittingTargetsFake answers the Sitting boards a Class Membership change
// refreshes and records each Class it was asked for.
type sittingTargetsFake struct {
	store.ExamSittingStore
	targets map[model.ClassID][]store.ExamSittingInvalidationTarget
	listed  []model.ClassID
}

func (s *sittingTargetsFake) ListInvalidationTargetsByClass(
	_ context.Context,
	classID model.ClassID,
	_ model.ExamSittingID,
	_ int,
) ([]store.ExamSittingInvalidationTarget, error) {
	s.listed = append(s.listed, classID)
	return s.targets[classID], nil
}

// relationshipUserStoreTestFake holds an active User, at revision 1, for
// every identifier, so mail can be prepared for any member.
type relationshipUserStoreTestFake struct{ store.UserStore }

func (relationshipUserStoreTestFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	parsed, err := model.ParseUserID(id.String())
	if err != nil {
		return nil, err
	}
	return mailTestUser(model.Principal{UserID: parsed}, time.UnixMilli(500)), nil
}

func (relationshipUserStoreTestFake) ListIdentities(_ context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	identities := make([]store.UserIdentity, 0, len(ids))
	for _, id := range ids {
		identities = append(identities, store.UserIdentity{ID: id, Username: "user-" + id.String(), DisplayName: "User " + id.String(), Email: id.String() + "@example.edu"})
	}
	return identities, nil
}

func mailTestUser(principal model.Principal, at time.Time) *model.User {
	user := &model.User{Email: "operator@example.test", DisplayName: "Operator", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(principal.UserID, at.Add(-time.Hour))
	return user
}

// mailRendererFake renders every message the real Composer freezes and
// records what it was asked to render, placing "render-mail" in the log a
// test shares with its other fakes.
type mailRendererFake struct {
	events   *[]string
	requests []mail.RenderRequest
}

func (r *mailRendererFake) Render(request mail.RenderRequest) (mail.FrozenContent, error) {
	if r.events != nil {
		*r.events = append(*r.events, "render-mail")
	}
	r.requests = append(r.requests, request)
	return mail.FrozenContent{Subject: "Notice", Text: "Notice", HTML: "<p>Notice</p>"}, nil
}

// classTransition returns the Class details of the one message rendered.
func (r *mailRendererFake) classTransition(t *testing.T) mail.ClassTransitionDetails {
	t.Helper()
	require.Len(t, r.requests, 1, "rendered mail")
	details, ok := r.requests[0].Presentation.(mail.ClassTransitionDetails)
	require.True(t, ok, "rendered mail = %#v", r.requests[0])
	return details
}

// newMail builds the production mail Composer over an enabled sender, so
// every eligible recipient's message is rendered.
func newMail(t *testing.T, events *[]string) (*mail.Composer, *mailRendererFake) {
	t.Helper()
	renderer := &mailRendererFake{events: events}
	composer, err := mail.NewComposer(mail.ComposerDependencies{
		Renderer: renderer,
		Sender:   &mailSenderFake{enabled: true, from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}},
		Sealer:   mailTestSealer(t),
	})
	require.NoError(t, err)
	return composer, renderer
}

type mailSenderFake struct {
	enabled bool
	from    mail.Address
}

func (s *mailSenderFake) Enabled() bool      { return s.enabled }
func (s *mailSenderFake) From() mail.Address { return s.from }
func (s *mailSenderFake) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, nil
}

func mailTestSealer(t *testing.T) *secretseal.Sealer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	value, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	return value
}

// newCaller returns a signed-in caller and the invocation it makes. The real
// authorizer refuses a zero Principal.
func newCaller() (model.Principal, invocation.Invocation) {
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now(),
	}
	return principal, invocation.New(principal, model.RequestMetadata{})
}

// accessWith builds the real authorizer and auditor the options describe and
// places what they do in events: "grants" for each read of the caller's Role
// Bindings, "decision:<action>:<status>" for a recorded decision, and
// "audit-begin:<action>" and "audit-<status>" for the audit of a mutation.
func accessWith(t *testing.T, events *[]string, options accesstest.Options) *accesstest.Fixture {
	t.Helper()
	options.Observe = func(event string) {
		if events != nil {
			*events = append(*events, event)
		}
	}
	return accesstest.New(t, options)
}

// institutionGrant grants the actions Institution-wide.
func institutionGrant(userID model.UserID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions}
}

// unitGrant grants the actions in one Academic Unit.
func unitGrant(userID model.UserID, unitID model.AcademicUnitID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}
}

// decisions describes each recorded authorization decision as
// "<action> <resource type>:<resource id> <status>".
func decisions(fixture *accesstest.Fixture) []string {
	var described []string
	for _, event := range fixture.Audits.Decisions() {
		described = append(described, event.Action+" "+string(event.Resource.Type)+":"+event.Resource.ID+" "+string(event.Status))
	}
	return described
}

// onlyAttempt returns the one mutation attempt the auditor recorded.
func onlyAttempt(t *testing.T, fixture *accesstest.Fixture) *model.AuditEvent {
	t.Helper()
	attempts := fixture.Audits.Attempts()
	require.Len(t, attempts, 1, "mutation attempts")
	return attempts[0]
}
