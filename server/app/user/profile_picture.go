// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package user

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const maximumProfilePictureBytes int64 = 5 << 20

// ErrInvalidProfilePicture is returned by the image normalizer for input that
// is not an acceptable image; the service maps it to profile_picture.invalid.
var ErrInvalidProfilePicture = errors.New("invalid profile picture")

// ErrDefaultProfilePictureUserNotFound means the user no longer exists when
// the default-picture job runs.
var ErrDefaultProfilePictureUserNotFound = errors.New("default profile-picture user not found")

// ErrDefaultProfilePictureInvariant wraps a model invariant failure while
// building the default picture's records.
var ErrDefaultProfilePictureInvariant = errors.New("default profile-picture invariant failed")

// PictureFiles is the port to profile-picture bytes, which live outside the
// application.
type PictureFiles interface {
	NormalizeAndStoreProfilePicture(context.Context, model.FileRevisionID, io.Reader, int64, time.Time) ([]model.FileRendition, error)
	RemoveProfilePictureRenditions(context.Context, model.FileRevisionID, []model.FileRendition) error
	OpenProfilePictureRendition(context.Context, model.FileRevisionID, model.FileRenditionID) (io.ReadCloser, error)
	// RenderDefaultProfilePicture renders a default picture without storing it.
	RenderDefaultProfilePicture(context.Context, string, int) (*RenderedProfilePicture, error)
	GenerateAndStoreDefaultProfilePicture(context.Context, model.FileRevisionID, string, time.Time) ([]model.FileRendition, error)
}

// RenderedProfilePicture is a generated default picture. The caller owns Body
// and must close it.
type RenderedProfilePicture struct {
	Body      io.ReadCloser
	MediaType string
	Size      int64
	SHA256    string
}

type UploadProfilePictureCommand struct {
	UserID           model.UserID
	ExpectedRevision int64
	ExpectedSHA256   string
	Body             io.Reader
	Size             int64
}

type GetProfilePictureQuery struct {
	UserID model.UserID
	Size   int
}
type RemoveProfilePictureCommand struct {
	UserID         model.UserID
	ExpectedSHA256 string
}

// ProfilePictureContent is a profile picture to serve. ETag is the quoted
// SHA-256 of the bytes; the caller owns Body and must close it.
type ProfilePictureContent struct {
	Body      io.ReadCloser
	MediaType string
	Size      int64
	ETag      string
}

// UploadProfilePicture stores the renditions before publishing, so the
// picture and its audit event commit together.
func (s *Service) UploadProfilePicture(
	ctx context.Context,
	inv invocation.Invocation,
	command UploadProfilePictureCommand,
) (*model.User, error) {
	userID := command.UserID
	if !userID.IsValid() ||
		command.ExpectedRevision < 0 ||
		command.Body == nil ||
		command.Size == 0 ||
		command.Size < -1 ||
		command.Size > maximumProfilePictureBytes {
		return nil, apperror.New("request.invalid").WithField("field", "profile_picture")
	}

	if err := s.authorizer.AuthorizeProfilePictureWrite(ctx, inv, userID); err != nil {
		return nil, err
	}

	current, err := s.users.Get(ctx, userID)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}
	if command.ExpectedRevision > 0 && current.Revision != command.ExpectedRevision {
		return nil, apperror.New("user.conflict").WithField("resource", "user")
	}

	currentState, err := s.checkProfilePicturePrecondition(ctx, command, current)
	if err != nil {
		return nil, err
	}

	at := model.TimeUTC(s.now())
	staged, err := s.stageProfilePictureUpload(ctx, inv, current, at)
	if err != nil {
		return nil, err
	}

	renditions, err := s.pictures.NormalizeAndStoreProfilePicture(ctx, staged.revision.ID, command.Body, command.Size, at)
	if err != nil {
		return nil, profilePictureNormalizationError(err)
	}

	if currentState != nil && sameNormalizedProfilePicture(currentState, renditions) {
		if err = s.pictures.RemoveProfilePictureRenditions(ctx, staged.revision.ID, renditions); err != nil {
			return nil, apperror.New("profile_picture.unavailable").Wrap(err)
		}

		err = s.files.DiscardProfilePictureUpload(ctx, &store.ProfilePictureUploadDiscard{
			ActorID:                   inv.Principal().UserID,
			UserID:                    current.ID,
			ExpectedUserRevision:      current.Revision,
			ExpectedActiveEntryID:     currentState.EntryID,
			ExpectedCurrentRevisionID: currentState.RevisionID,
			UploadEntryID:             staged.entryID,
			RevisionID:                staged.revision.ID,
			LeaseID:                   staged.lease.ID,
		})
		if err != nil {
			return nil, profilePictureStoreErrors.Translate(err)
		}

		return current, nil
	}

	return s.publishProfilePicture(ctx, inv, current, staged, renditions, at)
}

