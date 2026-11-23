// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// userFixture is the real user.Service the user handlers call, over
// in-memory stores a test fills and reads back. Handler tests assert what the
// request turned into at the Store, not what a fake application received.
type userFixture struct {
	users    *directoryUserStore
	roles    *directoryRoleStore
	bindings *directoryBindingStore
	files    *pictureFileStore
	pictures *pictureContent
	settings *settingsDocumentStore
	service  *user.Service
}

// newUserFixture builds the fixture with the caller's grants.
func newUserFixture(t *testing.T, grants ...accesstest.Grant) *userFixture {
	t.Helper()
	fixture := &userFixture{
		users:    &directoryUserStore{byID: map[model.UserID]*model.User{}},
		roles:    &directoryRoleStore{},
		bindings: &directoryBindingStore{},
		files:    &pictureFileStore{},
		pictures: &pictureContent{},
		settings: &settingsDocumentStore{},
	}
	authorization := accesstest.New(t, accesstest.Options{Grants: grants, Users: fixture.users})
	service, err := user.New(user.Dependencies{
		Store:      userCatalog{fixture: fixture},
		Authorizer: authorization.Authorizer,
		Auditor:    authorization.Auditor,
		Publisher:  &realtimetest.Publisher{},
		Pictures:   fixture.pictures,
		Now:        time.Now,
	})
	require.NoError(t, err)
	fixture.service = service
	return fixture
}

// institutionWide grants the caller the actions across the Institution.
func institutionWide(userID model.UserID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions}
}

// inAcademicUnit grants the caller the actions in one Academic Unit.
func inAcademicUnit(userID model.UserID, unitID model.AcademicUnitID, actions ...model.Action) accesstest.Grant {
	return accesstest.Grant{UserID: userID, Actions: actions, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String()}
}

type userCatalog struct {
	store.Catalog
	fixture *userFixture
}

func (c userCatalog) User() store.UserStore                 { return c.fixture.users }
func (c userCatalog) Role() store.RoleStore                 { return c.fixture.roles }
func (c userCatalog) RoleBinding() store.RoleBindingStore   { return c.fixture.bindings }
func (c userCatalog) File() store.FileStore                 { return c.fixture.files }
func (c userCatalog) UserSettings() store.UserSettingsStore { return c.fixture.settings }

// directoryUserStore serves the Users a test lists, in order. With more set,
// List adds one filler User past the request's limit, so the page reports
// more and its cursor is written from the last listed User.
type directoryUserStore struct {
	store.UserStore
	byID           map[model.UserID]*model.User
	listed         []*model.User
	more           bool
	total          int
	summaries      []store.UserDirectorySummary
	currentContext *store.CurrentUserContext
	// mfaEnabledAt holds the users with two-step verification on.
	mfaEnabledAt map[model.UserID]time.Time

	listOptions []store.UserListOptions
	update      *store.UserProfileUpdate
}

func (s *directoryUserStore) add(users ...*model.User) {
	for _, user := range users {
		s.byID[user.ID] = user
	}
}

// lastList is the options of the most recent List.
func (s *directoryUserStore) lastList(t *testing.T) store.UserListOptions {
	t.Helper()
	require.NotEmpty(t, s.listOptions, "the request never reached the User directory")
	return s.listOptions[len(s.listOptions)-1]
}

func (s *directoryUserStore) Get(_ context.Context, id model.UserID) (*model.User, error) {
	user, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("user", id.String())
	}
	copied := *user
	return &copied, nil
}

func (s *directoryUserStore) List(_ context.Context, options store.UserListOptions) ([]*model.User, error) {
	s.listOptions = append(s.listOptions, options)
	page := slices.Clone(s.listed)
	if s.more {
		page = append(page, &model.User{ID: model.NewUserID(), Username: "zz-filler"})
	}
	return page, nil
}

func (s *directoryUserStore) Count(_ context.Context, _ store.UserListOptions, maximum int) (int, error) {
	return min(max(s.total, len(s.listed)), maximum), nil
}

func (s *directoryUserStore) ListDirectorySummaries(context.Context, store.UserDirectorySummaryOptions) ([]store.UserDirectorySummary, error) {
	return s.summaries, nil
}

