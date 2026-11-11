// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type pictureStoreFake struct {
	store.FileStore
	user                *model.User
	publication         *store.ProfilePicturePublication
	defaultPublication  *store.DefaultProfilePicturePublication
	defaultPublications int
	rendition           *model.FileRendition
	state               *store.ProfilePictureState
	events              *[]string
	created             bool
	lookupName          string
	discarded           bool
	removal             *store.ProfilePictureRemoval
	publishErr          error
	removeErr           error
	stateErr            error
}

func TestNormalizedProfilePictureEqualityRequiresTheCompleteCanonicalManifest(t *testing.T) {
	t.Parallel()

	current := model.FileRendition{Name: "profile_128", MediaType: "image/webp", Size: 42, Width: 128, Height: 128, SHA256: strings.Repeat("a", 64)}
	state := &store.ProfilePictureState{Renditions: []model.FileRendition{current}}
	require.True(t, sameNormalizedProfilePicture(state, []model.FileRendition{current}), "identical canonical manifest was not a no-op")
	for _, changed := range []model.FileRendition{
		{Name: current.Name, MediaType: "image/png", Size: current.Size, Width: current.Width, Height: current.Height, SHA256: current.SHA256},
		{Name: current.Name, MediaType: current.MediaType, Size: current.Size + 1, Width: current.Width, Height: current.Height, SHA256: current.SHA256},
		{Name: current.Name, MediaType: current.MediaType, Size: current.Size, Width: current.Width + 1, Height: current.Height, SHA256: current.SHA256},
		{Name: current.Name, MediaType: current.MediaType, Size: current.Size, Width: current.Width, Height: current.Height + 1, SHA256: current.SHA256},
	} {
		require.False(t, sameNormalizedProfilePicture(state, []model.FileRendition{changed}), "noncanonical manifest treated as no-op: %#v", changed)
	}
}

func (s *pictureStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	copy := *s.user
	return &copy, nil
}
func (s *pictureStoreFake) CreateUpload(_ context.Context, input *store.FileUploadCreation) (*store.FileUpload, error) {
	s.created = true
	if s.events != nil {
		*s.events = append(*s.events, "create-upload")
	}
	return &store.FileUpload{Entry: input.Entry, Revision: input.Revision, Lease: input.Lease}, nil
}
func (s *pictureStoreFake) CreateRevisionUpload(_ context.Context, input *store.FileRevisionUploadCreation) (*store.FileUpload, error) {
	s.created = true
	if s.events != nil {
		*s.events = append(*s.events, "create-revision-upload")
	}
	return &store.FileUpload{Revision: input.Revision, Lease: input.Lease}, nil
}
func (s *pictureStoreFake) PublishProfilePicture(_ context.Context, input *store.ProfilePicturePublication) (*store.ProfilePicturePublicationResult, error) {
	s.publication = input
	if s.events != nil {
		*s.events = append(*s.events, "publish")
	}
	if s.publishErr != nil {
		return nil, s.publishErr
	}
	copy := *s.user
	copy.CustomProfilePictureFileID = input.EntryID
	copy.ProfilePictureChangedAt = model.OptionalTimeFrom(input.ChangedAt)
	copy.Revision++
	return &store.ProfilePicturePublicationResult{User: &copy, Revision: &model.FileRevision{ID: input.RevisionID, Availability: model.FileAvailabilityAvailable, Renditions: input.Renditions}}, nil
}
func (s *pictureStoreFake) PublishDefaultProfilePicture(_ context.Context, input *store.DefaultProfilePicturePublication) (*store.ProfilePicturePublicationResult, error) {
	s.defaultPublication = input
	s.defaultPublications++
	copy := *s.user
	copy.DefaultProfilePictureFileID = input.EntryID
	copy.Revision++
	return &store.ProfilePicturePublicationResult{User: &copy, Revision: &model.FileRevision{ID: input.RevisionID, Availability: model.FileAvailabilityAvailable, Renditions: input.Renditions}}, nil
}
func (s *pictureStoreFake) GetProfilePictureRendition(_ context.Context, _ model.UserID, name string) (*model.FileRendition, error) {
	s.lookupName = name
	if s.events != nil {
		*s.events = append(*s.events, "get-rendition")
	}
	if s.rendition == nil {
		return nil, store.NewErrNotFound("profile_picture", s.user.ID.String())
	}
	return s.rendition, nil
}
func (s *pictureStoreFake) GetProfilePictureState(context.Context, model.UserID) (*store.ProfilePictureState, error) {
	if s.stateErr != nil {
		return nil, s.stateErr
	}
	if s.state == nil {
		return nil, store.NewErrNotFound("profile_picture", s.user.ID.String())
	}
	return s.state, nil
}
func (s *pictureStoreFake) DiscardProfilePictureUpload(context.Context, *store.ProfilePictureUploadDiscard) error {
	s.discarded = true
	if s.events != nil {
		*s.events = append(*s.events, "discard-upload")
	}
	return nil
}
func (s *pictureStoreFake) RemoveProfilePicture(_ context.Context, input *store.ProfilePictureRemoval) (*model.User, error) {
	s.removal = input
	if s.events != nil {
		*s.events = append(*s.events, "remove-with-audit")
	}
	if s.removeErr != nil {
		return nil, s.removeErr
	}
	updated := *s.user
	updated.CustomProfilePictureFileID = ""
	updated.ProfilePictureChangedAt = model.OptionalTimeFrom(input.ChangedAt)
	updated.Revision++
	return &updated, nil
}

