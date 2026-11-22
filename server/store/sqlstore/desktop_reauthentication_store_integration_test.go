//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// desktopReauthenticationFixture is one live Desktop Session and its exact
// access credential, ready to start reauthentication.
type desktopReauthenticationFixture struct {
	institutionID model.InstitutionID
	user          *model.User
	session       *model.Session
	credentialID  model.SessionCredentialID
}

func newDesktopReauthenticationFixture(t *testing.T, ctx context.Context, persistence *SQLStore, name string) desktopReauthenticationFixture {
	t.Helper()
	institution, err := persistence.Institution().Save(ctx, &model.Institution{Name: name, DisplayName: "Desktop Reauthentication"})
	require.NoError(t, err)
	user := saveIntegrationUser(t, ctx, persistence, &model.User{Username: name, Email: name + "@example.edu"})
	_, code, state, verifier := issueDesktopAuthorizationForSQLTest(t, ctx, persistence, institution.ID, user.ID)
	audit := saveDesktopAuthorizationAuditForSQLTest(t, ctx, persistence, institution.ID, user.ID, "reauthentication-exchange")
	exchanged, err := persistence.BrowserAuthentication().Exchange(ctx, desktopAuthorizationExchangeForSQLTest(code, state, verifier, audit))
	require.NoError(t, err)
	require.NotNil(t, exchanged.Session)
	var credential string
	require.NoError(t, persistence.GetMaster().Get(ctx, &credential,
		`SELECT id FROM session_credentials WHERE session_id=? AND kind='access'`, exchanged.Session.ID.String()))
	return desktopReauthenticationFixture{institutionID: institution.ID, user: user, session: exchanged.Session,
		credentialID: model.SessionCredentialID(credential)}
}

// startDesktopReauthenticationForSQLTest creates and binds a reauthentication
// for the fixture's Desktop Session and returns its binding, state and verifier.
func startDesktopReauthenticationForSQLTest(t *testing.T, ctx context.Context, persistence *SQLStore, fixture desktopReauthenticationFixture) (string, string, string) {
	t.Helper()
	handle, proof, state, verifier := model.NewCredentialToken(), model.NewCredentialToken(), model.NewCredentialToken(), model.NewCredentialToken()
	_, err := persistence.BrowserAuthentication().CreateDesktopReauthentication(ctx, &store.DesktopReauthenticationCreation{
		ID: model.NewBrowserAuthenticationTransactionID(), InstitutionID: fixture.institutionID, Issuer: "https://proctor.example.edu",
		HandleHash: model.HashToken(handle), BrowserProofHash: model.HashToken(proof), StateHash: model.HashToken(state),
		CallbackURL: "http://127.0.0.1:49152/" + model.NewCredentialToken(), CodeChallenge: model.PKCES256Challenge(verifier),
		UserID: fixture.user.ID, SessionID: fixture.session.ID, CredentialID: fixture.credentialID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, KeyThumbprint: fixture.session.DPoPKeyThumbprint,
		Lifetime: model.BrowserAuthenticationTransactionLifetime,
	})
	require.NoError(t, err, "CreateDesktopReauthentication()")
	binding := model.NewCredentialToken()
	_, err = persistence.BrowserAuthentication().BindDesktopReauthentication(ctx, &store.DesktopAuthorizationBinding{
		HandleHash: model.HashToken(handle), BrowserProofHash: model.HashToken(proof),
		StateHash: model.HashToken(state), BindingHash: model.HashToken(binding),
	})
	require.NoError(t, err, "BindDesktopReauthentication()")
	return binding, state, verifier
}

// issueDesktopReauthenticationForSQLTest proves the fixture User's password
// and issues the one-use code.
func issueDesktopReauthenticationForSQLTest(t *testing.T, ctx context.Context, persistence *SQLStore, fixture desktopReauthenticationFixture) (string, string, string) {
	t.Helper()
	binding, state, verifier := startDesktopReauthenticationForSQLTest(t, ctx, persistence, fixture)
	result, err := persistence.BrowserAuthentication().AuthenticateDesktopReauthentication(ctx, &store.DesktopReauthenticationAuthentication{
		BindingHash: model.HashToken(binding), UserID: fixture.user.ID, AuthenticationMethod: "password",
		PasswordProof: passwordProofForSQLTest(t, ctx, persistence, fixture.user.ID), AuthenticatedAt: model.NowUTC(),
	})
	require.NoError(t, err, "AuthenticateDesktopReauthentication()")
	require.False(t, result.Denied, "AuthenticateDesktopReauthentication()")
	code := model.NewCredentialToken()
	_, err = persistence.BrowserAuthentication().IssueDesktopReauthenticationCode(ctx, &store.DesktopReauthenticationCodeIssue{
		BindingHash: model.HashToken(binding), StateHash: model.HashToken(state), CodeHash: model.HashToken(code), CodeLifetime: time.Minute,
	})
	require.NoError(t, err, "IssueDesktopReauthenticationCode()")
	return code, state, verifier
}

