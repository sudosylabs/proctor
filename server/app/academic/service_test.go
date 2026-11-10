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
	"github.com/sudosylabs/proctor/server/app/mail"
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

// mustNew builds the Service under test; New fails only without a catalog.
func mustNew(deps Dependencies) *Service {
	service, err := New(deps)
	if err != nil {
		panic(err)
	}
	return service
}

// effectsBase supplies the Effects methods a test does not record, one level
// below the fakes so their own methods win.
type effectsBase struct{ Effects }

// academicUnitEffects joins the Academic Unit change and failure-report fakes
// into the one Effects collaborator.
type academicUnitEffects struct {
	effectsBase
	*academicUnitCommandEffectsFake
	*academicUnitEffectFailureReporterFake
}

type relationshipUserStoreTestFake struct{ store.UserStore }

func (relationshipUserStoreTestFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	parsed, err := model.ParseUserID(id.String())
	if err != nil {
		return nil, err
	}
	return &model.User{ID: parsed, Revision: 1}, nil
}

func (relationshipUserStoreTestFake) ListIdentities(_ context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	identities := make([]store.UserIdentity, 0, len(ids))
	for _, id := range ids {
		identities = append(identities, store.UserIdentity{ID: id, Username: "user-" + id.String(), DisplayName: "User " + id.String(), Email: id.String() + "@example.edu"})
	}
	return identities, nil
}

type relationshipMailPreparerTestFake struct {
	Mail
	requests []mail.RelationshipTransitionPreparation
}

func (f *relationshipMailPreparerTestFake) PrepareRelationshipTransition(request mail.RelationshipTransitionPreparation) (*store.PreparedMail, error) {
	f.requests = append(f.requests, request)
	return &store.PreparedMail{}, nil
}

func mailTestUser(principal model.Principal, at time.Time) *model.User {
	user := &model.User{Email: "operator@example.test", DisplayName: "Operator", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(principal.UserID, at.Add(-time.Hour))
	return user
}

// newDirectMailPreparer builds the production mail Composer for a test.
func newDirectMailPreparer(renderer mail.Renderer, sender mail.Sender, sealer *secretseal.Sealer, institution func() string) (*mail.Composer, error) {
	return mail.NewComposer(mail.ComposerDependencies{Renderer: renderer, Sender: sender, Sealer: sealer, Institution: institution})
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

type classTransitionRendererFake struct {
	details mail.ClassTransitionDetails
	calls   int
}

func (r *classTransitionRendererFake) Render(request mail.RenderRequest) (mail.FrozenContent, error) {
	details, ok := request.Presentation.(mail.ClassTransitionDetails)
	if !ok {
		return mail.FrozenContent{}, nil
	}
	r.calls++
	r.details = details
	return mail.FrozenContent{Subject: "Class changed", Text: details.ClassDisplayName, HTML: "<p>Class changed</p>"}, nil
}
