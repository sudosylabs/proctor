// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
)

// accountAuthorizerFake authorizes every account-state and email action and
// records the decision for the call-order assertions of those tests.
type accountAuthorizerFake struct{ events *[]string }

func (a *accountAuthorizerFake) Authorize(context.Context, invocation.Invocation, model.Action, model.Resource) error {
	*a.events = append(*a.events, "authorize-manage")
	return nil
}

func (a *accountAuthorizerFake) AuthorizeAccountStateManage(context.Context, invocation.Invocation, model.UserID) error {
	*a.events = append(*a.events, "authorize-manage")
	return nil
}

// userSettingsSessionPrincipal is the interactive Session principal that the
// authentication tests sign a User in as.
func userSettingsSessionPrincipal(at time.Time) model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at,
	}
}

func TestProfilePictureRealtimeEffectContainsOnlySafeChangeMetadata(t *testing.T) {
	sink := &recordingRealtimeSink{}
	cluster := &recordingRealtimeCluster{}
	realtime := newTestRealtimeService(t, noopAuthenticationCache{})
	require.NoError(t, realtime.AttachRealtimeSink(sink))
	require.NoError(t, realtime.AttachRealtimeClusterFanout(cluster))
	change := user.PictureChange{UserID: model.NewUserID(), ActiveFileEntryID: model.NewFileEntryID(), Revision: 4, ChangedAt: time.UnixMilli(500)}
	require.NoError(t, (userRealtimeEffects{realtime: realtime}).PictureChanged(context.Background(), change))
	require.Len(t, sink.events, 1)
	require.Equal(t, "user_profile_picture_changed", sink.events[0].Name)
	require.Equal(t, change.UserID, sink.events[0].UserID)
	var data map[string]any
	require.NoError(t, json.Unmarshal(sink.events[0].Data, &data))
	require.Len(t, data, 3)
	require.Equal(t, change.ActiveFileEntryID.String(), data["active_file_entry_id"])
	require.Equal(t, float64(4), data["revision"])
	require.Equal(t, float64(500), data["changed_at"])
}

func TestDefaultProfilePictureJobAdapterClassifiesTheAreaSentinels(t *testing.T) {
	t.Parallel()
	classify := jobDefaultProfilePictureGenerator{}.ClassifyDefaultProfilePictureError
	require.Equal(t, "user.not_found", classify(user.ErrDefaultProfilePictureUserNotFound))
	require.Equal(t, "user.not_found", classify(errors.Join(errors.New("context"), user.ErrDefaultProfilePictureUserNotFound)))
	require.Equal(t, "job.invariant_failed", classify(user.ErrDefaultProfilePictureInvariant))
	require.Empty(t, classify(errors.New("unclassified")))
	var _ jobhandler.DefaultProfilePictureGenerator = jobDefaultProfilePictureGenerator{}
}
