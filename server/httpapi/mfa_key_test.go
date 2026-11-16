// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

// newAccountMFAKeyAPI serves the MFA key routes to an operator holding
// mfa.rekey with a strong, recent Session, over the key ring state mfa holds.
func newAccountMFAKeyAPI(t *testing.T, mfa *accountMFA, retiringKeys ...string) (http.Handler, *identityFixture) {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	now := time.Now()
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: now, MFACompletedAt: model.OptionalTimeFrom(now), ClientType: model.SessionClientWeb,
	}
	fixture := newIdentityFixture(t, identityOptions{
		Stores: identityStores{MFA: mfa, Jobs: struct{ store.JobStore }{}},
		Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionMFARekey)},
		MFA:    true, MFARetiringKeys: retiringKeys,
	})
	mfa.audits = fixture.access.Audits
	return newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, mfaKeyResource(fixture.service)), fixture
}

// The ring still opens an older key, which a rekey retires; a body carrying
// key material never reaches the use case.
func TestMFARekeyAcceptsOnlyAKeyIdentity(t *testing.T) {
	t.Parallel()
	retiring := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	retiringRing, err := secretseal.New(secretseal.Settings{EncryptionKey: retiring, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	retiringKeyID := retiringRing.PrimaryKeyID()
	mfa := newAccountMFA(newAccountSessions())
	api, fixture := newAccountMFAKeyAPI(t, mfa, retiring)
	rekey := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mfa/rekey", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer operator")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}

	response := rekey(`{"retiring_key_id":"` + retiringKeyID + `"}`)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	inputs := mfa.rekeyInputs()
	require.Len(t, inputs, 1, "the rekey reached the MFA Store once")
	require.Equal(t, retiringKeyID, inputs[0].RetiringKeyID)
	require.Equal(t, fixture.mfaSealer.PrimaryKeyID(), inputs[0].PrimaryKeyID)

	response = rekey(`{"retiring_key_id":"` + retiringKeyID + `","encryption_key":"secret"}`)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"code":"request.invalid"`, "a body carrying key material")
	require.Len(t, mfa.rekeyInputs(), 1, "the use case never saw the secret-bearing body")
	require.Len(t, fixture.access.Audits.Attempts(), 1, "only the accepted rekey was audited")
}

func TestMFAKeyStateProjectsIdentitiesAndCountsOnly(t *testing.T) {
	t.Parallel()
	mfa := newAccountMFA(newAccountSessions())
	api, fixture := newAccountMFAKeyAPI(t, mfa)
	// Another node fenced the ring and sealed secrets with a key this node
	// does not hold, after this node started.
	jobID := model.NewJobID()
	mfa.keyState = store.MFAKeyState{RequiredPrimaryKeyID: "0123456789abcdef0123456789abcdef", ActiveRekeyJobID: jobID,
		Active: []store.MFASecretKeyUsage{{KeyID: "11111111111111111111111111111111", Credentials: 4}}}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mfa/keys", nil)
	request.Header.Set("Authorization", "Bearer operator")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body mfaKeyStateResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, mfaKeyStateResponse{PrimaryKeyID: fixture.mfaSealer.PrimaryKeyID(),
		RequiredPrimaryKeyID: "0123456789abcdef0123456789abcdef", ActiveRekeyJobID: jobID.String(),
		Active: []mfaSecretKeyUsageResponse{{KeyID: "11111111111111111111111111111111", Credentials: 4}}}, body)
	require.NotContains(t, response.Body.String(), "encryption_key")
}
