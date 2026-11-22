// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ExternalSecondFactorSQLProbe struct {
	Activate        func(*testing.T, model.MFACredentialID)
	AddRecoveryCode func(*testing.T, model.UserID, string)
	SetGeneration   func(*testing.T, model.UserID)
	Disable         func(*testing.T, model.UserID)
	Backdate        func(*testing.T, model.ExternalSecondFactorStateID)
}

// TestExternalSecondFactorStateStore exercises replay protection and the
// generation and account fences through the public store contract.
func TestExternalSecondFactorStateStore(t *testing.T, ss store.Store, probe ExternalSecondFactorSQLProbe) {
	ctx := t.Context()
	cases := []struct {
		name     string
		change   func(*testing.T, *model.ExternalSecondFactorState)
		conflict bool
		expired  bool
	}{
		{name: "one use"},
		{name: "inactive user", change: func(t *testing.T, state *model.ExternalSecondFactorState) {
			probe.Disable(t, state.UserID)
		}, conflict: true},
		{name: "changed generation", change: func(t *testing.T, state *model.ExternalSecondFactorState) {
			probe.SetGeneration(t, state.UserID)
		}, conflict: true},
		{name: "expired", change: func(t *testing.T, state *model.ExternalSecondFactorState) {
			probe.Backdate(t, state.ID)
		}, conflict: true, expired: true},
	}
	institution := saveInstitution(t, ctx, ss)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := saveUser(t, ctx, ss)
			state := createWebSecondFactor(t, ctx, ss, user, institution)
			require.True(t, state.ExternalLoginStateID.IsValid())
			got, err := ss.ExternalLoginState().GetExternalSecondFactorStateByBinding(ctx, state.BindingHash, time.Now())
			require.NoError(t, err)
			require.Equal(t, state, got)
			_, err = ss.ExternalLoginState().GetExternalSecondFactorStateByBinding(ctx, model.HashToken(model.NewCredentialToken()), time.Now())
			require.True(t, store.IsNotFound(err))

			credential := seedMFACredentialSealedWith(t, ctx, ss, user.ID, mfaRekeyFixturePrimary)
			probe.Activate(t, credential.ID)
			input := factorConsumption(t, ctx, ss, institution, state, credential)
			wrong := *input
			wrong.BindingHash = model.HashToken(model.NewCredentialToken())
			_, err = ss.ExternalLoginState().ConsumeExternalSecondFactorState(ctx, &wrong)
			require.True(t, store.IsNotFound(err))
			if tc.change != nil {
				tc.change(t, state)
			}
			consumed, err := ss.ExternalLoginState().ConsumeExternalSecondFactorState(ctx, input)
			if tc.conflict {
				require.True(t, store.IsConflict(err), "%v", err)
				current, getErr := ss.MFA().GetByUser(ctx, user.ID)
				require.NoError(t, getErr)
				require.EqualValues(t, 1, current.LastUsedTimeStep)
				if tc.expired {
					_, err = ss.ExternalLoginState().GetExternalSecondFactorStateByBinding(ctx, state.BindingHash, time.Now())
					require.True(t, store.IsNotFound(err))
					maintained, err := ss.ExternalLoginState().Maintain(ctx, 100)
					require.NoError(t, err)
					require.Positive(t, maintained.Purged)
					require.Equal(t, 1, maintained.Terminalized)
					audit, err := ss.Audit().Get(ctx, state.AuditEventID)
					require.NoError(t, err)
					require.Equal(t, "authentication.external.second_factor_expired", audit.ErrorCode)
				}
				return
			}
			require.NoError(t, err)
			require.True(t, consumed.ConsumedAt.Valid)
			_, err = ss.ExternalLoginState().ConsumeExternalSecondFactorState(ctx, input)
			require.True(t, store.IsNotFound(err))

			next := createWebSecondFactor(t, ctx, ss, user, institution)
			nextInput := factorConsumption(t, ctx, ss, institution, next, credential)
			nextInput.TimeStep = input.TimeStep
			_, err = ss.ExternalLoginState().ConsumeExternalSecondFactorState(ctx, nextInput)
			require.True(t, store.IsNotFound(err), "%v", err)

			nextInput.TimeStep = 0
			nextInput.RecoveryCodeHash = model.HashToken(model.NewCredentialToken())
			probe.AddRecoveryCode(t, user.ID, nextInput.RecoveryCodeHash)
			_, err = ss.ExternalLoginState().ConsumeExternalSecondFactorState(ctx, nextInput)
			require.NoError(t, err)

			replay := createWebSecondFactor(t, ctx, ss, user, institution)
			replayInput := factorConsumption(t, ctx, ss, institution, replay, credential)
			replayInput.TimeStep = 0
			replayInput.RecoveryCodeHash = nextInput.RecoveryCodeHash
			_, err = ss.ExternalLoginState().ConsumeExternalSecondFactorState(ctx, replayInput)
			require.True(t, store.IsNotFound(err), "%v", err)
		})
	}
}