type pictureContentFake struct {
	normalized      bool
	workErr         error
	events          *[]string
	openedRevision  model.FileRevisionID
	openedRendition model.FileRenditionID
	removed         bool
}

type profilePictureAuditorFake struct {
	events    *[]string
	beginID   string
	failCode  string
	beginErr  error
	action    model.Action
	resource  model.Resource
	operation string
	value     map[string]any
	prior     map[string]any
}

func (a *profilePictureAuditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	// This fake keeps neither a scope nor a subject, so it refuses a Mutation
	// that names one instead of dropping it.
	// It stands in for access.MutationAuditAdapter, which records both.
	if attempt.ScopeType != "" || attempt.ScopeID != "" || !attempt.Subject.IsZero() {
		return "", apperror.New("audit.internal")
	}

	a.action, a.resource, a.operation, a.value, a.prior = attempt.Action, attempt.Resource, attempt.Operation, attempt.Value, attempt.Prior
	if a.events != nil {
		*a.events = append(*a.events, "audit-begin")
	}
	return model.AuditEventID(a.beginID), a.beginErr
}
func (a *profilePictureAuditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	a.failCode = code
	if a.events != nil {
		*a.events = append(*a.events, "audit-fail")
	}
	return nil
}

type profilePictureDefaultJobsFake struct {
	userID model.UserID
	count  int
	err    error
}

func (p *profilePictureDefaultJobsFake) ProposeDefaultProfilePicture(_ context.Context, userID model.UserID, _ time.Time) error {
	p.userID = userID
	p.count++
	return p.err
}

func (s *pictureContentFake) normalizeAndStoreProfilePicture(_ context.Context, revisionID model.FileRevisionID, _ io.Reader, _ int64, at time.Time) ([]model.FileRendition, error) {
	s.normalized = true
	if s.events != nil {
		*s.events = append(*s.events, "normalize")
	}
	if s.workErr != nil {
		return nil, s.workErr
	}
	result := make([]model.FileRendition, 0, 3)
	for _, size := range []int{128, 256, 512} {
		rendition, err := model.NewFileRendition(model.NewFileRenditionID(), revisionID, fmt.Sprintf("profile_%d", size), "image/webp", 10, 20, 20, strings.Repeat("a", 64), at)
		if err != nil {
			return nil, err
		}
		result = append(result, *rendition)
	}
	return result, nil
}
func (s *pictureContentFake) generateAndStoreDefaultProfilePicture(_ context.Context, revisionID model.FileRevisionID, _ string, at time.Time) ([]model.FileRendition, error) {
	if s.workErr != nil {
		return nil, s.workErr
	}
	result := make([]model.FileRendition, 0, 3)
	for _, size := range []int{128, 256, 512} {
		rendition, err := model.NewFileRendition(model.NewFileRenditionID(), revisionID, fmt.Sprintf("profile_%d", size), "image/webp", 10, size, size, strings.Repeat("d", 64), at)
		if err != nil {
			return nil, err
		}
		result = append(result, *rendition)
	}
	return result, nil
}
func (s *pictureContentFake) renderDefaultProfilePicture(context.Context, string, int) (*RenderedProfilePicture, error) {
	if s.workErr != nil {
		return nil, s.workErr
	}
	return &RenderedProfilePicture{Body: io.NopCloser(strings.NewReader("default")), MediaType: "image/webp", Size: 7, SHA256: strings.Repeat("d", 64)}, nil
}
func (s *pictureContentFake) openProfilePictureRendition(_ context.Context, revisionID model.FileRevisionID, renditionID model.FileRenditionID) (io.ReadCloser, error) {
	s.openedRevision = revisionID
	s.openedRendition = renditionID
	if s.events != nil {
		*s.events = append(*s.events, "open")
	}
	return io.NopCloser(strings.NewReader("webp")), nil
}
func (s *pictureContentFake) removeProfilePictureRenditions(context.Context, model.FileRevisionID, []model.FileRendition) error {
	s.removed = true
	if s.events != nil {
		*s.events = append(*s.events, "remove-staged")
	}
	return nil
}

type pictureUploadFilesFake struct{ state *pictureContentFake }

func (f pictureUploadFilesFake) NormalizeAndStoreProfilePicture(ctx context.Context, revisionID model.FileRevisionID, body io.Reader, size int64, at time.Time) ([]model.FileRendition, error) {
	return f.state.normalizeAndStoreProfilePicture(ctx, revisionID, body, size, at)
}