func (s *directoryUserStore) GetCurrentContext(context.Context, model.UserID, int) (*store.CurrentUserContext, error) {
	if s.currentContext == nil {
		return nil, store.NewErrNotFound("user", "")
	}
	return s.currentContext, nil
}

func (s *directoryUserStore) UpdateProfile(_ context.Context, input *store.UserProfileUpdate) (*model.User, error) {
	s.update = input
	updated := *s.byID[input.UserID]
	updated.ApplyProfileChanges(&input.Changes)
	updated.Revision = input.ExpectedRevision + 1
	return &updated, nil
}

// directoryRoleStore knows the Roles a test names.
type directoryRoleStore struct {
	store.RoleStore
	known []model.RoleID
}

func (s *directoryRoleStore) GetByIDs(_ context.Context, ids []model.RoleID) ([]*model.Role, error) {
	roles := []*model.Role{}
	for _, id := range ids {
		if slices.Contains(s.known, id) {
			roles = append(roles, &model.Role{ID: id})
		}
	}
	return roles, nil
}

type directoryBindingStore struct {
	store.RoleBindingStore
	summaries []store.UserRoleBindingSummary
}

func (s *directoryBindingStore) ListVisibleSummariesByUsers(context.Context, store.RoleBindingSummaryOptions) ([]store.UserRoleBindingSummary, error) {
	return s.summaries, nil
}

// pictureFileStore holds one User's profile-picture state and records what
// the picture use cases asked of it.
type pictureFileStore struct {
	store.FileStore
	state     *store.ProfilePictureState
	rendition *model.FileRendition

	renditionName string
	published     *store.ProfilePicturePublication
	removed       *store.ProfilePictureRemoval
}

func (s *pictureFileStore) GetProfilePictureState(_ context.Context, userID model.UserID) (*store.ProfilePictureState, error) {
	if s.state == nil {
		return nil, store.NewErrNotFound("profile_picture", userID.String())
	}
	return s.state, nil
}

func (s *pictureFileStore) CreateUpload(_ context.Context, input *store.FileUploadCreation) (*store.FileUpload, error) {
	return &store.FileUpload{Entry: input.Entry, Revision: input.Revision, Lease: input.Lease}, nil
}

func (s *pictureFileStore) CreateRevisionUpload(_ context.Context, input *store.FileRevisionUploadCreation) (*store.FileUpload, error) {
	return &store.FileUpload{Revision: input.Revision, Lease: input.Lease}, nil
}

func (s *pictureFileStore) PublishProfilePicture(_ context.Context, input *store.ProfilePicturePublication) (*store.ProfilePicturePublicationResult, error) {
	s.published = input
	return &store.ProfilePicturePublicationResult{
		User:     &model.User{ID: input.UserID, CustomProfilePictureFileID: input.EntryID, Revision: 2},
		Revision: &model.FileRevision{ID: input.RevisionID, Availability: model.FileAvailabilityAvailable, Renditions: input.Renditions},
	}, nil
}

func (s *pictureFileStore) RemoveProfilePicture(_ context.Context, input *store.ProfilePictureRemoval) (*model.User, error) {
	s.removed = input
	return &model.User{ID: input.UserID, Revision: 3}, nil
}

func (s *pictureFileStore) GetProfilePictureRendition(_ context.Context, userID model.UserID, name string) (*model.FileRendition, error) {
	s.renditionName = name
	if s.rendition == nil {
		return nil, store.NewErrNotFound("profile_picture", userID.String())
	}
	return s.rendition, nil
}

// pictureState is a stored picture whose renditions all carry checksum.
func pictureState(entryID model.FileEntryID, checksum string) *store.ProfilePictureState {
	revisionID := model.NewFileRevisionID()
	renditions := make([]model.FileRendition, 0, 3)
	for _, size := range []int{128, 256, 512} {
		renditions = append(renditions, model.FileRendition{
			RevisionID: revisionID, Name: fmt.Sprintf("profile_%d", size), MediaType: "image/webp",
			Size: 10, Width: 20, Height: 20, SHA256: checksum,
		})
	}
	return &store.ProfilePictureState{EntryID: entryID, RevisionID: revisionID, Renditions: renditions}
}

