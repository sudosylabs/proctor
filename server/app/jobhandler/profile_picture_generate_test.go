// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobhandler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// defaultPictureUsersFake is the User Store the real user.Service reads
// through; Get records the requested User and answers with user or err.
type defaultPictureUsersFake struct {
	store.UserStore
	user *model.User
	err  error
	got  model.UserID
}

func (f *defaultPictureUsersFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	f.got = id
	return f.user, f.err
}

type defaultPictureCatalogFake struct {
	store.Catalog
	users *defaultPictureUsersFake
}

func (f defaultPictureCatalogFake) User() store.UserStore { return f.users }

func (defaultPictureCatalogFake) RoleBinding() store.RoleBindingStore { return nil }

func (defaultPictureCatalogFake) File() store.FileStore { return nil }

func (defaultPictureCatalogFake) UserSettings() store.UserSettingsStore {
	return struct{ store.UserSettingsStore }{}
}

// defaultPictureService builds the real user.Service over the Users fake. The
// Service reaches only the User Store on these paths.
func defaultPictureService(t *testing.T, users *defaultPictureUsersFake) *user.Service {
	t.Helper()
	service, err := user.New(user.Dependencies{
		Store:           defaultPictureCatalogFake{users: users},
		SettingsAuditor: struct{ user.SettingsAuditor }{},
		Effects:         struct{ user.Effects }{},
		Now:             time.Now,
	})
	require.NoError(t, err)
	return service
}

func defaultPictureExecution(t *testing.T, userID model.UserID) job.Execution {
	t.Helper()
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	command, err := model.EncodeDefaultProfilePictureCommand(model.DefaultProfilePictureCommand{UserID: userID})
	require.NoError(t, err)
	record, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, command, userID.String(), at, at, 8)
	require.NoError(t, err)
	return job.Execution{Job: record, Attempt: &model.JobAttempt{ID: model.NewJobAttemptID(), JobID: record.ID}}
}

func TestDefaultProfilePictureHandlerRunsTheGeneratorAsItsJob(t *testing.T) {
	userID := model.NewUserID()
	entryID := model.NewFileEntryID()
	users := &defaultPictureUsersFake{user: &model.User{ID: userID, DefaultProfilePictureFileID: entryID}}
	execution := defaultPictureExecution(t, userID)

	outcome := NewDefaultProfilePictureDescriptor(defaultPictureService(t, users)).Handler.Run(context.Background(), execution)

	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.NoError(t, outcome.Err)
	require.JSONEq(t, `{"file_entry_id":"`+entryID.String()+`"}`, string(outcome.Result))
	require.Equal(t, userID, users.got)
}

func TestDefaultProfilePictureHandlerMapsGeneratorFailures(t *testing.T) {
	tests := []struct {
		name  string
		users *defaultPictureUsersFake
		kind  job.OutcomeKind
		code  string
	}{
		{name: "missing user", users: &defaultPictureUsersFake{err: store.NewErrNotFound("user", "missing")}, kind: job.OutcomePermanentFailure, code: "user.not_found"},
		// A User without a valid identifier cannot own an upload lease, which the
		// Service reports as its default-picture invariant.
		{name: "invariant", users: &defaultPictureUsersFake{user: &model.User{}}, kind: job.OutcomePermanentFailure, code: "job.invariant_failed"},
		{name: "dependency", users: &defaultPictureUsersFake{err: errors.New("generate")}, kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := NewDefaultProfilePictureDescriptor(defaultPictureService(t, tt.users)).Handler.Run(context.Background(), defaultPictureExecution(t, model.NewUserID()))
			require.Equal(t, tt.kind, outcome.Kind)
			require.Equal(t, tt.code, outcome.PublicErrorCode)
		})
	}
}

func TestDefaultProfilePictureErrorClassificationFollowsTheAreaSentinels(t *testing.T) {
	t.Parallel()
	classify := classifyDefaultProfilePictureError
	require.Equal(t, "user.not_found", classify(user.ErrDefaultProfilePictureUserNotFound))
	require.Equal(t, "user.not_found", classify(errors.Join(errors.New("context"), user.ErrDefaultProfilePictureUserNotFound)))
	require.Equal(t, "job.invariant_failed", classify(user.ErrDefaultProfilePictureInvariant))
	require.Empty(t, classify(errors.New("unclassified")))
}