func (f pictureUploadFilesFake) RemoveProfilePictureRenditions(ctx context.Context, revisionID model.FileRevisionID, renditions []model.FileRendition) error {
	return f.state.removeProfilePictureRenditions(ctx, revisionID, renditions)
}

type pictureReadFilesFake struct{ state *pictureContentFake }

func (f pictureReadFilesFake) OpenProfilePictureRendition(ctx context.Context, revisionID model.FileRevisionID, renditionID model.FileRenditionID) (io.ReadCloser, error) {
	return f.state.openProfilePictureRendition(ctx, revisionID, renditionID)
}

type defaultPictureRenderFilesFake struct{ state *pictureContentFake }

func (f defaultPictureRenderFilesFake) RenderDefaultProfilePicture(ctx context.Context, seed string, size int) (*RenderedProfilePicture, error) {
	return f.state.renderDefaultProfilePicture(ctx, seed, size)
}

type defaultPictureGenerationFilesFake struct{ state *pictureContentFake }

func (f defaultPictureGenerationFilesFake) GenerateAndStoreDefaultProfilePicture(ctx context.Context, revisionID model.FileRevisionID, seed string, at time.Time) ([]model.FileRendition, error) {
	return f.state.generateAndStoreDefaultProfilePicture(ctx, revisionID, seed, at)
}

func TestUploadProfilePictureNormalizesPrivateRenditionsBeforePublishing(t *testing.T) {
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	events := []string{}
	persistence := &pictureStoreFake{user: user, events: &events}
	content := &pictureContentFake{events: &events}
	nowCalls := 0
	service := newProfilePictureServiceForTest(persistence, persistence, content, &authorizerFake{events: &events}, &profilePictureAuditorFake{events: &events, beginID: model.NewID()}, publisherIn(&events), nil, func() time.Time {
		nowCalls++
		return at.Add(time.Duration(nowCalls-1) * time.Minute)
	})

	source := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			source.Set(x, y, color.NRGBA{R: 200, G: 10, B: 20, A: 255})
		}
	}
	var input bytes.Buffer
	require.NoError(t, png.Encode(&input, source))
	updated, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, ExpectedRevision: user.Revision, Body: bytes.NewReader(input.Bytes()), Size: int64(input.Len())})
	require.NoError(t, err)
	require.False(t, updated.CustomProfilePictureFileID.IsZero(), "upload result: user=%#v publication=%#v normalized", updated, persistence.publication)
	require.Len(t, persistence.publication.Renditions, 3, "upload result: user=%#v publication=%#v normalized", updated, persistence.publication)
	require.True(t, content.normalized, "upload result: user=%#v publication=%#v normalized", updated, persistence.publication)
	require.True(t, updated.ProfilePictureChangedAt.Time.Equal(at.Add(time.Minute)))
	wantEvents := []string{"authorize-profile-picture-write", "create-upload", "normalize", "audit-begin", "publish", "effect"}
	require.Equal(t, fmt.Sprint(wantEvents), fmt.Sprint(events), "events")
	for _, rendition := range persistence.publication.Renditions {
		require.Equal(t, 20, rendition.Width, "%s dimensions = %dx%d; small images must not be upscaled", rendition.Name, rendition.Width, rendition.Height)
		require.Equal(t, 20, rendition.Height, "%s dimensions = %dx%d; small images must not be upscaled", rendition.Name, rendition.Width, rendition.Height)
	}
}

func TestReplaceProfilePictureKeepsTheActiveFileEntry(t *testing.T) {
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	user.CustomProfilePictureFileID = model.NewFileEntryID()
	events := []string{}
	persistence := &pictureStoreFake{user: user, state: profilePictureState(user.CustomProfilePictureFileID, strings.Repeat("b", 64)), events: &events}
	content := &pictureContentFake{events: &events}
	auditor := &profilePictureAuditorFake{events: &events, beginID: model.NewID()}
	publisher := publisherIn(&events)
	service := newProfilePictureServiceForTest(persistence, persistence, content, &authorizerFake{events: &events}, auditor, publisher, nil, func() time.Time { return at })

	updated, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, ExpectedRevision: user.Revision, ExpectedSHA256: strings.Repeat("b", 64), Body: strings.NewReader("image"), Size: 5})
	require.NoError(t, err)
	require.Equal(t, user.CustomProfilePictureFileID, updated.CustomProfilePictureFileID, "replacement changed file entry: user")
	require.Equal(t, user.CustomProfilePictureFileID, persistence.publication.EntryID, "replacement changed file entry: user")
	wantEvents := []string{"authorize-profile-picture-write", "create-revision-upload", "normalize", "audit-begin", "publish", "effect"}
	require.Equal(t, fmt.Sprint(wantEvents), fmt.Sprint(events), "replacement events/change = %v / %#v", events, publisher.Calls())
	// Contract: a replacement publishes one change for the User, naming the
	// File Entry it kept, and reports no failure.
	require.Equal(t, []realtimetest.Call{profilePictureChangedCall(updated, user.ID, user.CustomProfilePictureFileID)}, publisher.Calls())
	auditData, err := json.Marshal([]map[string]any{auditor.value, auditor.prior})
	require.NoError(t, err)
	require.Equal(t, model.ActionUserProfilePictureManage, auditor.action, "unsafe or incorrect audit projection: action=%s resource=%#v data", auditor.action, auditor.resource)
	require.Equal(t, model.ResourceUser, auditor.resource.Type, "unsafe or incorrect audit projection: action=%s resource=%#v data", auditor.action, auditor.resource)
	require.Equal(t, user.ID.String(), auditor.resource.ID, "unsafe or incorrect audit projection: action=%s resource=%#v data", auditor.action, auditor.resource)
	require.NotContains(t, string(auditData), strings.Repeat("b", 64), "unsafe or incorrect audit projection: action=%s resource=%#v data", auditor.action, auditor.resource)
	require.NotContains(t, string(auditData), "storage", "unsafe or incorrect audit projection: action=%s resource=%#v data", auditor.action, auditor.resource)
}