type stagedProfilePictureUpload struct {
	entryID  model.FileEntryID
	revision *model.FileRevision
	lease    *model.UploadLease
}

// checkProfilePicturePrecondition returns nil state when the user has no
// profile picture.
func (s *Service) checkProfilePicturePrecondition(
	ctx context.Context,
	command UploadProfilePictureCommand,
	current *model.User,
) (*store.ProfilePictureState, error) {
	if (!current.CustomProfilePictureFileID.IsZero() || !current.DefaultProfilePictureFileID.IsZero()) &&
		strings.TrimSpace(command.ExpectedSHA256) == "" {
		return nil, apperror.New("request.invalid").WithField("field", "if_match")
	}

	var currentState *store.ProfilePictureState
	activeState, stateErr := s.files.GetProfilePictureState(ctx, current.ID)
	if stateErr == nil {
		currentState = activeState
		expectedEntryID := current.DefaultProfilePictureFileID
		if !current.CustomProfilePictureFileID.IsZero() {
			expectedEntryID = current.CustomProfilePictureFileID
		}
		if strings.TrimSpace(command.ExpectedSHA256) == "" {
			return nil, apperror.New("request.invalid").WithField("field", "if_match")
		}
		if activeState.EntryID != expectedEntryID || !profilePictureChecksumMatches(activeState, command.ExpectedSHA256) {
			return nil, apperror.New("user.conflict").WithField("resource", "profile_picture")
		}
	} else if !store.IsNotFound(stateErr) || !current.CustomProfilePictureFileID.IsZero() || !current.DefaultProfilePictureFileID.IsZero() {
		return nil, profilePictureStoreErrors.Translate(stateErr)
	}

	return currentState, nil
}

func (s *Service) stageProfilePictureUpload(
	ctx context.Context,
	inv invocation.Invocation,
	current *model.User,
	at time.Time,
) (stagedProfilePictureUpload, error) {
	entryID := current.CustomProfilePictureFileID
	var entry *model.FileEntry
	var err error
	if entryID.IsZero() {
		entry, err = model.NewFileEntry(model.NewFileEntryID(), model.FileIndexingNone, at)
		if err != nil {
			return stagedProfilePictureUpload{}, profilePictureStoreErrors.Translate(err)
		}

		entryID = entry.ID
	}

	revision, err := model.NewFileRevision(
		model.NewFileRevisionID(),
		entryID,
		model.FileAvailabilityPending,
		model.FileIndexingNotRequired,
		at,
	)
	if err != nil {
		return stagedProfilePictureUpload{}, profilePictureStoreErrors.Translate(err)
	}

	lease, err := model.NewUploadLease(model.NewUploadLeaseID(), revision.ID, inv.Principal().UserID, at, at.Add(time.Hour))
	if err != nil {
		return stagedProfilePictureUpload{}, profilePictureStoreErrors.Translate(err)
	}

	if entry != nil {
		_, err = s.files.CreateUpload(ctx, &store.FileUploadCreation{Entry: entry, Revision: revision, Lease: lease})
	} else {
		_, err = s.files.CreateRevisionUpload(ctx, &store.FileRevisionUploadCreation{EntryID: entryID, Revision: revision, Lease: lease})
	}

	if err != nil {
		return stagedProfilePictureUpload{}, profilePictureStoreErrors.Translate(err)
	}

	return stagedProfilePictureUpload{entryID: entryID, revision: revision, lease: lease}, nil
}