func desktopReauthenticationCompletionForSQLTest(t *testing.T, ctx context.Context, persistence *SQLStore, fixture desktopReauthenticationFixture,
	code, state, verifier string,
) *store.DesktopReauthenticationCompletion {
	t.Helper()
	audit, err := persistence.Audit().Save(ctx, &model.AuditEvent{ActorID: fixture.user.ID, Action: string(model.ActionAuthenticationReauthenticate),
		Resource: model.Resource{Type: model.ResourceUser, ID: fixture.user.ID.String()}, ScopeType: model.RoleScopeInstitution,
		ScopeID: fixture.institutionID.String(), Status: model.AuditStatusAttempt, NodeID: "desktop-reauthentication",
		Parameters: []byte(`{"operation":"desktop_password"}`)})
	require.NoError(t, err)
	return &store.DesktopReauthenticationCompletion{
		CodeHash: model.HashToken(code), StateHash: model.HashToken(state), CodeChallenge: model.PKCES256Challenge(verifier),
		UserID: fixture.user.ID, SessionID: fixture.session.ID, CredentialID: fixture.credentialID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, KeyThumbprint: fixture.session.DPoPKeyThumbprint,
		AuditEventID: audit.ID,
	}
}

func TestDesktopReauthenticationRefreshesTheBoundSessionWithASingleUseCode(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	fixture := newDesktopReauthenticationFixture(t, ctx, persistence, "desktop-reauth-bound")
	code, state, verifier := issueDesktopReauthenticationForSQLTest(t, ctx, persistence, fixture)

	completion := desktopReauthenticationCompletionForSQLTest(t, ctx, persistence, fixture, code, state, verifier)
	result, err := persistence.BrowserAuthentication().CompleteDesktopReauthentication(ctx, completion)
	require.NoError(t, err, "CompleteDesktopReauthentication()")
	require.Equal(t, fixture.session.ID, result.Session.ID, "completion refreshed another Session")
	require.True(t, result.Session.ReauthenticatedAt.Valid, "completion did not record fresh primary proof")
	require.False(t, result.Session.ReauthenticatedAt.Time.Before(fixture.session.AuthenticatedAt), "fresh proof preceded the Session's history")
	require.Equal(t, fixture.session.DesktopRegistrationID, result.Session.DesktopRegistrationID, "completion rotated the Desktop Registration")
	require.Equal(t, fixture.session.MFACompletedAt, result.Session.MFACompletedAt, "completion manufactured an MFA completion")
	var sessions int
	require.NoError(t, persistence.GetMaster().Get(ctx, &sessions, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, fixture.user.ID.String()))
	require.Equal(t, 1, sessions, "completion created another Session")
	var status string
	require.NoError(t, persistence.GetMaster().Get(ctx, &status, `SELECT status FROM audit_events WHERE id=?`, completion.AuditEventID.String()))
	require.Equal(t, string(model.AuditStatusSuccess), status, "completion did not commit its audit")

	replay := desktopReauthenticationCompletionForSQLTest(t, ctx, persistence, fixture, code, state, verifier)
	_, err = persistence.BrowserAuthentication().CompleteDesktopReauthentication(ctx, replay)
	require.True(t, store.IsNotFound(err), "replayed code error = %v, want not found", err)
}