func TestReplaceProfilePictureRequiresCurrentETag(t *testing.T) {
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), time.Now())
	user.CustomProfilePictureFileID = model.NewFileEntryID()
	persistence := &pictureStoreFake{user: user, state: profilePictureState(user.DefaultProfilePictureFileID, strings.Repeat("d", 64))}
	events := []string{}
	service := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{}, &authorizerFake{events: &events}, &profilePictureAuditorFake{}, &realtimetest.Publisher{}, nil, time.Now)

	_, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, Body: strings.NewReader("image"), Size: 5})
	require.True(t, apperror.Is(err, "request.invalid"), "Upload() error = %v, want request.invalid", err)
	require.False(t, persistence.created, "replacement created durable upload state without If-Match")
}

func TestReplaceProfilePictureRejectsAStaleETagBeforeCreatingUploadState(t *testing.T) {
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), time.Now())
	user.CustomProfilePictureFileID = model.NewFileEntryID()
	persistence := &pictureStoreFake{user: user, state: profilePictureState(user.CustomProfilePictureFileID, strings.Repeat("a", 64))}
	events := []string{}
	service := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{}, &authorizerFake{events: &events}, &profilePictureAuditorFake{}, &realtimetest.Publisher{}, nil, time.Now)

	_, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, ExpectedSHA256: strings.Repeat("b", 64), Body: strings.NewReader("image"), Size: 5})
	require.True(t, apperror.Is(err, "user.conflict"), "Upload() error = %v, want user.conflict", err)
	require.False(t, persistence.created, "stale replacement created durable upload state")
}

func TestReplaceProfilePictureWithIdenticalNormalizedContentIsANoOp(t *testing.T) {
	at := time.Now()
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	user.CustomProfilePictureFileID = model.NewFileEntryID()
	events := []string{}
	state := profilePictureState(user.CustomProfilePictureFileID, strings.Repeat("a", 64))
	persistence := &pictureStoreFake{user: user, state: state, events: &events}
	content := &pictureContentFake{events: &events}
	publisher := publisherIn(&events)
	service := newProfilePictureServiceForTest(persistence, persistence, content, &authorizerFake{events: &events}, &profilePictureAuditorFake{events: &events, beginID: model.NewID()}, publisher, nil, func() time.Time { return at })

	result, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, ExpectedSHA256: strings.Repeat("a", 64), Body: strings.NewReader("image"), Size: 5})
	require.NoError(t, err)
	require.Equal(t, user.ID, result.ID, "identical replacement changed state: result=%#v store=%#v effects=%#v", result, persistence, publisher.Calls())
	require.Equal(t, user.Revision, result.Revision, "identical replacement changed state: result=%#v store=%#v effects=%#v", result, persistence, publisher.Calls())
	require.True(t, persistence.discarded, "identical replacement changed state: result=%#v store=%#v effects=%#v", result, persistence, publisher.Calls())
	require.True(t, content.removed, "identical replacement changed state: result=%#v store=%#v effects=%#v", result, persistence, publisher.Calls())
	require.Nil(t, persistence.publication, "identical replacement changed state: result=%#v store=%#v effects=%#v", result, persistence, publisher.Calls())
	require.Empty(t, publisher.Calls(), "identical replacement changed state: result=%#v store=%#v effects=%#v", result, persistence, publisher.Calls())
	want := []string{"authorize-profile-picture-write", "create-revision-upload", "normalize", "remove-staged", "discard-upload"}
	require.Equal(t, fmt.Sprint(want), fmt.Sprint(events), "events")
}

