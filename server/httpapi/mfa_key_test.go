// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type mfaKeyAPIFake struct {
	state   identity.MFAKeyStateView
	started identity.StartMFARekeyCommand
	status  model.JobID
	err     error
}

func (f *mfaKeyAPIFake) GetMFAKeyState(context.Context, invocation.Invocation) (identity.MFAKeyStateView, error) {
	return f.state, f.err
}

func (f *mfaKeyAPIFake) StartMFARekey(_ context.Context, _ invocation.Invocation, command identity.StartMFARekeyCommand) (identity.MFARekeyView, error) {
	f.started = command
	return identity.MFARekeyView{JobID: model.NewJobID(), PrimaryKeyID: "0123456789abcdef0123456789abcdef", RetiringKeyID: command.RetiringKeyID}, f.err
}

func (f *mfaKeyAPIFake) GetMFARekeyStatus(_ context.Context, _ invocation.Invocation, jobID model.JobID) (identity.MFARekeyStatusView, error) {
	f.status = jobID
	return identity.MFARekeyStatusView{JobID: jobID}, f.err
}

func TestMFARekeyAcceptsOnlyAKeyIdentity(t *testing.T) {
	t.Parallel()
	fake := &mfaKeyAPIFake{}
	h := mfaKeyHandlers{app: fake}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mfa/rekey", bytes.NewBufferString(`{"retiring_key_id":"11111111111111111111111111111111"}`))
	request.Header.Set("Content-Type", "application/json")
	result, err := h.startRekey(route.Request{HTTP: request})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, result.Status())
	require.Equal(t, identity.StartMFARekeyCommand{RetiringKeyID: "11111111111111111111111111111111"}, fake.started)

	fake.started = identity.StartMFARekeyCommand{}
	secretBearing, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mfa/rekey",
		bytes.NewBufferString(`{"retiring_key_id":"11111111111111111111111111111111","encryption_key":"secret"}`))
	secretBearing.Header.Set("Content-Type", "application/json")
	_, err = h.startRekey(route.Request{HTTP: secretBearing})
	require.True(t, apperror.Is(err, "request.invalid"), "a body carrying key material: error = %v", err)
	require.Equal(t, identity.StartMFARekeyCommand{}, fake.started, "the use case never saw the secret-bearing body")
}

func TestMFAKeyStateProjectsIdentitiesAndCountsOnly(t *testing.T) {
	t.Parallel()
	jobID := model.NewJobID()
	fake := &mfaKeyAPIFake{state: identity.MFAKeyStateView{PrimaryKeyID: "0123456789abcdef0123456789abcdef",
		RequiredPrimaryKeyID: "0123456789abcdef0123456789abcdef", ActiveRekeyJobID: jobID,
		Active: []identity.MFASecretKeyUsageView{{KeyID: "11111111111111111111111111111111", Credentials: 4}}}}
	result, err := mfaKeyHandlers{app: fake}.getKeyState(route.Request{HTTP: &http.Request{}})
	require.NoError(t, err)
	require.Equal(t, mfaKeyStateResponse{PrimaryKeyID: "0123456789abcdef0123456789abcdef",
		RequiredPrimaryKeyID: "0123456789abcdef0123456789abcdef", ActiveRekeyJobID: jobID.String(),
		Active: []mfaSecretKeyUsageResponse{{KeyID: "11111111111111111111111111111111", Credentials: 4}}}, result.Body())
}