func profilePictureNormalizationError(err error) error {
	var capacity apperror.WorkCapacityExceeded
	if errors.As(err, &capacity) {
		return apperror.New("service.busy").Wrap(err)
	}
	if errors.Is(err, ErrInvalidProfilePicture) {
		return apperror.New("profile_picture.invalid").Wrap(err)
	}
	return apperror.New("profile_picture.unavailable").Wrap(err)
}

func (s *Service) publishProfilePicture(
	ctx context.Context,
	inv invocation.Invocation,
	current *model.User,
	staged stagedProfilePictureUpload,
	renditions []model.FileRendition,
	at time.Time,
) (*model.User, error) {
	entryID := staged.entryID
	changedAt := model.TimeUTC(s.now())
	if changedAt.Before(at) {
		changedAt = at
	}

	action := model.ActionUserManage
	if inv.Principal().UserID == current.ID {
		action = model.ActionUserProfilePictureManage
	}

	published, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     action,
			Resource:   model.Resource{Type: model.ResourceUser, ID: current.ID.String()},
			Operation:  "set_profile_picture",
			Value:      map[string]any{"user_id": current.ID.String(), "active_file_entry_id": entryID.String()},
			Prior:      map[string]any{"user": current.Auditable(), "active_file_entry_id": current.CustomProfilePictureFileID.String()},
			At:         changedAt,
		},
		func(ctx context.Context, reference access.MutationReference) (*store.ProfilePicturePublicationResult, error) {
			return s.files.PublishProfilePicture(ctx, &store.ProfilePicturePublication{
				ActorID:              inv.Principal().UserID,
				UserID:               current.ID,
				ExpectedUserRevision: current.Revision,
				EntryID:              entryID,
				RevisionID:           staged.revision.ID,
				LeaseID:              staged.lease.ID,
				Renditions:           renditions,
				ChangedAt:            changedAt,
				AuditEventID:         reference.ID,
				AuditAt:              reference.MutationAt,
			})
		},
		profilePictureStoreErrors.Translate,
	)
	if err != nil {
		// The commit may have succeeded with its acknowledgement lost, so leave the
		// staged bytes to reconciliation and lease cleanup.
		return nil, err
	}

	if err := s.publishProfilePictureChanged(ctx, published.User, published.User.CustomProfilePictureFileID); err != nil {
		s.publisher.Report(ctx, "user_profile_picture_changed", err)
	}

	return published.User, nil
}

func profilePictureChecksumMatches(state *store.ProfilePictureState, expected string) bool {
	if state == nil {
		return false
	}
	expected = strings.TrimSpace(expected)
	for _, rendition := range state.Renditions {
		if rendition.SHA256 == expected {
			return true
		}
	}
	return false
}

func sameNormalizedProfilePicture(state *store.ProfilePictureState, renditions []model.FileRendition) bool {
	if state == nil || len(state.Renditions) != len(renditions) {
		return false
	}

	currentByName := make(map[string]model.FileRendition, len(state.Renditions))
	for _, rendition := range state.Renditions {
		if _, duplicate := currentByName[rendition.Name]; duplicate {
			return false
		}

		currentByName[rendition.Name] = rendition
	}

	for _, rendition := range renditions {
		current, found := currentByName[rendition.Name]
		if !found ||
			current.MediaType != rendition.MediaType ||
			current.Size != rendition.Size ||
			current.Width != rendition.Width ||
			current.Height != rendition.Height ||
			current.SHA256 != rendition.SHA256 {
			return false
		}

		delete(currentByName, rendition.Name)
	}

	return len(currentByName) == 0
}