func TestUploadIdenticalToGeneratedDefaultIsANoOp(t *testing.T) {
	at := time.Now()
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	user.DefaultProfilePictureFileID = model.NewFileEntryID()
	events := []string{}
	persistence := &pictureStoreFake{user: user, state: profilePictureState(user.DefaultProfilePictureFileID, strings.Repeat("a", 64)), events: &events}
	content := &pictureContentFake{events: &events}
	publisher := publisherIn(&events)
	service := newProfilePictureServiceForTest(persistence, persistence, content, &authorizerFake{events: &events}, &profilePictureAuditorFake{events: &events, beginID: model.NewID()}, publisher, nil, func() time.Time { return at })

	result, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, ExpectedSHA256: strings.Repeat("a", 64), Body: strings.NewReader("image"), Size: 5})
	require.NoError(t, err)
	require.Equal(t, user.Revision, result.Revision, "default-identical upload changed visible state: result=%#v store=%#v effect=%#v", result, persistence, publisher.Calls())
	require.True(t, persistence.discarded, "default-identical upload changed visible state: result=%#v store=%#v effect=%#v", result, persistence, publisher.Calls())
	require.Nil(t, persistence.publication, "default-identical upload changed visible state: result=%#v store=%#v effect=%#v", result, persistence, publisher.Calls())
	require.Empty(t, publisher.Calls(), "default-identical upload changed visible state: result=%#v store=%#v effect=%#v", result, persistence, publisher.Calls())
	want := []string{"authorize-profile-picture-write", "create-upload", "normalize", "remove-staged", "discard-upload"}
	require.Equal(t, fmt.Sprint(want), fmt.Sprint(events), "events")
}

func TestRemoveProfilePictureArchivesCustomAndPublishesDefaultAfterCommit(t *testing.T) {
	at := time.Date(2026, time.August, 9, 15, 0, 0, 0, time.UTC)
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	user.CustomProfilePictureFileID = model.NewFileEntryID()
	user.DefaultProfilePictureFileID = model.NewFileEntryID()
	events := []string{}
	state := profilePictureState(user.CustomProfilePictureFileID, strings.Repeat("a", 64))
	persistence := &pictureStoreFake{user: user, state: state, events: &events}
	publisher := publisherIn(&events)
	service := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{}, &authorizerFake{events: &events}, &profilePictureAuditorFake{events: &events, beginID: model.NewID()}, publisher, nil, func() time.Time { return at })

	updated, err := service.RemoveProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), RemoveProfilePictureCommand{UserID: user.ID, ExpectedSHA256: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.True(t, updated.CustomProfilePictureFileID.IsZero(), "removal result/input/effect = %#v / %#v / %#v", updated, persistence.removal, publisher.Calls())
	require.Equal(t, user.DefaultProfilePictureFileID, updated.DefaultProfilePictureFileID, "removal result/input/effect = %#v / %#v / %#v", updated, persistence.removal, publisher.Calls())
	require.Equal(t, user.CustomProfilePictureFileID, persistence.removal.EntryID, "removal result/input/effect = %#v / %#v / %#v", updated, persistence.removal, publisher.Calls())
	// Contract: a removal publishes one change naming the default File Entry
	// that is active again, and reports no failure.
	require.Equal(t, []realtimetest.Call{profilePictureChangedCall(updated, user.ID, user.DefaultProfilePictureFileID)}, publisher.Calls())
	want := []string{"authorize-profile-picture-write", "audit-begin", "remove-with-audit", "effect"}
	require.Equal(t, fmt.Sprint(want), fmt.Sprint(events), "events")
}

func TestUploadAfterRemovalCreatesANewCustomFileEntry(t *testing.T) {
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), time.Now())
	user.DefaultProfilePictureFileID = model.NewFileEntryID()
	events := []string{}
	persistence := &pictureStoreFake{user: user, state: profilePictureState(user.DefaultProfilePictureFileID, strings.Repeat("d", 64))}
	service := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{}, &authorizerFake{events: &events}, &profilePictureAuditorFake{beginID: model.NewID()}, &realtimetest.Publisher{}, nil, time.Now)

	updated, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, ExpectedSHA256: strings.Repeat("d", 64), Body: strings.NewReader("image"), Size: 5})
	require.NoError(t, err)
	require.False(t, updated.CustomProfilePictureFileID.IsZero(), "upload reused the retained default entry: %#v / %#v", updated, persistence.publication)
	require.NotEqual(t, user.DefaultProfilePictureFileID, updated.CustomProfilePictureFileID, "upload reused the retained default entry: %#v / %#v", updated, persistence.publication)
	require.NotEqual(t, user.DefaultProfilePictureFileID, persistence.publication.EntryID, "upload reused the retained default entry: %#v / %#v", updated, persistence.publication)
}