func TestDesktopReauthenticationCodeIsBoundToItsSessionAndKey(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	fixture := newDesktopReauthenticationFixture(t, ctx, persistence, "desktop-reauth-key")
	code, state, verifier := issueDesktopReauthenticationForSQLTest(t, ctx, persistence, fixture)
	for name, mutate := range map[string]func(*store.DesktopReauthenticationCompletion){
		"another key": func(input *store.DesktopReauthenticationCompletion) {
			input.KeyThumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		},
		"another Session": func(input *store.DesktopReauthenticationCompletion) { input.SessionID = model.NewSessionID() },
		"another verifier": func(input *store.DesktopReauthenticationCompletion) {
			input.CodeChallenge = model.PKCES256Challenge(model.NewCredentialToken())
		},
		"another state": func(input *store.DesktopReauthenticationCompletion) {
			input.StateHash = model.HashToken(model.NewCredentialToken())
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := desktopReauthenticationCompletionForSQLTest(t, ctx, persistence, fixture, code, state, verifier)
			mutate(input)
			_, err := persistence.BrowserAuthentication().CompleteDesktopReauthentication(ctx, input)
			require.True(t, store.IsNotFound(err), "mismatched completion error = %v, want not found", err)
		})
	}
	// The mismatches consumed nothing: the exact Session and key still redeem it.
	_, err := persistence.BrowserAuthentication().CompleteDesktopReauthentication(ctx,
		desktopReauthenticationCompletionForSQLTest(t, ctx, persistence, fixture, code, state, verifier))
	require.NoError(t, err, "exact completion after refused mismatches")
}

func TestDesktopReauthenticationIsRefusedAfterTheSessionIsRevoked(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	fixture := newDesktopReauthenticationFixture(t, ctx, persistence, "desktop-reauth-revoked")
	code, state, verifier := issueDesktopReauthenticationForSQLTest(t, ctx, persistence, fixture)
	_, err := persistence.Session().Withdraw(ctx, &store.SessionWithdrawal{ID: fixture.session.ID, UserID: fixture.user.ID,
		RevokedAt: model.NowUTC(), Reason: model.SessionRevocationDesktopAuthorizationFailed})
	require.NoError(t, err)
	_, err = persistence.BrowserAuthentication().CompleteDesktopReauthentication(ctx,
		desktopReauthenticationCompletionForSQLTest(t, ctx, persistence, fixture, code, state, verifier))
	require.True(t, store.IsNotFound(err), "completion for a revoked Session error = %v, want not found", err)
	var reauthenticated bool
	require.NoError(t, persistence.GetMaster().Get(ctx, &reauthenticated,
		`SELECT reauthenticated_at IS NOT NULL FROM sessions WHERE id=?`, fixture.session.ID.String()))
	require.False(t, reauthenticated, "a revoked Session recorded fresh proof")
}

func TestDesktopReauthenticationDeniesProofOfAnotherAccount(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	fixture := newDesktopReauthenticationFixture(t, ctx, persistence, "desktop-reauth-owner")
	other := saveIntegrationUser(t, ctx, persistence, &model.User{Username: "desktop-reauth-other", Email: "desktop-reauth-other@example.edu"})
	binding, state, _ := startDesktopReauthenticationForSQLTest(t, ctx, persistence, fixture)
	result, err := persistence.BrowserAuthentication().AuthenticateDesktopReauthentication(ctx, &store.DesktopReauthenticationAuthentication{
		BindingHash: model.HashToken(binding), UserID: other.ID, AuthenticationMethod: "password",
		PasswordProof: passwordProofForSQLTest(t, ctx, persistence, other.ID), AuthenticatedAt: model.NowUTC(),
	})
	require.NoError(t, err, "AuthenticateDesktopReauthentication(other account)")
	require.True(t, result.Denied, "proof of another account was accepted")
	require.Equal(t, fixture.user.ID, result.UserID, "denial named the wrong protected account")
	_, err = persistence.BrowserAuthentication().IssueDesktopReauthenticationCode(ctx, &store.DesktopReauthenticationCodeIssue{
		BindingHash: model.HashToken(binding), StateHash: model.HashToken(state),
		CodeHash: model.HashToken(model.NewCredentialToken()), CodeLifetime: time.Minute,
	})
	require.True(t, store.IsNotFound(err), "denied reauthentication issued a code: %v", err)
}