// RemoveProfilePicture reverts the user to their default picture.
func (s *Service) RemoveProfilePicture(
	ctx context.Context,
	inv invocation.Invocation,
	command RemoveProfilePictureCommand,
) (*model.User, error) {
	userID := command.UserID
	expectedSHA256 := strings.TrimSpace(command.ExpectedSHA256)
	if !userID.IsValid() || expectedSHA256 == "" {
		return nil, apperror.New("request.invalid").WithField("field", "if_match")
	}
	if err := s.authorizer.AuthorizeProfilePictureWrite(ctx, inv, userID); err != nil {
		return nil, err
	}

	current, err := s.users.Get(ctx, userID)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}
	if current.CustomProfilePictureFileID.IsZero() {
		return nil, apperror.New("user.conflict").WithField("resource", "profile_picture")
	}

	state, err := s.files.GetProfilePictureState(ctx, current.ID)
	if err != nil {
		return nil, profilePictureStoreErrors.Translate(err)
	}
	if state.EntryID != current.CustomProfilePictureFileID || !profilePictureChecksumMatches(state, expectedSHA256) {
		return nil, apperror.New("user.conflict").WithField("resource", "profile_picture")
	}

	action := model.ActionUserManage
	if inv.Principal().UserID == current.ID {
		action = model.ActionUserProfilePictureManage
	}

	changedAt := model.TimeUTC(s.now())
	updated, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     action,
			Resource:   model.Resource{Type: model.ResourceUser, ID: current.ID.String()},
			Operation:  "remove_profile_picture",
			Value:      map[string]any{"user_id": current.ID.String()},
			Prior:      map[string]any{"user": current.Auditable(), "active_file_entry_id": current.CustomProfilePictureFileID.String()},
			At:         changedAt,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.User, error) {
			return s.files.RemoveProfilePicture(ctx, &store.ProfilePictureRemoval{
				ActorID:                   inv.Principal().UserID,
				UserID:                    current.ID,
				ExpectedUserRevision:      current.Revision,
				EntryID:                   state.EntryID,
				ExpectedCurrentRevisionID: state.RevisionID,
				ExpectedSHA256:            expectedSHA256,
				ChangedAt:                 changedAt,
				AuditEventID:              reference.ID,
				AuditAt:                   reference.MutationAt,
			})
		},
		profilePictureStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}

	if err := s.publishProfilePictureChanged(ctx, updated, updated.DefaultProfilePictureFileID); err != nil {
		s.publisher.Report(ctx, "user_profile_picture_changed", err)
	}

	return updated, nil
}

// GetProfilePicture renders a not-yet-stored default picture on the fly and
// proposes its durable generation. Size must be 128, 256 or 512.
func (s *Service) GetProfilePicture(
	ctx context.Context,
	inv invocation.Invocation,
	query GetProfilePictureQuery,
) (*ProfilePictureContent, error) {
	id := query.UserID
	if !id.IsValid() || (query.Size != 128 && query.Size != 256 && query.Size != 512) {
		return nil, apperror.New("request.invalid")
	}
	if _, err := s.authorizer.AuthorizeProfileRead(ctx, inv, id); err != nil {
		return nil, err
	}

	user, err := s.users.Get(ctx, id)
	if err != nil {
		return nil, StoreErrors.Translate(err)
	}

	rendition, err := s.files.GetProfilePictureRendition(ctx, id, fmt.Sprintf("profile_%d", query.Size))
	if err != nil {
		if store.IsNotFound(err) && user.CustomProfilePictureFileID.IsZero() && user.DefaultProfilePictureFileID.IsZero() {
			rendered, renderErr := s.pictures.RenderDefaultProfilePicture(ctx, user.DefaultProfilePictureSeed, query.Size)
			if renderErr != nil {
				var capacity apperror.WorkCapacityExceeded
				if errors.As(renderErr, &capacity) {
					return nil, apperror.New("service.busy").Wrap(renderErr)
				}

				return nil, apperror.New("profile_picture.unavailable").Wrap(renderErr)
			}

			if s.defaultJobs != nil {
				if proposeErr := s.defaultJobs.ProposeDefaultProfilePicture(ctx, user.ID, model.TimeUTC(s.now())); proposeErr != nil {
					s.publisher.Report(ctx, "propose_default_profile_picture", proposeErr)
				}
			}

			return &ProfilePictureContent{
				Body:      rendered.Body,
				MediaType: rendered.MediaType,
				Size:      rendered.Size,
				ETag:      `"` + rendered.SHA256 + `"`,
			}, nil
		}

		return nil, profilePictureStoreErrors.Translate(err)
	}

	body, err := s.pictures.OpenProfilePictureRendition(ctx, rendition.RevisionID, rendition.ID)
	if err != nil {
		return nil, apperror.New("profile_picture.unavailable").Wrap(err)
	}

	return &ProfilePictureContent{Body: body, MediaType: rendition.MediaType, Size: rendition.Size, ETag: `"` + rendition.SHA256 + `"`}, nil
}