func TestProfilePictureAuditFailurePreventsVisibleMutationAndEvent(t *testing.T) {
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), time.Now())
	events := []string{}
	persistence := &pictureStoreFake{user: user, events: &events}
	publisher := publisherIn(&events)
	auditor := &profilePictureAuditorFake{events: &events, beginErr: apperror.New("audit.unavailable")}
	service := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{events: &events}, &authorizerFake{events: &events}, auditor, publisher, nil, time.Now)

	_, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, Body: strings.NewReader("image"), Size: 5})
	require.True(t, apperror.Is(err, "audit.unavailable"), "audit failure result: err=%v publication=%#v effect=%#v", err, persistence.publication, publisher.Calls())
	require.Nil(t, persistence.publication, "audit failure result: err=%v publication=%#v effect=%#v", err, persistence.publication, publisher.Calls())
	require.Empty(t, publisher.Calls(), "audit failure result: err=%v publication=%#v effect=%#v", err, persistence.publication, publisher.Calls())
	want := []string{"authorize-profile-picture-write", "create-upload", "normalize", "audit-begin"}
	require.Equal(t, fmt.Sprint(want), fmt.Sprint(events), "events")
}

func TestProfilePictureCommitFailureFailsAuditAndPublishesNoEvent(t *testing.T) {
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), time.Now())
	events := []string{}
	persistence := &pictureStoreFake{user: user, events: &events, publishErr: store.NewErrConflict("profile_picture", "changed", nil)}
	auditor := &profilePictureAuditorFake{events: &events, beginID: model.NewID()}
	publisher := publisherIn(&events)
	service := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{events: &events}, &authorizerFake{events: &events}, auditor, publisher, nil, time.Now)

	_, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, Body: strings.NewReader("image"), Size: 5})
	require.True(t, apperror.Is(err, "user.conflict"), "commit failure result: err=%v audit=%#v effect=%#v", err, auditor, publisher.Calls())
	require.Equal(t, "user.conflict", auditor.failCode, "commit failure result: err=%v audit=%#v effect=%#v", err, auditor, publisher.Calls())
	require.Empty(t, publisher.Calls(), "commit failure result: err=%v audit=%#v effect=%#v", err, auditor, publisher.Calls())
	want := []string{"authorize-profile-picture-write", "create-upload", "normalize", "audit-begin", "publish", "audit-fail"}
	require.Equal(t, fmt.Sprint(want), fmt.Sprint(events), "events")
}

func profilePictureState(entryID model.FileEntryID, checksum string) *store.ProfilePictureState {
	revisionID := model.NewFileRevisionID()
	renditions := make([]model.FileRendition, 0, 3)
	for _, size := range []int{128, 256, 512} {
		renditions = append(renditions, model.FileRendition{RevisionID: revisionID, Name: fmt.Sprintf("profile_%d", size), MediaType: "image/webp", Size: 10, Width: 20, Height: 20, SHA256: checksum})
	}
	return &store.ProfilePictureState{EntryID: entryID, RevisionID: revisionID, Renditions: renditions}
}

func TestUploadProfilePictureAuthorizationDenialHasNoSideEffects(t *testing.T) {
	at := time.Now()
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at)
	events := []string{}
	persistence := &pictureStoreFake{user: user, events: &events}
	content := &pictureContentFake{events: &events}
	service := newProfilePictureServiceForTest(persistence, persistence, content, &authorizerFake{events: &events, writeErr: apperror.New("authorization.denied")}, &profilePictureAuditorFake{}, &realtimetest.Publisher{}, nil, func() time.Time { return at })

	_, err := service.UploadProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), UploadProfilePictureCommand{UserID: user.ID, Body: strings.NewReader("image"), Size: 5})
	require.True(t, apperror.Is(err, "authorization.denied"), "Upload() error = %v", err)
	require.False(t, persistence.created, "denied upload side effects: created")
	require.False(t, content.normalized, "denied upload side effects: created")
	require.Equal(t, fmt.Sprint([]string{"authorize-profile-picture-write"}), fmt.Sprint(events), "denied upload side effects: created")
}