func TestDesktopReauthenticationSecondFactorStore(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "complete"
		if changed {
			name = "password changed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			persistence := openTestStore(t)
			resetTestStore(t, persistence)
			fixture := newDesktopReauthenticationFixture(t, ctx, persistence, "reauth-factor")
			binding, browserState, verifier := startDesktopReauthenticationForSQLTest(t, ctx, persistence, fixture)
			bindingHash := model.HashToken(binding)
			current, err := persistence.BrowserAuthentication().GetDesktopReauthenticationContext(ctx, bindingHash)
			require.NoError(t, err)
			require.Equal(t, model.AuthenticationSingleFactor, current.AuthenticationStrength)
			password := passwordProofForSQLTest(t, ctx, persistence, fixture.user.ID)
			pause, err := persistence.ExternalLoginState().CreateExternalSecondFactorState(ctx, &store.ExternalSecondFactorStateCreation{
				State: &model.ExternalSecondFactorState{
					Purpose: model.ExternalAuthenticationPurposeDesktopReauthentication, UserID: fixture.user.ID,
					AuthenticationMethod: "password", BrowserAuthenticationTransactionID: current.ID,
					BindingHash: bindingHash, PrimaryAuthenticatedAt: model.NowUTC(),
					ReturnTo: "/reauthenticate/desktop", ClientType: model.SessionClientDesktop,
					PasswordCredentialID: password.ID, PasswordCredentialRevision: password.Revision,
				},
				Lifetime: time.Minute,
			})
			require.NoError(t, err)
			paused, err := persistence.BrowserAuthentication().GetDesktopReauthenticationContext(ctx, bindingHash)
			require.NoError(t, err)
			require.Equal(t, model.BrowserAuthenticationStateSecondFactorRequired, paused.State)

			credentialID := model.NewMFACredentialID()
			_, err = persistence.GetMaster().Exec(ctx, `INSERT INTO mfa_credentials
				(id, created_at, updated_at, user_id, state, encrypted_secret, encryption_key_id, activated_at, last_used_time_step)
				VALUES (?, now(), now(), ?, 'active', 'fixture', ?, now(), 1)`,
				credentialID.String(), fixture.user.ID.String(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			require.NoError(t, err)
			audit, err := persistence.Audit().Save(ctx, &model.AuditEvent{
				ActorID: fixture.user.ID, Action: string(model.ActionMFAChallenge),
				Resource:  model.Resource{Type: model.ResourceUser, ID: fixture.user.ID.String()},
				ScopeType: model.RoleScopeInstitution, ScopeID: fixture.institutionID.String(),
				Status: model.AuditStatusAttempt, NodeID: "desktop-second-factor",
			})
			require.NoError(t, err)
			if changed {
				_, err = persistence.GetMaster().Exec(ctx, `UPDATE password_credentials SET revision = revision + 1 WHERE id = ?`, password.ID.String())
				require.NoError(t, err)
			}
			now := model.NowUTC()
			consumed, err := persistence.ExternalLoginState().ConsumeExternalSecondFactorState(ctx, &store.ExternalSecondFactorStateConsumption{
				ID: pause.ID, Purpose: pause.Purpose, BindingHash: pause.BindingHash,
				CredentialID: credentialID, TimeStep: now.Unix() / 30, VerifiedAt: now, AuditEventID: audit.ID,
			})
			if changed {
				var conflict *store.ErrConflict
				require.ErrorAs(t, err, &conflict)
				require.Equal(t, "password_changed", conflict.Constraint)
				credential, getErr := persistence.MFA().GetByUser(ctx, fixture.user.ID)
				require.NoError(t, getErr)
				require.EqualValues(t, 1, credential.LastUsedTimeStep)
				return
			}
			require.NoError(t, err)
			_, err = persistence.BrowserAuthentication().AuthenticateDesktopReauthentication(ctx, &store.DesktopReauthenticationAuthentication{
				BindingHash: bindingHash, UserID: fixture.user.ID, AuthenticationMethod: "password", PasswordProof: password,
				AuthenticatedAt: consumed.PrimaryAuthenticatedAt, MFACompletedAt: consumed.ConsumedAt,
			})
			require.NoError(t, err)
			code := model.NewCredentialToken()
			_, err = persistence.BrowserAuthentication().IssueDesktopReauthenticationCode(ctx, &store.DesktopReauthenticationCodeIssue{
				BindingHash: bindingHash, StateHash: model.HashToken(browserState), CodeHash: model.HashToken(code), CodeLifetime: time.Minute,
			})
			require.NoError(t, err)
			result, err := persistence.BrowserAuthentication().CompleteDesktopReauthentication(ctx,
				desktopReauthenticationCompletionForSQLTest(t, ctx, persistence, fixture, code, browserState, verifier))
			require.NoError(t, err)
			require.Equal(t, fixture.session.ID, result.Session.ID)
			require.Equal(t, model.AuthenticationMultiFactor, result.Session.AuthenticationStrength)
			require.Equal(t, consumed.ConsumedAt, result.Session.MFACompletedAt)
			require.True(t, result.Session.ReauthenticatedAt.Valid)
			require.NotEmpty(t, result.AccessTokenHashes)
		})
	}
}