// pictureContent stands in for file content, recording the bytes it was
// asked to normalize.
type pictureContent struct {
	body []byte
	size int64
}

func (c *pictureContent) NormalizeAndStoreProfilePicture(_ context.Context, revisionID model.FileRevisionID, body io.Reader, size int64, at time.Time) ([]model.FileRendition, error) {
	read, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	c.body, c.size = read, size
	renditions := make([]model.FileRendition, 0, 3)
	for _, pixels := range []int{128, 256, 512} {
		rendition, err := model.NewFileRendition(model.NewFileRenditionID(), revisionID, fmt.Sprintf("profile_%d", pixels), "image/webp", 10, 20, 20, strings.Repeat("e", 64), at)
		if err != nil {
			return nil, err
		}
		renditions = append(renditions, *rendition)
	}
	return renditions, nil
}

func (c *pictureContent) RemoveProfilePictureRenditions(context.Context, model.FileRevisionID, []model.FileRendition) error {
	return nil
}

func (c *pictureContent) OpenProfilePictureRendition(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("webp")), nil
}

func (c *pictureContent) RenderDefaultProfilePicture(context.Context, string, int) (*user.RenderedProfilePicture, error) {
	return nil, fmt.Errorf("the handler tests store a picture for every read")
}

func (c *pictureContent) GenerateAndStoreDefaultProfilePicture(context.Context, model.FileRevisionID, string, time.Time) ([]model.FileRendition, error) {
	return nil, fmt.Errorf("the handler tests never generate a default picture")
}

// settingsDocumentStore holds the caller's settings document and records the
// replacement it was asked to commit.
type settingsDocumentStore struct {
	store.UserSettingsStore
	document *model.UserSettingsDocument
	result   *store.UserSettingsReplacementResult

	reads       int
	replacement *store.UserSettingsReplacement
	idempotency *store.CommandIdempotency
}

func (s *settingsDocumentStore) Get(context.Context, model.UserID) (*model.UserSettingsDocument, error) {
	s.reads++
	if s.document == nil {
		return nil, store.NewErrNotFound("user_settings", "")
	}
	return s.document.Clone(), nil
}

func (s *settingsDocumentStore) Replace(_ context.Context, input *store.UserSettingsReplacement, command *store.CommandIdempotency) (*store.UserSettingsReplacementResult, error) {
	s.replacement, s.idempotency = input, command
	return s.result, nil
}

// ListDisplayNames names every unarchived user the fake holds.
func (s *directoryUserStore) ListDisplayNames(_ context.Context, ids []model.UserID) ([]store.UserDisplayName, error) {
	names := make([]store.UserDisplayName, 0, len(ids))
	for _, id := range ids {
		user, ok := s.byID[id]
		if !ok || user.ArchivedAt.Valid {
			continue
		}
		name := user.DisplayName
		if name == "" {
			name = user.Username
		}
		names = append(names, store.UserDisplayName{UserID: id, DisplayName: name})
	}
	return names, nil
}

// ListVisibleDisplayNames treats only institution-wide visibility as seeing
// anyone, which is enough to tell a visible person from a hidden one.
func (s *directoryUserStore) ListVisibleDisplayNames(ctx context.Context, ids []model.UserID, visibility store.UserVisibilityScope) ([]store.UserDisplayName, error) {
	if !visibility.InstitutionWide {
		return nil, nil
	}
	return s.ListDisplayNames(ctx, ids)
}

func (s *directoryUserStore) ListMFAStates(_ context.Context, ids []model.UserID) ([]store.UserMFAState, error) {
	states := []store.UserMFAState{}
	for _, id := range ids {
		if at, ok := s.mfaEnabledAt[id]; ok {
			states = append(states, store.UserMFAState{UserID: id, EnabledAt: model.OptionalTimeFrom(at)})
		}
	}
	return states, nil
}

func (s *directoryUserStore) ListIdentities(_ context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	identities := make([]store.UserIdentity, 0, len(ids))
	for _, id := range ids {
		if user, ok := s.byID[id]; ok {
			identities = append(identities, store.UserIdentity{ID: id, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email})
		}
	}
	return identities, nil
}