func TestGetProfilePictureAuthorizesAndMapsRendition(t *testing.T) {
	at := time.Now()
	userID := model.NewUserID()
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(userID, at)
	revisionID := model.NewFileRevisionID()
	rendition, err := model.NewFileRendition(model.NewFileRenditionID(), revisionID, "profile_256", "image/webp", 4, 256, 256, strings.Repeat("a", 64), at)
	require.NoError(t, err)
	events := []string{}
	persistence := &pictureStoreFake{user: user, rendition: rendition, events: &events}
	contentAdapter := &pictureContentFake{events: &events}
	service := newProfilePictureServiceForTest(persistence, persistence, contentAdapter, &authorizerFake{events: &events}, &profilePictureAuditorFake{}, &realtimetest.Publisher{}, nil, func() time.Time { return at })

	content, err := service.GetProfilePicture(context.Background(), invocation.New(model.Principal{UserID: userID}, model.RequestMetadata{}), GetProfilePictureQuery{UserID: userID, Size: 256})
	require.NoError(t, err)
	defer content.Body.Close()
	wantEvents := []string{"authorize-read", "get-rendition", "open"}
	require.Equal(t, "profile_256", persistence.lookupName, "Get() mapping: content=%#v lookup=%q events", content, persistence.lookupName)
	require.Equal(t, revisionID, contentAdapter.openedRevision, "Get() mapping: content=%#v lookup=%q events", content, persistence.lookupName)
	require.Equal(t, rendition.ID, contentAdapter.openedRendition, "Get() mapping: content=%#v lookup=%q events", content, persistence.lookupName)
	require.Equal(t, `"`+rendition.SHA256+`"`, content.ETag, "Get() mapping: content=%#v lookup=%q events", content, persistence.lookupName)
	require.Equal(t, "image/webp", content.MediaType, "Get() mapping: content=%#v lookup=%q events", content, persistence.lookupName)
	require.Equal(t, int64(4), content.Size, "Get() mapping: content=%#v lookup=%q events", content, persistence.lookupName)
	require.Equal(t, fmt.Sprint(wantEvents), fmt.Sprint(events), "Get() mapping: content=%#v lookup=%q events", content, persistence.lookupName)
}

func TestGetMissingDefaultRendersImmediatelyAndProposesDurableGeneration(t *testing.T) {
	at := time.Now()
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at)
	events := []string{}
	persistence := &pictureStoreFake{user: user, events: &events}
	jobs := &profilePictureDefaultJobsFake{}
	service := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{}, &authorizerFake{events: &events}, &profilePictureAuditorFake{}, &realtimetest.Publisher{}, jobs, func() time.Time { return at })

	content, err := service.GetProfilePicture(context.Background(), invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{}), GetProfilePictureQuery{UserID: user.ID, Size: 256})
	require.NoError(t, err)
	defer content.Body.Close()
	require.Equal(t, `"`+strings.Repeat("d", 64)+`"`, content.ETag, "fallback = %#v jobs=%#v user=%#v", content, jobs, user)
	require.Equal(t, int64(7), content.Size, "fallback = %#v jobs=%#v user=%#v", content, jobs, user)
	require.Equal(t, 1, jobs.count, "fallback = %#v jobs=%#v user=%#v", content, jobs, user)
	require.Equal(t, user.ID, jobs.userID, "fallback = %#v jobs=%#v user=%#v", content, jobs, user)
	require.False(t, user.ProfilePictureChangedAt.Valid, "fallback = %#v jobs=%#v user=%#v", content, jobs, user)
}

type pictureCapacityError struct{}

func (pictureCapacityError) Error() string         { return "content capacity is unavailable" }
func (pictureCapacityError) WorkCapacityExceeded() {}

func TestProfilePictureCapacityRefusalDoesNotPublishOrProposeMoreWork(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"upload", "fallback", "default generation"} {
		t.Run(operation, func(t *testing.T) {
			user := &model.User{Username: "student", Email: "student@example.test"}
			user.PrepareCreate(model.NewUserID(), time.Now())
			events := []string{}
			persistence := &pictureStoreFake{user: user, events: &events}
			jobs := &profilePictureDefaultJobsFake{}
			publisher := publisherIn(&events)
			auditor := &profilePictureAuditorFake{events: &events}
			capacity := pictureCapacityError{}
			content := &pictureContentFake{events: &events, workErr: fmt.Errorf("processing: %w", capacity)}
			service := newProfilePictureServiceForTest(persistence, persistence, content, &authorizerFake{events: &events}, auditor, publisher, jobs, time.Now)
			call := invocation.New(model.Principal{UserID: user.ID}, model.RequestMetadata{})
			var err error
			switch operation {
			case "upload":
				_, err = service.UploadProfilePicture(context.Background(), call, UploadProfilePictureCommand{UserID: user.ID, Body: strings.NewReader("image"), Size: 5})
			case "fallback":
				_, err = service.GetProfilePicture(context.Background(), call, GetProfilePictureQuery{UserID: user.ID, Size: 256})
			case "default generation":
				_, err = service.EnsureDefaultProfilePicture(context.Background(), invocation.Job(model.NewJobID(), model.NewJobAttemptID()), user.ID)
			}
			require.ErrorIs(t, err, capacity)
			require.False(t, operation != "default generation" && !apperror.Is(err, "service.busy"))
			require.Nil(t, persistence.publication, "capacity refusal published state or more work: store=%#v jobs=%#v effect=%#v audit=%#v", persistence, jobs, publisher.Calls(), auditor)
			require.Nil(t, persistence.defaultPublication, "capacity refusal published state or more work: store=%#v jobs=%#v effect=%#v audit=%#v", persistence, jobs, publisher.Calls(), auditor)
			require.Equal(t, 0, jobs.count, "capacity refusal published state or more work: store=%#v jobs=%#v effect=%#v audit=%#v", persistence, jobs, publisher.Calls(), auditor)
			require.Empty(t, publisher.Calls(), "capacity refusal published state or more work: store=%#v jobs=%#v effect=%#v audit=%#v", persistence, jobs, publisher.Calls(), auditor)
			require.Empty(t, auditor.operation, "capacity refusal published state or more work: store=%#v jobs=%#v effect=%#v audit=%#v", persistence, jobs, publisher.Calls(), auditor)
		})
	}
}