// EnsureDefaultProfilePicture is the durable job's use case; system must name
// the job. It is idempotent across retries.
func (s *Service) EnsureDefaultProfilePicture(
	ctx context.Context,
	system invocation.System,
	userID model.UserID,
) (model.FileEntryID, error) {
	if !system.Valid() || system.Task != "" {
		return "", fmt.Errorf("%w: default profile picture requires its Job identity", ErrDefaultProfilePictureInvariant)
	}
	return s.ensureDefaultProfilePicture(ctx, userID)
}

func (s *Service) ensureDefaultProfilePicture(ctx context.Context, userID model.UserID) (model.FileEntryID, error) {
	user, err := s.users.Get(ctx, userID)
	if err != nil {
		if store.IsNotFound(err) {
			return "", ErrDefaultProfilePictureUserNotFound
		}
		return "", err
	}

	if !user.DefaultProfilePictureFileID.IsZero() {
		return user.DefaultProfilePictureFileID, nil
	}

	at := model.TimeUTC(s.now())
	entry, err := model.NewFileEntryForPurpose(model.NewFileEntryID(), model.FilePurposeProfilePictureDefault, model.FileIndexingNone, at)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDefaultProfilePictureInvariant, err)
	}

	revision, err := model.NewFileRevision(
		model.NewFileRevisionID(),
		entry.ID,
		model.FileAvailabilityPending,
		model.FileIndexingNotRequired,
		at,
	)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDefaultProfilePictureInvariant, err)
	}

	lease, err := model.NewUploadLease(model.NewUploadLeaseID(), revision.ID, user.ID, at, at.Add(time.Hour))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDefaultProfilePictureInvariant, err)
	}
	if _, err = s.files.CreateUpload(ctx, &store.FileUploadCreation{Entry: entry, Revision: revision, Lease: lease}); err != nil {
		return "", err
	}

	renditions, err := s.pictures.GenerateAndStoreDefaultProfilePicture(ctx, revision.ID, user.DefaultProfilePictureSeed, at)
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}

	// Cancellation is honored up to here; once publication starts it runs to
	// one outcome rather than being cut off mid-commit.
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelCommit()
	published, err := s.files.PublishDefaultProfilePicture(commitCtx, &store.DefaultProfilePicturePublication{
		UserID:               user.ID,
		ExpectedUserRevision: user.Revision,
		EntryID:              entry.ID,
		RevisionID:           revision.ID,
		LeaseID:              lease.ID,
		Renditions:           renditions,
		AttachedAt:           model.TimeUTC(s.now()),
	})
	if err != nil {
		if store.IsConflict(err) {
			current, getErr := s.users.Get(ctx, userID)
			if getErr == nil && !current.DefaultProfilePictureFileID.IsZero() {
				return current.DefaultProfilePictureFileID, nil
			}
		}

		return "", err
	}

	return published.User.DefaultProfilePictureFileID, nil
}

var profilePictureStoreErrors = apperror.StoreTable{
	NotFound:    "resource.not_found",
	Conflict:    "user.conflict",
	Unavailable: "profile_picture.unavailable",
}