func createWebSecondFactor(
	t *testing.T, ctx context.Context, ss store.Store, user *model.User, institution *model.Institution,
) *model.ExternalSecondFactorState {
	t.Helper()
	const provider = "campus-cas"
	identity, err := ss.ExternalIdentity().GetByProviderSubject(ctx, provider, user.ID.String())
	if store.IsNotFound(err) {
		identity, err = ss.ExternalIdentity().Save(ctx, &model.ExternalIdentity{
			UserID: user.ID, Provider: provider, Subject: user.ID.String(),
		})
	}
	require.NoError(t, err)
	login, err := ss.ExternalLoginState().Save(ctx, &store.ExternalLoginStateCreation{
		State: &model.ExternalLoginState{Provider: provider, StateHash: model.HashToken(model.NewCredentialToken()),
			BindingHash: model.HashToken(model.NewCredentialToken()), ReturnTo: "/", ClientType: model.SessionClientWeb},
		Lifetime: time.Minute,
	})
	require.NoError(t, err)
	_, err = ss.ExternalLoginState().Consume(ctx, &store.ExternalLoginStateConsumption{
		Provider: provider, StateHash: login.StateHash, BindingHash: login.BindingHash,
	})
	require.NoError(t, err)
	audit := secondFactorTestAudit(t, ctx, ss, institution, user.ID, model.ActionAuthenticationExternalLogin)
	state, err := ss.ExternalLoginState().CreateExternalSecondFactorState(ctx, &store.ExternalSecondFactorStateCreation{
		State: &model.ExternalSecondFactorState{
			Purpose: model.ExternalAuthenticationPurposeLogin, UserID: user.ID,
			ExternalIdentityID: identity.ID, Provider: provider, AuthenticationMethod: "cas",
			BindingHash: model.HashToken(model.NewCredentialToken()), PrimaryAuthenticatedAt: time.Now(),
			ReturnTo: "/", ClientType: model.SessionClientWeb, AuditEventID: audit.ID, ExternalLoginStateID: login.ID,
		},
		Lifetime: time.Minute,
	})
	require.NoError(t, err)
	return state
}

func factorConsumption(t *testing.T, ctx context.Context, ss store.Store, institution *model.Institution,
	state *model.ExternalSecondFactorState, credential *model.MFACredential,
) *store.ExternalSecondFactorStateConsumption {
	t.Helper()
	audit := secondFactorTestAudit(t, ctx, ss, institution, state.UserID, model.ActionMFAChallenge)
	now := model.NowUTC()
	return &store.ExternalSecondFactorStateConsumption{ID: state.ID, Purpose: state.Purpose,
		BindingHash: state.BindingHash, CredentialID: credential.ID, TimeStep: now.Unix() / 30, VerifiedAt: now, AuditEventID: audit.ID}
}

func secondFactorTestAudit(t *testing.T, ctx context.Context, ss store.Store, institution *model.Institution,
	userID model.UserID, action model.Action,
) *model.AuditEvent {
	t.Helper()
	audit, err := ss.Audit().Save(ctx, &model.AuditEvent{ActorID: userID, Action: string(action),
		Resource: model.Resource{Type: model.ResourceUser, ID: userID.String()}, ScopeType: model.RoleScopeInstitution,
		ScopeID: institution.ID.String(), Status: model.AuditStatusAttempt, NodeID: "second-factor-test"})
	require.NoError(t, err)
	return audit
}