func TestEnsureDefaultProfilePictureAttachesGeneratedRenditionsIdempotently(t *testing.T) {
	at := time.Date(2026, 9, 4, 12, 0, 0, 123456789, time.UTC)
	wantAttachedAt := time.Date(2026, 9, 4, 12, 0, 0, 123456000, time.UTC)
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	persistence := &pictureStoreFake{user: user}
	generator := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{}, nil, nil, nil, nil, func() time.Time { return at })
	execution := invocation.Job(model.NewJobID(), model.NewJobAttemptID())
	entryID, err := generator.EnsureDefaultProfilePicture(context.Background(), execution, user.ID)
	require.NoError(t, err, "ensure/store = %#v", persistence)
	require.NotNil(t, persistence.defaultPublication, "ensure/store = %#v", persistence)
	require.Equal(t, persistence.defaultPublication.EntryID, entryID)
	require.Len(t, persistence.defaultPublication.Renditions, 3, "ensure/store = %#v", persistence)
	require.Equal(t, user.ID, persistence.defaultPublication.UserID, "ensure/store = %#v", persistence)
	require.True(t, persistence.defaultPublication.AttachedAt.Equal(wantAttachedAt))
	require.False(t, user.ProfilePictureChangedAt.Valid)
	attached := *user
	attached.DefaultProfilePictureFileID = persistence.defaultPublication.EntryID
	persistence.user = &attached
	second, err := generator.EnsureDefaultProfilePicture(context.Background(), execution, user.ID)
	require.NoError(t, err)
	require.Equal(t, attached.DefaultProfilePictureFileID, second)
	require.Equal(t, attached.DefaultProfilePictureFileID, persistence.defaultPublication.EntryID)
	require.Equal(t, 1, persistence.defaultPublications)
}

func TestEnsureDefaultProfilePictureRequiresItsJobIdentity(t *testing.T) {
	t.Parallel()
	user := &model.User{Username: "student", Email: "student@example.test"}
	user.PrepareCreate(model.NewUserID(), time.Now())
	persistence := &pictureStoreFake{user: user}
	generator := newProfilePictureServiceForTest(persistence, persistence, &pictureContentFake{}, nil, nil, nil, nil, time.Now)
	for name, system := range map[string]invocation.System{"zero": {}, "task": invocation.Task("startup")} {
		t.Run(name, func(t *testing.T) {
			_, err := generator.EnsureDefaultProfilePicture(context.Background(), system, user.ID)
			require.ErrorIs(t, err, ErrDefaultProfilePictureInvariant)
			require.Nil(t, persistence.defaultPublication)
		})
	}
}

// pictureUsers serves the profile-picture fake's User from the User Store.
type pictureUsers struct {
	store.UserStore
	files *pictureStoreFake
}

func (u pictureUsers) Get(ctx context.Context, id model.UserID) (*model.User, error) {
	return u.files.Get(ctx, id)
}

// profilePictureChangedCall is the publication of a committed profile-picture
// change: the event addressed to the User, carrying the active File Entry and
// the revision and change instant of the committed User.
func profilePictureChangedCall(committed *model.User, userID model.UserID, active model.FileEntryID) realtimetest.Call {
	return realtimetest.Call{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
		Name:   "user_profile_picture_changed",
		UserID: userID,
		Data: json.RawMessage(fmt.Sprintf(
			`{"active_file_entry_id":%q,"revision":%d,"changed_at":%d}`,
			active, committed.Revision, committed.ProfilePictureChangedAt.Time.UnixMilli(),
		)),
	}}
}

func newProfilePictureServiceForTest(_ *pictureStoreFake, files *pictureStoreFake, content *pictureContentFake, authorization Authorizer, audit access.MutationAuditor, publisher *realtimetest.Publisher, defaultJobs DefaultPictureJobs, now func() time.Time) *Service {
	deps := Dependencies{
		Store: &catalogFake{user: pictureUsers{files: files}, file: files, userSettings: &userSettingsStoreFake{}}, Authorizer: authorization, Auditor: audit,
		Publisher: &realtimetest.Publisher{}, DefaultJobs: defaultJobs,
		Uploads: pictureUploadFilesFake{state: content}, Reads: pictureReadFilesFake{state: content},
		Fallback: defaultPictureRenderFilesFake{state: content}, Defaults: defaultPictureGenerationFilesFake{state: content},
		Now: now,
	}
	deps.SettingsAuditor = &settingsAuditFake{}
	if publisher != nil {
		deps.Publisher = publisher
	}
	service, err := New(deps)
	if err != nil {
		panic(err)
	}
	return service
}
